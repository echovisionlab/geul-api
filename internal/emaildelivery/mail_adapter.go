package emaildelivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"reflect"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
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
	Type     AdapterType
	SES      *SESConfigInput
	SMTP     *SMTPConfigInput
	IsActive bool
	Priority int32
}

// CreateMailAdapter stores the adapter and invalidates every process's
// provider cache.
func (s *Service) CreateMailAdapter(ctx context.Context, command CreateMailAdapterCommand) (_ MailAdapter, err error) {
	defer failurecause.AnnotatePrivate(&err, providerConfigPrivateValues(command.SES, command.SMTP)...)
	adapter, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[MailAdapter, Store]{
		Access: freshAdmin(policyv1.MailAdapter.Create()),
		Change: func(ctx context.Context, store Store) (MailAdapter, error) {
			name := strings.TrimSpace(command.Name)
			if name == "" {
				return MailAdapter{}, domainerr.Required("name")
			}
			if len(name) > 255 {
				return MailAdapter{}, domainerr.InvalidField("name", "must be at most 255 characters")
			}
			if !command.Type.Known() {
				return MailAdapter{}, domainerr.InvalidField("type", "invalid adapter type")
			}
			config, err := buildConfig(command.Type, command.SES, command.SMTP)
			if err != nil {
				return MailAdapter{}, invalidMailAdapter(err.Error())
			}
			encoded, err := json.Marshal(config)
			if err != nil {
				return MailAdapter{}, fmt.Errorf("failed to set config: %w", err)
			}
			return store.InsertMailAdapter(ctx, MailAdapter{
				Name:      name,
				Type:      command.Type,
				IsActive:  command.IsActive,
				Priority:  int(command.Priority),
				Config:    encoded,
				CreatedAt: s.dbNow(),
			})
		},
		Audit: func(adapter MailAdapter) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMailAdapterCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMailAdapterCreatedAuditRecord(metadata, adapter.ID)
			})
		},
	})
	if err != nil {
		return MailAdapter{}, err
	}
	s.mailAdapterChanged(ctx, adapter.ID, MailAdapterCreated)
	return adapter, nil
}

// dbNow is the current time at the precision the database keeps.
func (s *Service) dbNow() time.Time {
	return s.now().UTC().Truncate(dbtime.Precision)
}

// UpdateMailAdapterCommand changes a mail adapter. Nil fields keep their
// stored values. A provider configuration for the stored provider merges
// over the stored one: an empty SES secret access key or SMTP password keeps
// the stored secret.
type UpdateMailAdapterCommand struct {
	ID       string
	Name     *string
	Type     *AdapterType
	SES      *SESConfigInput
	SMTP     *SMTPConfigInput
	IsActive *bool
	Priority *int32
}

// UpdateMailAdapter applies the change, verifying a changed sender or
// delivery setting with a provider send, and invalidates every process's
// provider cache when something changed.
func (s *Service) UpdateMailAdapter(ctx context.Context, command UpdateMailAdapterCommand) (_ MailAdapter, err error) {
	defer failurecause.AnnotatePrivate(&err, providerConfigPrivateValues(command.SES, command.SMTP)...)
	return s.updateMailAdapter(ctx, command, nil)
}

// mailAdapterUpdate is an applied update and whether it changed anything.
type mailAdapterUpdate struct {
	adapter       MailAdapter
	changedFields []string
}

func (s *Service) updateMailAdapter(ctx context.Context, command UpdateMailAdapterCommand, sender *MailAdapterSenderUpdate) (MailAdapter, error) {
	var adapter MailAdapter
	update, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[mailAdapterUpdate, Store]{
		Access: freshAdmin(policyv1.MailAdapter.Update()),
		Change: func(ctx context.Context, store Store) (mailAdapterUpdate, error) {
			var err error
			if adapter, err = store.LockMailAdapter(ctx, command.ID); err != nil {
				return mailAdapterUpdate{}, err
			}
			requested, err := s.requestedMailAdapterState(ctx, adapter, command, sender)
			if err != nil {
				return mailAdapterUpdate{}, err
			}
			changedFields, err := s.persistMailAdapterUpdate(ctx, store, &adapter, command, requested)
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
		return MailAdapter{}, classifyMailAdapterUpdateError(err, command.ID, sender, adapter)
	}
	if len(update.changedFields) != 0 {
		s.mailAdapterChanged(ctx, update.adapter.ID, MailAdapterUpdated)
	}
	return update.adapter, nil
}

// requestedMailAdapterState is the validated state an update asks for. config
// is set only when configChanged.
type requestedMailAdapterState struct {
	name          string
	adapterType   AdapterType
	config        json.RawMessage
	configChanged bool
}

func (s *Service) requestedMailAdapterState(
	ctx context.Context,
	adapter MailAdapter,
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
		if !command.Type.Known() {
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
	adapter MailAdapter,
	command UpdateMailAdapterCommand,
	sender *MailAdapterSenderUpdate,
	requested requestedMailAdapterState,
	typeChanged bool,
) (json.RawMessage, bool, error) {
	senderChanged := sender != nil && (sender.FromName != nil || sender.FromEmail != nil)
	if !typeChanged && command.SES == nil && command.SMTP == nil && !senderChanged {
		return nil, false, nil
	}
	var requestedConfig json.RawMessage
	var config structured.Value
	var err error
	if senderChanged {
		requestedConfig, config, err = senderConfig(adapter, *sender)
	} else {
		requestedConfig, config, err = replacementConfig(adapter, requested.adapterType, typeChanged, command)
	}
	if err != nil {
		return nil, false, err
	}
	// Only a changed sender or delivery setting needs a provider send to
	// prove it; resending the stored config is a no-op.
	if configJSONSemanticallyEqual(adapter.Config, requestedConfig) {
		return requestedConfig, false, nil
	}
	if err := s.verifyAdapterBeforeSave(ctx, requested.name, requested.adapterType, config); err != nil {
		if senderChanged {
			// Provider diagnostics can contain credentials. Keep their cause
			// for the redacted request record, never in an actionable MCP error.
			return nil, false, domainerr.InvalidField("config", "sender configuration verification failed; check provider configuration and sender verification").WithCause(err)
		}
		return nil, false, domainerr.InvalidField("config", err.Error())
	}
	return requestedConfig, true, nil
}

// persistMailAdapterUpdate writes the fields the command changed to adapter
// and the store. It returns the changed fields.
func (s *Service) persistMailAdapterUpdate(
	ctx context.Context,
	store Store,
	adapter *MailAdapter,
	command UpdateMailAdapterCommand,
	requested requestedMailAdapterState,
) ([]string, error) {
	var changes MailAdapterChanges
	changedFields := make([]string, 0, 5)
	if command.Name != nil && adapter.Name != requested.name {
		changes.Name = &requested.name
		adapter.Name = requested.name
		changedFields = append(changedFields, "name")
	}
	if command.IsActive != nil && adapter.IsActive != *command.IsActive {
		changes.IsActive = command.IsActive
		adapter.IsActive = *command.IsActive
		changedFields = append(changedFields, "active")
	}
	if command.Priority != nil && adapter.Priority != int(*command.Priority) {
		changes.Priority = command.Priority
		adapter.Priority = int(*command.Priority)
		changedFields = append(changedFields, "priority")
	}
	if command.Type != nil && adapter.Type != requested.adapterType {
		changes.Type = &requested.adapterType
		adapter.Type = requested.adapterType
		changedFields = append(changedFields, "type")
	}
	if requested.configChanged {
		changes.Config = requested.config
		adapter.Config = requested.config
		changedFields = append(changedFields, "config")
	}
	if len(changedFields) == 0 {
		return nil, nil
	}
	now := s.dbNow()
	changes.UpdatedAt = now
	adapter.UpdatedAt = &now
	return changedFields, store.UpdateMailAdapter(ctx, adapter.ID, changes)
}

// classifyMailAdapterUpdateError maps an update failure to the client answer.
// A sender update may carry provider credentials in its cause, so its
// adapter's secrets are marked private and an unclassified failure answers
// with a fixed message.
func classifyMailAdapterUpdateError(err error, adapterID string, sender *MailAdapterSenderUpdate, adapter MailAdapter) error {
	if errors.Is(err, ErrMailAdapterNotFound) {
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
	adapter, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[MailAdapter, Store]{
		Access: freshAdmin(policyv1.MailAdapter.Delete()),
		Change: func(ctx context.Context, store Store) (MailAdapter, error) {
			adapter, err := store.LockMailAdapter(ctx, command.ID)
			if err != nil {
				return adapter, err
			}
			return adapter, store.DeleteMailAdapter(ctx, adapter.ID)
		},
		Audit: func(adapter MailAdapter) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMailAdapterDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMailAdapterDeletedAuditRecord(metadata, adapter.ID)
			})
		},
	})
	if err != nil {
		if errors.Is(err, ErrMailAdapterNotFound) {
			return domainerr.ResourceNotFound("mail adapter", command.ID)
		}
		return err
	}
	s.mailAdapterChanged(ctx, adapter.ID, MailAdapterDeleted)
	return nil
}

// ListMailAdaptersQuery pages through mail adapters in priority order.
type ListMailAdaptersQuery struct {
	// Limit defaults and is capped as queryutil.DefaultPaginationConfig.
	Limit  int32
	Offset int32
	// ActiveOnly lists only the active adapters.
	ActiveOnly bool
}

// MailAdapterPage is one page of mail adapters and how many there are in
// all.
type MailAdapterPage struct {
	Adapters []MailAdapter
	Page     queryutil.Pagination
	Total    int64
}

// ListMailAdapters returns one page of adapters. A response built from them
// shows no secret (MailAdapter.ProviderConfig).
func (s *Service) ListMailAdapters(ctx context.Context, query ListMailAdaptersQuery) (MailAdapterPage, error) {
	can, canErr := policyv1.MailAdapter.List()
	if err := s.requireMailAdapterCan(ctx, can, canErr); err != nil {
		return MailAdapterPage{}, err
	}
	page := MailAdapterPage{Page: queryutil.ExtractPaginationRaw(query.Limit, query.Offset, queryutil.DefaultPaginationConfig)}
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		page.Adapters, page.Total, err = store.MailAdapters(ctx, MailAdapterListing{
			ActiveOnly: query.ActiveOnly, Limit: page.Page.Limit, Offset: page.Page.Offset,
		})
		return err
	})
	if err != nil {
		return MailAdapterPage{}, err
	}
	return page, nil
}

// TestSendResult is the outcome of a test email: a provider failure is the
// result, not an error.
type TestSendResult struct {
	Success bool
	// Error is the provider failure.
	Error *string
	// MessageID is the provider's message ID of an accepted email.
	MessageID *string
}

// TestMailAdapterConfigCommand sends a test email through an unsaved
// adapter configuration.
type TestMailAdapterConfigCommand struct {
	Type      AdapterType
	SES       *SESConfigInput
	SMTP      *SMTPConfigInput
	TestEmail string
}

// TestMailAdapterConfig reports whether the configuration delivered a test
// email.
func (s *Service) TestMailAdapterConfig(ctx context.Context, command TestMailAdapterConfigCommand) (_ TestSendResult, err error) {
	private := append(providerConfigPrivateValues(command.SES, command.SMTP), command.TestEmail, strings.TrimSpace(command.TestEmail))
	defer failurecause.AnnotatePrivate(&err, private...)

	can, canErr := policyv1.MailAdapter.Test()
	if err := s.requireMailAdapterCan(ctx, can, canErr); err != nil {
		return TestSendResult{}, err
	}
	testEmail, err := validateTestEmail(command.TestEmail)
	if err != nil {
		return TestSendResult{}, err
	}
	if !command.Type.Known() {
		return TestSendResult{}, domainerr.InvalidField("type", "invalid adapter type")
	}
	config, err := buildConfig(command.Type, command.SES, command.SMTP)
	if err != nil {
		return TestSendResult{}, invalidMailAdapter(err.Error())
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return TestSendResult{}, fmt.Errorf("failed to set config: %w", err)
	}
	tempAdapter := MailAdapter{ID: "test-config", Name: "Test Config", Type: command.Type, Config: encoded}
	return s.sendThroughTransientAdapter(ctx, tempAdapter, testEmail, private...), nil
}

// TestMailAdapterCommand sends a test email through a stored adapter.
type TestMailAdapterCommand struct {
	ID        string
	TestEmail string
}

// TestMailAdapter reports whether the stored adapter delivered a test email.
func (s *Service) TestMailAdapter(ctx context.Context, command TestMailAdapterCommand) (_ TestSendResult, err error) {
	private := []string{command.TestEmail, strings.TrimSpace(command.TestEmail)}
	defer func() { failurecause.AnnotatePrivate(&err, private...) }()

	can, canErr := policyv1.MailAdapter.Test()
	if err := s.requireMailAdapterCan(ctx, can, canErr); err != nil {
		return TestSendResult{}, err
	}
	testEmail, err := validateTestEmail(command.TestEmail)
	if err != nil {
		return TestSendResult{}, err
	}
	var stored MailAdapter
	if err := s.units.Read(ctx, func(store Store) (err error) {
		stored, err = store.MailAdapter(ctx, command.ID)
		return err
	}); err != nil {
		if errors.Is(err, ErrMailAdapterNotFound) {
			return TestSendResult{}, domainerr.ResourceNotFound("mail adapter", command.ID)
		}
		return TestSendResult{}, err
	}
	if cfg, err := stored.SESConfig(); err == nil {
		private = append(private, cfg.AccessKeyID, cfg.SecretAccessKey, cfg.FromEmail, cfg.FromName)
	}
	if cfg, err := stored.SMTPConfig(); err == nil {
		private = append(private, cfg.User, cfg.Password, cfg.FromEmail, cfg.FromName)
	}
	return s.sendThroughTransientAdapter(ctx, stored, testEmail, private...), nil
}

// sendThroughTransientAdapter opens a provider for one test send and
// reports its outcome.
func (s *Service) sendThroughTransientAdapter(ctx context.Context, stored MailAdapter, testEmail string, private ...string) TestSendResult {
	provider, err := s.providers.Open(stored)
	if err != nil {
		slog.ErrorContext(ctx, "Mail adapter test failed", "domain", "mail", "event", "mail.operation_failed", "operation", "test_adapter", "outcome", "failed", failurecause.Attr(err, private...))
		errStr := err.Error()
		return TestSendResult{Success: false, Error: &errStr}
	}
	defer closeTransientMailAdapter(ctx, provider)
	return sendAdapterTestEmail(ctx, provider, testEmail, private...)
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

func (s *Service) verifyAdapterBeforeSave(
	ctx context.Context,
	adapterName string,
	adapterType AdapterType,
	config structured.Value) error {
	encoded, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to serialize adapter config: %w", err)
	}
	tempAdapter := MailAdapter{ID: "verify-config", Name: adapterName, Type: adapterType, Config: encoded}

	sender, err := s.providers.Open(tempAdapter)
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
// adapter opened for one verification or test send keeps pooled.
func closeTransientMailAdapter(ctx context.Context, provider MailProvider) {
	if err := provider.Close(); err != nil {
		slog.WarnContext(ctx, "Failed to close transient mail adapter", "domain", "mail", failurecause.Attr(err))
	}
}

func probeEmailForConfig(adapterType AdapterType, config structured.Value) (string, error) {
	switch adapterType {
	case AdapterTypeLogging:
		return "", nil
	case AdapterTypeSES:
		cfg, ok := config.(*SESConfig)
		if !ok {
			return "", fmt.Errorf("invalid SES config")
		}
		return strings.TrimSpace(cfg.FromEmail), nil
	case AdapterTypeSMTP:
		cfg, ok := config.(*SMTPConfig)
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
) TestSendResult {
	result, err := sendVerificationEmail(ctx, sender, testEmail)
	if err != nil {
		private = append(private, testEmail)
		if recipient, parseErr := mail.ParseAddress(testEmail); parseErr == nil {
			private = append(private, recipient.Name)
		}
		slog.ErrorContext(ctx, "Mail adapter test failed", "domain", "mail", "event", "mail.operation_failed", "operation", "test_adapter", "outcome", "failed", failurecause.Attr(err, private...))
		errStr := err.Error()
		return TestSendResult{Success: false, Error: &errStr}
	}

	response := TestSendResult{Success: true}
	if result != nil && strings.TrimSpace(result.MessageID) != "" {
		response.MessageID = &result.MessageID
	}
	return response
}

func buildConfig(
	adapterType AdapterType,
	sesConfig *SESConfigInput,
	smtpConfig *SMTPConfigInput,
) (structured.Value, error) {
	switch adapterType {
	case AdapterTypeLogging:
		return nil, fmt.Errorf("logging adapter is non-delivery and cannot be configured")

	case AdapterTypeSES:
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
		cfg := &SESConfig{
			Region:          sesConfig.Region,
			AccessKeyID:     sesConfig.AccessKeyID,
			SecretAccessKey: sesConfig.SecretAccessKey,
			FromEmail:       sesConfig.FromEmail,
		}
		if sesConfig.FromName != nil {
			cfg.FromName = *sesConfig.FromName
		}
		return cfg, nil

	case AdapterTypeSMTP:
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
		cfg := &SMTPConfig{
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

// ProviderConfig is the stored configuration of the adapter's provider
// that a response shows. A response never shows the SES secret access key
// or SMTP password. A stored configuration that does not decode is logged
// and shown as none.
func (a MailAdapter) ProviderConfig(ctx context.Context) (*SESConfig, *SMTPConfig) {
	switch a.Type {
	case AdapterTypeSES:
		cfg, err := a.SESConfig()
		if err != nil {
			slog.ErrorContext(ctx, "Stored mail adapter config decode failed; returning adapter without config", "domain", "mail", "event", "mail.operation_failed", "operation", "decode_ses_config", "outcome", "failed", "adapter_id", a.ID, failurecause.Attr(err))
			return nil, nil
		}
		return &cfg, nil
	case AdapterTypeSMTP:
		cfg, err := a.SMTPConfig()
		if err != nil {
			slog.ErrorContext(ctx, "Stored mail adapter config decode failed; returning adapter without config", "domain", "mail", "event", "mail.operation_failed", "operation", "decode_smtp_config", "outcome", "failed", "adapter_id", a.ID, failurecause.Attr(err))
			return nil, nil
		}
		return nil, &cfg
	default:
		return nil, nil
	}
}

// mailAdapterChanged invalidates the local provider cache and publishes the
// change so other processes (including other backend instances) invalidate
// theirs. The mutation is committed; a failed publish leaves other caches
// to expire.
func (s *Service) mailAdapterChanged(ctx context.Context, adapterID string, changeType MailAdapterChangeType) {
	s.adapterCache.InvalidateCache()
	change := MailAdapterChange{AdapterID: adapterID, Type: changeType, At: s.now()}
	if err := s.adapterChanges.PublishMailAdapterChanged(ctx, change); err != nil {
		slog.ErrorContext(ctx, "Failed to publish mail adapter changed event",
			"domain", "mail",
			"event", "mail.adapter.invalidation_publish_failed",
			"adapter_id", adapterID,
			"change_type", string(changeType),
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
		"change_type", string(changeType),
	)
}
