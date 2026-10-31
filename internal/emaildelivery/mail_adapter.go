package emaildelivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"reflect"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func configJSONSemanticallyEqual(left, right []byte) bool {
	var leftValue any
	var rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func (s *Service) requireMailAdapterCan(
	ctx context.Context,
	can policyv1.Can,
	canErr error,
) error {
	if canErr != nil {
		return canErr
	}
	return authz.RequireAdminCan(ctx, s.spiceDB, can)
}

// SESConfigInput is a requested Amazon SES provider configuration.
type SESConfigInput struct {
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	FromEmail       string
	FromName        *string
}

// SMTPConfigInput is a requested SMTP provider configuration.
type SMTPConfigInput struct {
	Host      string
	Port      int32
	Secure    bool
	User      string
	Password  string
	FromEmail string
	FromName  *string
}

// privateValues are the submitted credentials and sender identity that no
// failure log may keep.
func providerConfigPrivateValues(ses *SESConfigInput, smtp *SMTPConfigInput) []string {
	var private []string
	if ses != nil {
		private = append(private, ses.AccessKeyID, ses.SecretAccessKey, ses.FromEmail, optionalString(ses.FromName))
	}
	if smtp != nil {
		private = append(private, smtp.User, smtp.Password, smtp.FromEmail, optionalString(smtp.FromName))
	}
	return private
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// CreateMailAdapterCommand creates a mail adapter. Type selects which
// provider configuration applies.
type CreateMailAdapterCommand struct {
	Name     string
	Type     model.MailAdapterType
	SES      *SESConfigInput
	SMTP     *SMTPConfigInput
	IsActive bool
	Priority int32
}

// CreateMailAdapter stores the adapter and invalidates every process's
// provider cache.
func (s *Service) CreateMailAdapter(ctx context.Context, command CreateMailAdapterCommand) (_ *managev1.MailAdapter, err error) {
	defer failurecause.AnnotatePrivate(&err, providerConfigPrivateValues(command.SES, command.SMTP)...)
	adapter, err := mutation.Run(ctx, s.mutations, mutation.Spec[*model.MailAdapter]{
		Access: freshAdmin(policyv1.MailAdapter.Create()),
		Change: func(_ context.Context, tx *gorm.DB) (*model.MailAdapter, error) {
			name := strings.TrimSpace(command.Name)
			if name == "" {
				return nil, domainerr.Required("name")
			}
			if len(name) > 255 {
				return nil, domainerr.InvalidField("name", "must be at most 255 characters")
			}
			if !knownMailAdapterType(command.Type) {
				return nil, domainerr.InvalidField("type", "invalid adapter type")
			}
			config, err := buildConfig(command.Type, command.SES, command.SMTP)
			if err != nil {
				return nil, invalidMailAdapter(err.Error())
			}
			adapter := &model.MailAdapter{
				Name:      name,
				Type:      command.Type,
				IsActive:  command.IsActive,
				Priority:  int(command.Priority),
				CreatedAt: dbtime.Now(),
			}
			if err := adapter.SetConfig(config); err != nil {
				return nil, fmt.Errorf("failed to set config: %w", err)
			}
			return adapter, tx.Omit("ID").Clauses(clause.Returning{}).Create(adapter).Error
		},
		Audit: func(adapter *model.MailAdapter) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMailAdapterCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMailAdapterCreatedAuditRecord(metadata, adapter.ID)
			})
		},
	})
	if err != nil {
		return nil, err
	}
	s.mailAdapterChanged(ctx, adapter.ID, managev1.MailAdapterChangeType_MAIL_ADAPTER_CHANGE_TYPE_CREATED)
	return toProtoMailAdapter(ctx, adapter), nil
}

// UpdateMailAdapterCommand changes a mail adapter. Nil fields keep their
// stored values. A provider configuration for the stored provider merges
// over the stored one: an empty SES secret access key or SMTP password keeps
// the stored secret.
type UpdateMailAdapterCommand struct {
	ID       string
	Name     *string
	Type     *model.MailAdapterType
	SES      *SESConfigInput
	SMTP     *SMTPConfigInput
	IsActive *bool
	Priority *int32
}

// UpdateMailAdapter applies the change, verifying a changed sender or
// delivery setting with a provider send, and invalidates every process's
// provider cache when something changed.
func (s *Service) UpdateMailAdapter(ctx context.Context, command UpdateMailAdapterCommand) (_ *managev1.MailAdapter, err error) {
	defer failurecause.AnnotatePrivate(&err, providerConfigPrivateValues(command.SES, command.SMTP)...)
	return s.updateMailAdapter(ctx, command, nil)
}

// mailAdapterUpdate is an applied update and whether it changed anything.
type mailAdapterUpdate struct {
	adapter       model.MailAdapter
	changedFields []string
}

func (s *Service) updateMailAdapter(ctx context.Context, command UpdateMailAdapterCommand, sender *MailAdapterSenderUpdate) (*managev1.MailAdapter, error) {
	var adapter model.MailAdapter
	update, err := mutation.Run(ctx, s.mutations, mutation.Spec[mailAdapterUpdate]{
		Access: freshAdmin(policyv1.MailAdapter.Update()),
		Change: func(ctx context.Context, tx *gorm.DB) (mailAdapterUpdate, error) {
			if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&adapter, "id = ?", command.ID).Error; err != nil {
				return mailAdapterUpdate{}, err
			}
			requested, err := s.requestedMailAdapterState(ctx, &adapter, command, sender)
			if err != nil {
				return mailAdapterUpdate{}, err
			}
			changedFields, err := persistMailAdapterUpdate(tx, &adapter, command, requested)
			return mailAdapterUpdate{adapter: adapter, changedFields: changedFields}, err
		},
		Audit: func(update mailAdapterUpdate) mutation.Audit {
			if len(update.changedFields) == 0 {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditMailAdapterUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMailAdapterConfigUpdatedAuditRecord(metadata, update.adapter.ID, update.changedFields)
			})
		},
	})
	if err != nil {
		return nil, classifyMailAdapterUpdateError(err, command.ID, sender, &adapter)
	}
	if len(update.changedFields) != 0 {
		s.mailAdapterChanged(ctx, update.adapter.ID, managev1.MailAdapterChangeType_MAIL_ADAPTER_CHANGE_TYPE_UPDATED)
	}
	return toProtoMailAdapter(ctx, &update.adapter), nil
}

// requestedMailAdapterState is the validated state an update asks for. config
// is set only when configChanged.
type requestedMailAdapterState struct {
	name          string
	adapterType   model.MailAdapterType
	config        model.MailAdapterConfig
	configChanged bool
}

func (s *Service) requestedMailAdapterState(
	ctx context.Context,
	adapter *model.MailAdapter,
	command UpdateMailAdapterCommand,
	sender *MailAdapterSenderUpdate,
) (requestedMailAdapterState, error) {
	requested := requestedMailAdapterState{name: adapter.Name, adapterType: adapter.Type}
	if command.Name != nil {
		name := strings.TrimSpace(*command.Name)
		if name == "" {
			return requested, domainerr.InvalidField("name", "cannot be empty")
		}
		if len(name) > 255 {
			return requested, domainerr.InvalidField("name", "must be at most 255 characters")
		}
		requested.name = name
	}
	typeChanged := false
	if command.Type != nil {
		if !knownMailAdapterType(*command.Type) {
			return requested, domainerr.InvalidField("type", "invalid adapter type")
		}
		if *command.Type != adapter.Type {
			typeChanged = true
			requested.adapterType = *command.Type
		}
	}
	var err error
	requested.config, requested.configChanged, err = s.requestedMailAdapterConfig(ctx, adapter, command, sender, requested, typeChanged)
	return requested, err
}

// requestedMailAdapterConfig builds the configuration an update asks for and
// verifies it with the provider when it differs from the stored one.
func (s *Service) requestedMailAdapterConfig(
	ctx context.Context,
	adapter *model.MailAdapter,
	command UpdateMailAdapterCommand,
	sender *MailAdapterSenderUpdate,
	requested requestedMailAdapterState,
	typeChanged bool,
) (model.MailAdapterConfig, bool, error) {
	senderChanged := sender != nil && (sender.FromName != nil || sender.FromEmail != nil)
	if !typeChanged && command.SES == nil && command.SMTP == nil && !senderChanged {
		return model.MailAdapterConfig{}, false, nil
	}
	var requestedConfig model.MailAdapterConfig
	var config structured.Value
	var err error
	if senderChanged {
		requestedConfig, config, err = senderConfig(adapter, *sender)
	} else {
		requestedConfig, config, err = replacementConfig(adapter, requested.adapterType, typeChanged, command)
	}
	if err != nil {
		return model.MailAdapterConfig{}, false, err
	}
	// Only a changed sender or delivery setting needs a provider send to
	// prove it; resending the stored config is a no-op.
	if configJSONSemanticallyEqual(adapter.Config.RawMessage, requestedConfig.RawMessage) {
		return requestedConfig, false, nil
	}
	if err := verifyAdapterBeforeSave(ctx, requested.name, requested.adapterType, config); err != nil {
		if senderChanged {
			// Provider diagnostics can contain credentials. Keep their cause
			// for the redacted request record, never in an actionable MCP error.
			return model.MailAdapterConfig{}, false, domainerr.InvalidField("config", "sender configuration verification failed; check provider configuration and sender verification").WithCause(err)
		}
		return model.MailAdapterConfig{}, false, domainerr.InvalidField("config", err.Error())
	}
	return requestedConfig, true, nil
}

// persistMailAdapterUpdate writes the fields the command changed to adapter
// and the row. It returns the changed fields.
func persistMailAdapterUpdate(
	tx *gorm.DB,
	adapter *model.MailAdapter,
	command UpdateMailAdapterCommand,
	requested requestedMailAdapterState,
) ([]string, error) {
	updates := structured.Fields{}
	changedFields := make([]string, 0, 5)
	if command.Name != nil && adapter.Name != requested.name {
		updates["name"] = requested.name
		adapter.Name = requested.name
		changedFields = append(changedFields, "name")
	}
	if command.IsActive != nil && adapter.IsActive != *command.IsActive {
		updates["is_active"] = *command.IsActive
		adapter.IsActive = *command.IsActive
		changedFields = append(changedFields, "active")
	}
	if command.Priority != nil && adapter.Priority != int(*command.Priority) {
		updates["priority"] = *command.Priority
		adapter.Priority = int(*command.Priority)
		changedFields = append(changedFields, "priority")
	}
	if command.Type != nil && adapter.Type != requested.adapterType {
		updates["type"] = requested.adapterType
		adapter.Type = requested.adapterType
		changedFields = append(changedFields, "type")
	}
	if requested.configChanged {
		updates["config"] = requested.config
		adapter.Config = requested.config
		changedFields = append(changedFields, "config")
	}
	if len(changedFields) == 0 {
		return nil, nil
	}
	now := dbtime.Now()
	updates["updated_at"] = now
	adapter.UpdatedAt = &now
	return changedFields, tx.Model(adapter).Updates(updates).Error
}

// classifyMailAdapterUpdateError maps an update failure to the client answer.
// A sender update may carry provider credentials in its cause, so its
// adapter's secrets are marked private and an unclassified failure answers
// with a fixed message.
func classifyMailAdapterUpdateError(err error, adapterID string, sender *MailAdapterSenderUpdate, adapter *model.MailAdapter) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainerr.ResourceNotFound("mail adapter", adapterID).WithCause(err)
	}
	if sender == nil {
		return err
	}
	return failurecause.WithAttrs(
		failurecause.WithPrivate(fmt.Errorf("mail adapter sender update failed: %w", err), mailAdapterPrivateValues(adapter)...),
		slog.String("adapter_id", adapterID),
	)
}

// DeleteMailAdapterCommand deletes a mail adapter.
type DeleteMailAdapterCommand struct {
	ID string
}

// DeleteMailAdapter deletes the adapter and invalidates every process's
// provider cache.
func (s *Service) DeleteMailAdapter(ctx context.Context, command DeleteMailAdapterCommand) error {
	adapter, err := mutation.Run(ctx, s.mutations, mutation.Spec[model.MailAdapter]{
		Access: freshAdmin(policyv1.MailAdapter.Delete()),
		Change: func(_ context.Context, tx *gorm.DB) (model.MailAdapter, error) {
			var adapter model.MailAdapter
			if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&adapter, "id = ?", command.ID).Error; err != nil {
				return adapter, err
			}
			return adapter, tx.Delete(&adapter).Error
		},
		Audit: func(adapter model.MailAdapter) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMailAdapterDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMailAdapterDeletedAuditRecord(metadata, adapter.ID)
			})
		},
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("mail adapter", command.ID)
		}
		return err
	}
	s.mailAdapterChanged(ctx, adapter.ID, managev1.MailAdapterChangeType_MAIL_ADAPTER_CHANGE_TYPE_DELETED)
	return nil
}

// ListMailAdaptersQuery pages through mail adapters in priority order. The
// only filter it applies is is_active = true.
type ListMailAdaptersQuery struct {
	// Limit defaults and is capped as queryutil.NormalizePaginationParams.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
}

// MailAdapterPage is one page of mail adapters.
type MailAdapterPage struct {
	Adapters   []*managev1.MailAdapter
	Pagination *commonv1.PaginationResponse
}

// ListMailAdapters returns one page of adapters without their secrets.
func (s *Service) ListMailAdapters(ctx context.Context, query ListMailAdaptersQuery) (MailAdapterPage, error) {
	can, canErr := policyv1.MailAdapter.List()
	if err := s.requireMailAdapterCan(ctx, can, canErr); err != nil {
		return MailAdapterPage{}, err
	}
	list := s.db.WithContext(ctx).Model(&model.MailAdapter{})
	for _, f := range query.Filters {
		if f == nil {
			continue
		}
		if f.GetField() == "is_active" && f.GetValue() == "true" {
			list = list.Where("is_active = ?", true)
		}
	}
	var total int64
	if err := list.Count(&total).Error; err != nil {
		return MailAdapterPage{}, err
	}
	limit, offset := queryutil.NormalizePaginationParams(&commonv1.PaginationRequest{Limit: query.Limit, Offset: query.Offset})
	var adapters []model.MailAdapter
	if err := list.Order("priority ASC, created_at ASC").Limit(int(limit)).Offset(int(offset)).Find(&adapters).Error; err != nil {
		return MailAdapterPage{}, err
	}
	protoAdapters := make([]*managev1.MailAdapter, len(adapters))
	for i := range adapters {
		protoAdapters[i] = toProtoMailAdapter(ctx, &adapters[i])
	}
	return MailAdapterPage{
		Adapters:   protoAdapters,
		Pagination: (queryutil.Pagination{Limit: limit, Offset: offset}).BuildResponse(total),
	}, nil
}

// TestMailAdapterConfigCommand sends a test email through an unsaved
// adapter configuration.
type TestMailAdapterConfigCommand struct {
	Type      model.MailAdapterType
	SES       *SESConfigInput
	SMTP      *SMTPConfigInput
	TestEmail string
}

// TestMailAdapterConfig reports whether the configuration delivered a test
// email; a provider failure is the result, not an error.
func (s *Service) TestMailAdapterConfig(ctx context.Context, command TestMailAdapterConfigCommand) (_ *managev1.TestMailAdapterResponse, err error) {
	private := append(providerConfigPrivateValues(command.SES, command.SMTP), command.TestEmail, strings.TrimSpace(command.TestEmail))
	defer failurecause.AnnotatePrivate(&err, private...)

	can, canErr := policyv1.MailAdapter.Test()
	if err := s.requireMailAdapterCan(ctx, can, canErr); err != nil {
		return nil, err
	}
	testEmail, err := validateTestEmail(command.TestEmail)
	if err != nil {
		return nil, err
	}
	if !knownMailAdapterType(command.Type) {
		return nil, domainerr.InvalidField("type", "invalid adapter type")
	}
	config, err := buildConfig(command.Type, command.SES, command.SMTP)
	if err != nil {
		return nil, invalidMailAdapter(err.Error())
	}
	tempAdapter := &model.MailAdapter{
		ID:   "test-config",
		Name: "Test Config",
		Type: command.Type,
	}
	if err := tempAdapter.SetConfig(config); err != nil {
		return nil, fmt.Errorf("failed to set config: %w", err)
	}
	return sendThroughTransientAdapter(ctx, tempAdapter, testEmail, private...), nil
}

// TestMailAdapterCommand sends a test email through a stored adapter.
type TestMailAdapterCommand struct {
	ID        string
	TestEmail string
}

// TestMailAdapter reports whether the stored adapter delivered a test email;
// a provider failure is the result, not an error.
func (s *Service) TestMailAdapter(ctx context.Context, command TestMailAdapterCommand) (_ *managev1.TestMailAdapterResponse, err error) {
	private := []string{command.TestEmail, strings.TrimSpace(command.TestEmail)}
	defer func() { failurecause.AnnotatePrivate(&err, private...) }()

	can, canErr := policyv1.MailAdapter.Test()
	if err := s.requireMailAdapterCan(ctx, can, canErr); err != nil {
		return nil, err
	}
	testEmail, err := validateTestEmail(command.TestEmail)
	if err != nil {
		return nil, err
	}
	var dbAdapter model.MailAdapter
	if err := s.db.WithContext(ctx).First(&dbAdapter, "id = ?", command.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("mail adapter", command.ID)
		}
		return nil, err
	}
	if cfg, err := dbAdapter.GetSESConfig(); err == nil {
		private = append(private, cfg.AccessKeyID, cfg.SecretAccessKey, cfg.FromEmail, cfg.FromName)
	}
	if cfg, err := dbAdapter.GetSMTPConfig(); err == nil {
		private = append(private, cfg.User, cfg.Password, cfg.FromEmail, cfg.FromName)
	}
	return sendThroughTransientAdapter(ctx, &dbAdapter, testEmail, private...), nil
}

// sendThroughTransientAdapter builds a provider for one test send and
// reports its outcome.
func sendThroughTransientAdapter(ctx context.Context, stored *model.MailAdapter, testEmail string, private ...string) *managev1.TestMailAdapterResponse {
	adapter, err := email.NewAdapterFactory().Create(stored)
	if err != nil {
		slog.ErrorContext(ctx, "Mail adapter test failed", "domain", "mail", "event", "mail.operation_failed", "operation", "test_adapter", "outcome", "failed", failurecause.Attr(err, private...))
		errStr := err.Error()
		return &managev1.TestMailAdapterResponse{Success: false, Error: &errStr}
	}
	defer closeTransientMailAdapter(ctx, adapter)
	return sendAdapterTestEmail(ctx, adapter, testEmail, private...)
}

// invalidMailAdapter rejects a mail adapter request with message.
func invalidMailAdapter(message string) error {
	return domainerr.New(domainerr.Invalid, "", message)
}

// Helper functions

func validateTestEmail(rawEmail string) (string, error) {
	testEmail := strings.TrimSpace(rawEmail)
	if testEmail == "" {
		return "", domainerr.Required("test_email")
	}
	if _, err := mail.ParseAddress(testEmail); err != nil {
		return "", domainerr.InvalidField("test_email", fmt.Sprintf("invalid email address: %s", testEmail))
	}
	return testEmail, nil
}

func verifyAdapterBeforeSave(
	ctx context.Context,
	adapterName string,
	adapterType model.MailAdapterType,
	config structured.Value) error {
	tempAdapter := &model.MailAdapter{
		ID:   "verify-config",
		Name: adapterName,
		Type: adapterType,
	}
	if err := tempAdapter.SetConfig(config); err != nil {
		return fmt.Errorf("failed to serialize adapter config: %w", err)
	}

	factory := email.NewAdapterFactory()
	sender, err := factory.Create(tempAdapter)
	if err != nil {
		return fmt.Errorf("failed to initialize adapter: %w", err)
	}
	defer closeTransientMailAdapter(ctx, sender)

	probeEmail, err := probeEmailForConfig(adapterType, config)
	if err != nil {
		return err
	}
	if probeEmail == "" {
		return nil
	}

	verifyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if _, err := sendVerificationEmail(verifyCtx, sender, probeEmail); err != nil {
		return fmt.Errorf("verification send failed: %w", err)
	}

	return nil
}

// closeTransientMailAdapter releases the provider connections that an
// adapter built for one verification or test send keeps pooled.
func closeTransientMailAdapter(ctx context.Context, adapter email.Adapter) {
	closer, ok := adapter.(io.Closer)
	if !ok {
		return
	}
	if err := closer.Close(); err != nil {
		slog.WarnContext(ctx, "Failed to close transient mail adapter", "domain", "mail", failurecause.Attr(err))
	}
}

func probeEmailForConfig(adapterType model.MailAdapterType, config structured.Value) (string, error) {
	switch adapterType {
	case model.MailAdapterTypeLogging:
		return "", nil
	case model.MailAdapterTypeSES:
		cfg, ok := config.(*model.SESAdapterConfig)
		if !ok {
			return "", fmt.Errorf("invalid SES config")
		}
		return strings.TrimSpace(cfg.FromEmail), nil
	case model.MailAdapterTypeSMTP:
		cfg, ok := config.(*model.SMTPAdapterConfig)
		if !ok {
			return "", fmt.Errorf("invalid SMTP config")
		}
		return strings.TrimSpace(cfg.FromEmail), nil
	default:
		return "", fmt.Errorf("unknown adapter type: %s", adapterType)
	}
}

func sendVerificationEmail(ctx context.Context, sender email.Sender, to string) (*email.SendResult, error) {
	testEmailMsg := &email.Email{
		To:      to,
		Subject: "Mail Adapter Verification",
		HTML:    "<h1>Mail Adapter Verification</h1><p>This email confirms your adapter configuration works.</p>",
		Text:    "Mail Adapter Verification\n\nThis email confirms your adapter configuration works.",
	}

	return sender.Send(ctx, testEmailMsg)
}

func sendAdapterTestEmail(
	ctx context.Context,
	sender email.Sender,
	testEmail string,
	private ...string,
) *managev1.TestMailAdapterResponse {
	result, err := sendVerificationEmail(ctx, sender, testEmail)
	if err != nil {
		private = append(private, testEmail)
		if recipient, parseErr := mail.ParseAddress(testEmail); parseErr == nil {
			private = append(private, recipient.Name)
		}
		slog.ErrorContext(ctx, "Mail adapter test failed", "domain", "mail", "event", "mail.operation_failed", "operation", "test_adapter", "outcome", "failed", failurecause.Attr(err, private...))
		errStr := err.Error()
		return &managev1.TestMailAdapterResponse{
			Success: false,
			Error:   &errStr,
		}
	}

	response := &managev1.TestMailAdapterResponse{
		Success: true,
	}
	if result != nil && strings.TrimSpace(result.MessageID) != "" {
		response.MessageId = &result.MessageID
	}
	return response
}

func buildConfig(
	adapterType model.MailAdapterType,
	sesConfig *SESConfigInput,
	smtpConfig *SMTPConfigInput,
) (structured.Value, error) {
	switch adapterType {
	case model.MailAdapterTypeLogging:
		return nil, fmt.Errorf("logging adapter is non-delivery and cannot be configured")

	case model.MailAdapterTypeSES:
		if sesConfig == nil {
			return nil, fmt.Errorf("SES config is required for SES adapter")
		}
		if sesConfig.Region == "" {
			return nil, fmt.Errorf("SES region is required")
		}
		if sesConfig.AccessKeyID == "" {
			return nil, fmt.Errorf("SES access key ID is required")
		}
		if sesConfig.SecretAccessKey == "" {
			return nil, fmt.Errorf("SES secret access key is required")
		}
		if sesConfig.FromEmail == "" {
			return nil, fmt.Errorf("SES from email is required")
		}
		if _, err := mail.ParseAddress(sesConfig.FromEmail); err != nil {
			return nil, fmt.Errorf("invalid SES from email: %s", sesConfig.FromEmail)
		}
		cfg := &model.SESAdapterConfig{
			Region:          sesConfig.Region,
			AccessKeyID:     sesConfig.AccessKeyID,
			SecretAccessKey: sesConfig.SecretAccessKey,
			FromEmail:       sesConfig.FromEmail,
		}
		if sesConfig.FromName != nil {
			cfg.FromName = *sesConfig.FromName
		}
		return cfg, nil

	case model.MailAdapterTypeSMTP:
		if smtpConfig == nil {
			return nil, fmt.Errorf("SMTP config is required for SMTP adapter")
		}
		if smtpConfig.Host == "" {
			return nil, fmt.Errorf("SMTP host is required")
		}
		if smtpConfig.Port == 0 {
			return nil, fmt.Errorf("SMTP port is required")
		}
		if smtpConfig.FromEmail == "" {
			return nil, fmt.Errorf("SMTP from email is required")
		}
		if _, err := mail.ParseAddress(smtpConfig.FromEmail); err != nil {
			return nil, fmt.Errorf("invalid SMTP from email: %s", smtpConfig.FromEmail)
		}
		cfg := &model.SMTPAdapterConfig{
			Host:      smtpConfig.Host,
			Port:      int(smtpConfig.Port),
			Secure:    smtpConfig.Secure,
			User:      smtpConfig.User,
			Password:  smtpConfig.Password,
			FromEmail: smtpConfig.FromEmail,
		}
		if smtpConfig.FromName != nil {
			cfg.FromName = *smtpConfig.FromName
		}
		return cfg, nil

	default:
		return nil, fmt.Errorf("unknown adapter type: %s", adapterType)
	}
}

// toProtoMailAdapter never exposes the SES secret access key or SMTP password.
func toProtoMailAdapter(ctx context.Context, a *model.MailAdapter) *managev1.MailAdapter {
	proto := &managev1.MailAdapter{
		Id:        a.ID,
		Name:      a.Name,
		Type:      mailAdapterTypeToProto(a.Type),
		IsActive:  a.IsActive,
		Priority:  int32(a.Priority),
		CreatedAt: a.CreatedAt.Format(time.RFC3339),
	}

	if a.UpdatedAt != nil {
		proto.UpdatedAt = a.UpdatedAt.Format(time.RFC3339)
	}

	switch a.Type {
	case model.MailAdapterTypeSES:
		if cfg, err := a.GetSESConfig(); err == nil {
			sesConfig := &managev1.SESConfig{
				Region:      cfg.Region,
				AccessKeyId: cfg.AccessKeyID,
				FromEmail:   cfg.FromEmail,
			}
			if cfg.FromName != "" {
				sesConfig.FromName = &cfg.FromName
			}
			proto.Config = &managev1.MailAdapter_SesConfig{SesConfig: sesConfig}
		} else {
			slog.ErrorContext(ctx, "Stored mail adapter config decode failed; returning adapter without config", "domain", "mail", "event", "mail.operation_failed", "operation", "decode_ses_config", "outcome", "failed", "adapter_id", a.ID, failurecause.Attr(err))
		}

	case model.MailAdapterTypeSMTP:
		if cfg, err := a.GetSMTPConfig(); err == nil {
			smtpConfig := &managev1.SMTPConfig{
				Host:      cfg.Host,
				Port:      int32(cfg.Port),
				Secure:    cfg.Secure,
				User:      cfg.User,
				FromEmail: cfg.FromEmail,
			}
			if cfg.FromName != "" {
				smtpConfig.FromName = &cfg.FromName
			}
			proto.Config = &managev1.MailAdapter_SmtpConfig{SmtpConfig: smtpConfig}
		} else {
			slog.ErrorContext(ctx, "Stored mail adapter config decode failed; returning adapter without config", "domain", "mail", "event", "mail.operation_failed", "operation", "decode_smtp_config", "outcome", "failed", "adapter_id", a.ID, failurecause.Attr(err))
		}
	}

	return proto
}

// mailAdapterChanged invalidates the local provider cache and publishes the
// change so other processes (including other backend instances) invalidate
// theirs. The mutation is committed; a failed publish leaves other caches
// to expire.
func (s *Service) mailAdapterChanged(ctx context.Context, adapterID string, changeType managev1.MailAdapterChangeType) {
	s.adapterCache.InvalidateCache()
	event := &managev1.MailAdapterChangedEvent{
		AdapterId:   adapterID,
		ChangeType:  changeType,
		TimestampMs: time.Now().UnixMilli(),
	}
	if err := s.adapterChanges.PublishMailAdapterChanged(ctx, event); err != nil {
		slog.ErrorContext(ctx, "Failed to publish mail adapter changed event",
			"domain", "mail",
			"event", "mail.adapter.invalidation_publish_failed",
			"adapter_id", adapterID,
			"change_type", changeType.String(),
			"mutation_applied", true,
			"cache_ttl_seconds", int(email.AdapterCacheTTL.Seconds()),
			failurecause.Attr(err),
		)
		return
	}
	slog.Info("Mail adapter invalidation published",
		"domain", "mail",
		"event", "mail.adapter.invalidation_published",
		"adapter_id", adapterID,
		"change_type", changeType.String(),
	)
}
