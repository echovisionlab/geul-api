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

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
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

// MailAdapterService implements the MailAdapterService Connect handler
type MailAdapterService struct {
	managev1connect.UnimplementedMailAdapterServiceHandler
	db            *gorm.DB
	spiceDB       *auth.SpiceDBClient
	adapterLoader *email.AdapterLoader
	publisher     MailAdapterChangePublisher
	auditWriter   domainaudit.Appender
}

// NewMailAdapterService creates a new MailAdapterService
type MailAdapterChangePublisher interface {
	PublishMailAdapterChanged(context.Context, *managev1.MailAdapterChangedEvent) error
}

func NewMailAdapterService(db *gorm.DB, adapterLoader *email.AdapterLoader, publisher MailAdapterChangePublisher, spiceDB *auth.SpiceDBClient) *MailAdapterService {
	dependencycheck.MustNotNil(db, "db")
	dependencycheck.MustNotNil(spiceDB, "spiceDB")
	return &MailAdapterService{
		db:            db,
		spiceDB:       spiceDB,
		adapterLoader: adapterLoader,
		publisher:     publisher,
	}
}

// NewAuditedMailAdapterService creates a MailAdapterService whose mutations
// require an in-transaction Domain Audit append.
func NewAuditedMailAdapterService(
	db *gorm.DB,
	adapterLoader *email.AdapterLoader,
	publisher MailAdapterChangePublisher,
	auditWriter domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
) *MailAdapterService {
	if auditWriter == nil {
		panic("mail adapter audit writer is required")
	}
	service := NewMailAdapterService(db, adapterLoader, publisher, spiceDB)
	service.auditWriter = auditWriter
	return service
}

func (s *MailAdapterService) requireMailAdapterCan(
	ctx context.Context,
	can policyv1.Can,
	canErr error,
) error {
	if canErr != nil {
		return errs.Internal(canErr)
	}
	return authz.RequireAdminCan(ctx, s.spiceDB, can)
}

// Create creates a new mail adapter
func (s *MailAdapterService) Create(
	ctx context.Context,
	req *connect.Request[managev1.CreateMailAdapterRequest],
) (_ *connect.Response[managev1.CreateMailAdapterResponse], rpcErr error) {
	defer failurecause.AnnotatePrivate(&rpcErr, mailAdapterConfigPrivateValues(req.Msg.GetSesConfig(), req.Msg.GetSmtpConfig())...)

	mailAdapterCan, err := policyv1.MailAdapter.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}

	var adapter *model.MailAdapter
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, mailAdapterCan); err != nil {
			return err
		}
		name := strings.TrimSpace(req.Msg.Name)
		if name == "" {
			return errs.Required("name")
		}
		if len(name) > 255 {
			return errs.InvalidArgument("name", "must be at most 255 characters")
		}
		adapterType := protoToModelAdapterType(req.Msg.Type)
		if adapterType == "" {
			return errs.InvalidArgument("type", "invalid adapter type")
		}
		config, err := s.buildConfig(adapterType, req.Msg.GetSesConfig(), req.Msg.GetSmtpConfig())
		if err != nil {
			return errs.InvalidArgumentMsg(err.Error())
		}
		adapter = &model.MailAdapter{
			Name:      name,
			Type:      adapterType,
			IsActive:  req.Msg.IsActive,
			Priority:  int(req.Msg.GetPriority()),
			CreatedAt: dbtime.Now(),
		}
		if err := adapter.SetConfig(config); err != nil {
			return errs.Internal(fmt.Errorf("failed to set config: %w", err))
		}
		if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(adapter).Error; err != nil {
			return err
		}
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditMailAdapterCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMailAdapterCreatedAuditRecord(metadata, adapter.ID)
		})
	}); err != nil {
		return nil, errs.Wrap(err)
	}

	// Invalidate local cache
	if s.adapterLoader != nil {
		s.adapterLoader.InvalidateCache()
	}

	// Publish change event for other processes
	s.publishAdapterChangedEvent(ctx, adapter.ID, managev1.MailAdapterChangeType_MAIL_ADAPTER_CHANGE_TYPE_CREATED)

	return connect.NewResponse(&managev1.CreateMailAdapterResponse{
		Adapter: toProtoMailAdapter(ctx, adapter),
	}), nil
}

// Update updates a mail adapter
func (s *MailAdapterService) Update(
	ctx context.Context,
	req *connect.Request[managev1.UpdateMailAdapterRequest],
) (*connect.Response[managev1.UpdateMailAdapterResponse], error) {
	return s.update(ctx, req, nil)
}

func (s *MailAdapterService) update(
	ctx context.Context,
	req *connect.Request[managev1.UpdateMailAdapterRequest],
	sender *MailAdapterSenderUpdate,
) (_ *connect.Response[managev1.UpdateMailAdapterResponse], rpcErr error) {
	defer failurecause.AnnotatePrivate(&rpcErr, mailAdapterConfigPrivateValues(req.Msg.GetSesConfig(), req.Msg.GetSmtpConfig())...)

	mailAdapterCan, err := policyv1.MailAdapter.Update()
	if err != nil {
		return nil, errs.Internal(err)
	}

	var adapter model.MailAdapter
	changed := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, mailAdapterCan); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&adapter, "id = ?", req.Msg.Id).Error; err != nil {
			return err
		}
		requested, err := s.requestedMailAdapterState(ctx, &adapter, req.Msg, sender)
		if err != nil {
			return err
		}
		changed, err = s.persistMailAdapterUpdate(ctx, tx, &adapter, req.Msg, requested)
		return err
	})
	if err != nil {
		return nil, classifyMailAdapterUpdateError(err, req.Msg.Id, sender, &adapter)
	}
	if !changed {
		return connect.NewResponse(&managev1.UpdateMailAdapterResponse{Adapter: toProtoMailAdapter(ctx, &adapter)}), nil
	}

	// Invalidate local cache
	if s.adapterLoader != nil {
		s.adapterLoader.InvalidateCache()
	}

	// Publish change event for other processes
	s.publishAdapterChangedEvent(ctx, adapter.ID, managev1.MailAdapterChangeType_MAIL_ADAPTER_CHANGE_TYPE_UPDATED)

	return connect.NewResponse(&managev1.UpdateMailAdapterResponse{
		Adapter: toProtoMailAdapter(ctx, &adapter),
	}), nil
}

// requestedMailAdapterState is the validated state an update asks for. config
// is set only when configChanged.
type requestedMailAdapterState struct {
	name          string
	adapterType   model.MailAdapterType
	config        model.MailAdapterConfig
	configChanged bool
}

func (s *MailAdapterService) requestedMailAdapterState(
	ctx context.Context,
	adapter *model.MailAdapter,
	msg *managev1.UpdateMailAdapterRequest,
	sender *MailAdapterSenderUpdate,
) (requestedMailAdapterState, error) {
	requested := requestedMailAdapterState{name: adapter.Name, adapterType: adapter.Type}
	if msg.Name != nil {
		name := strings.TrimSpace(*msg.Name)
		if name == "" {
			return requested, errs.InvalidArgument("name", "cannot be empty")
		}
		if len(name) > 255 {
			return requested, errs.InvalidArgument("name", "must be at most 255 characters")
		}
		requested.name = name
	}
	typeChanged := false
	if msg.Type != nil {
		nextType := protoToModelAdapterType(*msg.Type)
		if nextType == "" {
			return requested, errs.InvalidArgument("type", "invalid adapter type")
		}
		if nextType != adapter.Type {
			typeChanged = true
			requested.adapterType = nextType
		}
	}
	var err error
	requested.config, requested.configChanged, err = s.requestedMailAdapterConfig(ctx, adapter, msg, sender, requested, typeChanged)
	return requested, err
}

// requestedMailAdapterConfig builds the configuration an update asks for and
// verifies it with the provider when it differs from the stored one.
func (s *MailAdapterService) requestedMailAdapterConfig(
	ctx context.Context,
	adapter *model.MailAdapter,
	msg *managev1.UpdateMailAdapterRequest,
	sender *MailAdapterSenderUpdate,
	requested requestedMailAdapterState,
	typeChanged bool,
) (model.MailAdapterConfig, bool, error) {
	senderChanged := sender != nil && (sender.FromName != nil || sender.FromEmail != nil)
	if !typeChanged && msg.GetSesConfig() == nil && msg.GetSmtpConfig() == nil && !senderChanged {
		return model.MailAdapterConfig{}, false, nil
	}
	var requestedConfig model.MailAdapterConfig
	var config structured.Value
	var err error
	if senderChanged {
		requestedConfig, config, err = s.senderConfig(adapter, *sender)
	} else {
		requestedConfig, config, err = s.replacementConfig(adapter, requested.adapterType, typeChanged, msg)
	}
	if err != nil {
		return model.MailAdapterConfig{}, false, err
	}
	// Only a changed sender or delivery setting needs a provider send to
	// prove it; resending the stored config is a no-op.
	if configJSONSemanticallyEqual(adapter.Config.RawMessage, requestedConfig.RawMessage) {
		return requestedConfig, false, nil
	}
	if err := s.verifyAdapterBeforeSave(ctx, requested.name, requested.adapterType, config); err != nil {
		if senderChanged {
			// Provider diagnostics can contain credentials. Keep their cause
			// for the redacted request record, never in an actionable MCP error.
			return model.MailAdapterConfig{}, false, errs.WithCause(errs.InvalidArgument("config", "sender configuration verification failed; check provider configuration and sender verification"), err)
		}
		return model.MailAdapterConfig{}, false, errs.InvalidArgument("config", err.Error())
	}
	return requestedConfig, true, nil
}

// persistMailAdapterUpdate writes the fields the request changed to adapter
// and the row and audits them. It reports whether anything changed.
func (s *MailAdapterService) persistMailAdapterUpdate(
	ctx context.Context,
	tx *gorm.DB,
	adapter *model.MailAdapter,
	msg *managev1.UpdateMailAdapterRequest,
	requested requestedMailAdapterState,
) (bool, error) {
	updates := structured.Fields{}
	changedFields := make([]string, 0, 5)
	if msg.Name != nil && adapter.Name != requested.name {
		updates["name"] = requested.name
		adapter.Name = requested.name
		changedFields = append(changedFields, "name")
	}
	if msg.IsActive != nil && adapter.IsActive != *msg.IsActive {
		updates["is_active"] = *msg.IsActive
		adapter.IsActive = *msg.IsActive
		changedFields = append(changedFields, "active")
	}
	if msg.Priority != nil && adapter.Priority != int(*msg.Priority) {
		updates["priority"] = *msg.Priority
		adapter.Priority = int(*msg.Priority)
		changedFields = append(changedFields, "priority")
	}
	if msg.Type != nil && adapter.Type != requested.adapterType {
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
		return false, nil
	}
	now := dbtime.Now()
	updates["updated_at"] = now
	adapter.UpdatedAt = &now
	if err := tx.Model(adapter).Updates(updates).Error; err != nil {
		return true, err
	}
	if s.auditWriter == nil {
		return true, nil
	}
	return true, domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditMailAdapterUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewMailAdapterConfigUpdatedAuditRecord(metadata, adapter.ID, changedFields)
	})
}

// classifyMailAdapterUpdateError maps an update failure to the client answer.
// A sender update may carry provider credentials in its cause, so it keeps
// the cause behind a fixed client error and marks the adapter's secrets
// private.
func classifyMailAdapterUpdateError(err error, adapterID string, sender *MailAdapterSenderUpdate, adapter *model.MailAdapter) error {
	if sender != nil {
		err = failurecause.WithAttrs(
			failurecause.WithPrivate(err, mailAdapterPrivateValues(adapter)...),
			slog.String("adapter_id", adapterID),
		)
	}
	if connectErr, ok := errors.AsType[*connect.Error](err); ok {
		if sender != nil {
			// The outer context can carry provider credentials. The client sees
			// the classified error alone; the request record keeps the redacted
			// cause.
			return errs.WithCause(connectErr, err)
		}
		return errs.Internal(err)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errs.NotFound("mail adapter", adapterID)
	}
	if sender != nil {
		return errs.WithCause(errs.InternalMsg("mail adapter sender update failed"), err)
	}
	return errs.Internal(err)
}

// Delete deletes a mail adapter
func (s *MailAdapterService) Delete(
	ctx context.Context,
	req *connect.Request[managev1.DeleteMailAdapterRequest],
) (*connect.Response[managev1.DeleteMailAdapterResponse], error) {
	mailAdapterCan, err := policyv1.MailAdapter.Delete()
	if err != nil {
		return nil, errs.Internal(err)
	}

	var adapter model.MailAdapter
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, mailAdapterCan); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&adapter, "id = ?", req.Msg.Id).Error; err != nil {
			return err
		}
		if err := tx.Delete(&adapter).Error; err != nil {
			return err
		}
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditMailAdapterDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMailAdapterDeletedAuditRecord(metadata, adapter.ID)
		})
	}); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("mail adapter", req.Msg.Id)
		}
		return nil, errs.Wrap(err)
	}

	// Invalidate local cache
	if s.adapterLoader != nil {
		s.adapterLoader.InvalidateCache()
	}

	// Publish change event for other processes
	s.publishAdapterChangedEvent(ctx, adapter.ID, managev1.MailAdapterChangeType_MAIL_ADAPTER_CHANGE_TYPE_DELETED)

	return connect.NewResponse(&managev1.DeleteMailAdapterResponse{}), nil
}

// ListMailAdaptersAdmin lists all mail adapters (admin only)
func (s *MailAdapterService) ListMailAdaptersAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListMailAdaptersAdminRequest],
) (*connect.Response[managev1.ListMailAdaptersAdminResponse], error) {
	can, canErr := policyv1.MailAdapter.List()
	if err := s.requireMailAdapterCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	query := s.db.WithContext(ctx).Model(&model.MailAdapter{})

	// Apply filters (support is_active filter)
	for _, f := range req.Msg.Filters {
		if f == nil {
			continue
		}
		if f.GetField() == "is_active" && f.GetValue() == "true" {
			query = query.Where("is_active = ?", true)
		}
	}

	// Count total
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply pagination
	limit, offset := queryutil.NormalizePaginationParams(req.Msg.Pagination)

	// Default sort
	query = query.Order("priority ASC, created_at ASC")

	var adapters []model.MailAdapter
	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&adapters).Error; err != nil {
		return nil, errs.Internal(err)
	}

	protoAdapters := make([]*managev1.MailAdapter, len(adapters))
	for i := range adapters {
		protoAdapters[i] = toProtoMailAdapter(ctx, &adapters[i])
	}

	return connect.NewResponse(&managev1.ListMailAdaptersAdminResponse{
		Adapters:   protoAdapters,
		Pagination: (queryutil.Pagination{Limit: limit, Offset: offset}).BuildResponse(total),
	}), nil
}

// TestConfig tests adapter configuration before saving by sending a test email
func (s *MailAdapterService) TestConfig(
	ctx context.Context,
	req *connect.Request[managev1.TestMailAdapterConfigRequest],
) (_ *connect.Response[managev1.TestMailAdapterResponse], rpcErr error) {
	private := append(mailAdapterConfigPrivateValues(req.Msg.GetSesConfig(), req.Msg.GetSmtpConfig()), req.Msg.TestEmail, strings.TrimSpace(req.Msg.TestEmail))
	defer failurecause.AnnotatePrivate(&rpcErr, private...)

	can, canErr := policyv1.MailAdapter.Test()
	if err := s.requireMailAdapterCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	testEmail, err := validateTestEmail(req.Msg.TestEmail)
	if err != nil {
		return nil, err
	}

	adapterType := protoToModelAdapterType(req.Msg.Type)
	if adapterType == "" {
		return nil, errs.InvalidArgument("type", "invalid adapter type")
	}

	config, err := s.buildConfig(adapterType, req.Msg.GetSesConfig(), req.Msg.GetSmtpConfig())
	if err != nil {
		return nil, errs.InvalidArgumentMsg(err.Error())
	}

	tempAdapter := &model.MailAdapter{
		ID:   "test-config",
		Name: "Test Config",
		Type: adapterType,
	}
	if err := tempAdapter.SetConfig(config); err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to set config: %w", err))
	}

	factory := email.NewAdapterFactory()
	adapter, err := factory.Create(tempAdapter)
	if err != nil {
		slog.ErrorContext(ctx, "Mail adapter test failed", "domain", "mail", "event", "mail.operation_failed", "operation", "test_adapter", "outcome", "failed", failurecause.Attr(err, private...))
		errStr := err.Error()
		return connect.NewResponse(&managev1.TestMailAdapterResponse{
			Success: false,
			Error:   &errStr,
		}), nil
	}
	defer closeTransientMailAdapter(ctx, adapter)

	return sendAdapterTestEmail(ctx, adapter, testEmail, private...), nil
}

// Test tests a mail adapter by sending a test email
func (s *MailAdapterService) Test(
	ctx context.Context,
	req *connect.Request[managev1.TestMailAdapterRequest],
) (_ *connect.Response[managev1.TestMailAdapterResponse], rpcErr error) {
	private := []string{req.Msg.TestEmail, strings.TrimSpace(req.Msg.TestEmail)}
	defer func() { failurecause.AnnotatePrivate(&rpcErr, private...) }()

	can, canErr := policyv1.MailAdapter.Test()
	if err := s.requireMailAdapterCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	testEmail, err := validateTestEmail(req.Msg.TestEmail)
	if err != nil {
		return nil, err
	}

	// Get adapter from DB
	var dbAdapter model.MailAdapter
	if err := s.db.WithContext(ctx).First(&dbAdapter, "id = ?", req.Msg.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("mail adapter", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}

	if cfg, err := dbAdapter.GetSESConfig(); err == nil {
		private = append(private, cfg.AccessKeyID, cfg.SecretAccessKey, cfg.FromEmail, cfg.FromName)
	}
	if cfg, err := dbAdapter.GetSMTPConfig(); err == nil {
		private = append(private, cfg.User, cfg.Password, cfg.FromEmail, cfg.FromName)
	}

	// Create adapter instance
	factory := email.NewAdapterFactory()
	adapter, err := factory.Create(&dbAdapter)
	if err != nil {
		slog.ErrorContext(ctx, "Mail adapter test failed", "domain", "mail", "event", "mail.operation_failed", "operation", "test_adapter", "outcome", "failed", failurecause.Attr(err, private...))
		errStr := err.Error()
		return connect.NewResponse(&managev1.TestMailAdapterResponse{
			Success: false,
			Error:   &errStr,
		}), nil
	}
	defer closeTransientMailAdapter(ctx, adapter)

	return sendAdapterTestEmail(ctx, adapter, testEmail, private...), nil
}

// Helper functions

func validateTestEmail(rawEmail string) (string, error) {
	testEmail := strings.TrimSpace(rawEmail)
	if testEmail == "" {
		return "", errs.Required("test_email")
	}
	if _, err := mail.ParseAddress(testEmail); err != nil {
		return "", errs.InvalidArgument("test_email", fmt.Sprintf("invalid email address: %s", testEmail))
	}
	return testEmail, nil
}

func (s *MailAdapterService) verifyAdapterBeforeSave(
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
	case model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_LOGGING.String()):
		return "", nil
	case model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES.String()):
		cfg, ok := config.(*model.SESAdapterConfig)
		if !ok {
			return "", fmt.Errorf("invalid SES config")
		}
		return strings.TrimSpace(cfg.FromEmail), nil
	case model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP.String()):
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
) *connect.Response[managev1.TestMailAdapterResponse] {
	result, err := sendVerificationEmail(ctx, sender, testEmail)
	if err != nil {
		private = append(private, testEmail)
		if recipient, parseErr := mail.ParseAddress(testEmail); parseErr == nil {
			private = append(private, recipient.Name)
		}
		slog.ErrorContext(ctx, "Mail adapter test failed", "domain", "mail", "event", "mail.operation_failed", "operation", "test_adapter", "outcome", "failed", failurecause.Attr(err, private...))
		errStr := err.Error()
		return connect.NewResponse(&managev1.TestMailAdapterResponse{
			Success: false,
			Error:   &errStr,
		})
	}

	response := &managev1.TestMailAdapterResponse{
		Success: true,
	}
	if result != nil && strings.TrimSpace(result.MessageID) != "" {
		response.MessageId = &result.MessageID
	}
	return connect.NewResponse(response)
}

func protoToModelAdapterType(t managev1.MailAdapterType) model.MailAdapterType {
	switch t {
	case managev1.MailAdapterType_MAIL_ADAPTER_TYPE_LOGGING:
		return model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_LOGGING.String())
	case managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES:
		return model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES.String())
	case managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP:
		return model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP.String())
	default:
		return ""
	}
}

func modelToProtoAdapterType(t model.MailAdapterType) managev1.MailAdapterType {
	switch t {
	case model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_LOGGING.String()):
		return managev1.MailAdapterType_MAIL_ADAPTER_TYPE_LOGGING
	case model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES.String()):
		return managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES
	case model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP.String()):
		return managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP
	default:
		return managev1.MailAdapterType_MAIL_ADAPTER_TYPE_UNSPECIFIED
	}
}

func (s *MailAdapterService) buildConfig(
	adapterType model.MailAdapterType,
	sesConfig *managev1.SESConfig,
	smtpConfig *managev1.SMTPConfig,
) (structured.Value, error) {
	switch adapterType {
	case model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_LOGGING.String()):
		return nil, fmt.Errorf("logging adapter is non-delivery and cannot be configured")

	case model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES.String()):
		if sesConfig == nil {
			return nil, fmt.Errorf("SES config is required for SES adapter")
		}
		if sesConfig.Region == "" {
			return nil, fmt.Errorf("SES region is required")
		}
		if sesConfig.AccessKeyId == "" {
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
			AccessKeyID:     sesConfig.AccessKeyId,
			SecretAccessKey: sesConfig.SecretAccessKey,
			FromEmail:       sesConfig.FromEmail,
		}
		if sesConfig.FromName != nil {
			cfg.FromName = *sesConfig.FromName
		}
		return cfg, nil

	case model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP.String()):
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
		Type:      modelToProtoAdapterType(a.Type),
		IsActive:  a.IsActive,
		Priority:  int32(a.Priority),
		CreatedAt: a.CreatedAt.Format(time.RFC3339),
	}

	if a.UpdatedAt != nil {
		proto.UpdatedAt = a.UpdatedAt.Format(time.RFC3339)
	}

	switch a.Type {
	case model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES.String()):
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

	case model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP.String()):
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

// publishAdapterChangedEvent publishes a mail adapter changed event to PGMQ.
// This notifies other processes (including other backend instances) to invalidate their caches.
func (s *MailAdapterService) publishAdapterChangedEvent(ctx context.Context, adapterID string, changeType managev1.MailAdapterChangeType) {
	if s.publisher == nil {
		return
	}

	event := &managev1.MailAdapterChangedEvent{
		AdapterId:   adapterID,
		ChangeType:  changeType,
		TimestampMs: time.Now().UnixMilli(),
	}

	if err := s.publisher.PublishMailAdapterChanged(ctx, event); err != nil {
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

// mailAdapterConfigPrivateValues are the submitted credentials and sender
// identity that no failure log may keep.
func mailAdapterConfigPrivateValues(ses *managev1.SESConfig, smtp *managev1.SMTPConfig) []string {
	var private []string
	if ses != nil {
		private = append(private, ses.AccessKeyId, ses.SecretAccessKey, ses.FromEmail, ses.GetFromName())
	}
	if smtp != nil {
		private = append(private, smtp.User, smtp.Password, smtp.FromEmail, smtp.GetFromName())
	}
	return private
}
