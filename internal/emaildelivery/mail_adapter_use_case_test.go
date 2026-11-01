package emaildelivery_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (f *fixture) update(t *testing.T, ctx principal, request *managev1.UpdateMailAdapterRequest) (*managev1.MailAdapter, error) {
	t.Helper()
	response, err := emaildelivery.NewMailAdapterConnectHandler(f.delivery).Update(ctx.ctx, connect.NewRequest(request))
	if err != nil {
		return nil, err
	}
	return response.Msg.Adapter, nil
}

// requireResponse pins the masked response projection: provider secrets
// never leave the service.
func requireResponse(t *testing.T, response *managev1.MailAdapter, stored emaildelivery.MailAdapter, fromEmail string) {
	t.Helper()
	require.Equal(t, stored.ID, response.GetId())
	require.Equal(t, stored.Name, response.GetName())
	require.Equal(t, stored.IsActive, response.GetIsActive())
	require.EqualValues(t, stored.Priority, response.GetPriority())
	require.Equal(t, stored.CreatedAt.Format(time.RFC3339), response.GetCreatedAt())
	require.Equal(t, stored.UpdatedAt.Format(time.RFC3339), response.GetUpdatedAt())
	switch config := response.GetConfig().(type) {
	case *managev1.MailAdapter_SmtpConfig:
		require.Equal(t, fromEmail, config.SmtpConfig.GetFromEmail())
		require.Empty(t, config.SmtpConfig.GetPassword())
	case *managev1.MailAdapter_SesConfig:
		require.Equal(t, fromEmail, config.SesConfig.GetFromEmail())
		require.Empty(t, config.SesConfig.GetSecretAccessKey())
	default:
		t.Fatalf("unexpected config %T", config)
	}
}

func TestUpdateMailAdapterMetadataSkipsVerification(t *testing.T) {
	f := newFixture(t)
	adapter := f.seedSMTP("")
	name, active, priority := "  Renamed adapter  ", true, int32(7)

	response, err := f.update(t, f.admin, &managev1.UpdateMailAdapterRequest{Id: adapter.ID, Name: &name, IsActive: &active, Priority: &priority})

	require.NoError(t, err)
	stored := f.storedAdapter(t, adapter.ID)
	require.Equal(t, "Renamed adapter", stored.Name)
	require.True(t, stored.IsActive)
	require.Equal(t, 7, stored.Priority)
	require.JSONEq(t, string(adapter.Config), string(stored.Config))
	require.True(t, stored.UpdatedAt.After(*adapter.UpdatedAt))
	requireResponse(t, response, stored, "smtp-sender@example.test")
	require.Empty(t, f.providers.recipients())
	f.requireAudit(t, sharedtelemetry.AuditMailAdapterUpdated, adapter.ID, []string{"active", "name", "priority"})
	f.changes.requireOne(t, adapter.ID, emaildelivery.MailAdapterUpdated)
	require.Equal(t, 1, f.cache.invalidated())
}

func TestUpdateMailAdapterSemanticNoOpWritesNothing(t *testing.T) {
	f := newFixture(t)
	adapter := f.seedSMTP("")
	name, active, priority := adapter.Name, adapter.IsActive, int32(adapter.Priority)

	response, err := f.update(t, f.admin, &managev1.UpdateMailAdapterRequest{Id: adapter.ID, Name: &name, IsActive: &active, Priority: &priority})

	require.NoError(t, err)
	requireResponse(t, response, adapter, "smtp-sender@example.test")
	require.Empty(t, f.providers.recipients())
	f.requireUnchanged(t, adapter)
	require.Equal(t, mutation.StepCommit, f.units.Steps()[len(f.units.Steps())-1])
}

func TestUpdateMailAdapterConfigVerifiesAndKeepsStoredPasswordAndUnknownKeys(t *testing.T) {
	f := newFixture(t)
	adapter := f.seedSMTP(`"provider_hint":"kept"`)

	response, err := f.update(t, f.admin, &managev1.UpdateMailAdapterRequest{
		Id: adapter.ID,
		Config: &managev1.UpdateMailAdapterRequest_SmtpConfig{SmtpConfig: &managev1.SMTPConfig{
			Host: "127.0.0.1", Port: 2525, User: "smtp-user", FromEmail: "replaced-sender@example.test",
		}},
	})

	require.NoError(t, err)
	require.Equal(t, []string{"replaced-sender@example.test"}, f.providers.recipients())
	f.providers.requireAllClosed(t)
	stored := f.storedAdapter(t, adapter.ID)
	// The admin form never sees the stored password, so an empty one keeps
	// it; an omitted sender name clears it.
	require.JSONEq(t, `{
		"host":"127.0.0.1","port":2525,"secure":false,
		"user":"smtp-user","password":"smtp-secret","from_email":"replaced-sender@example.test",
		"provider_hint":"kept"
	}`, string(stored.Config))
	requireResponse(t, response, stored, "replaced-sender@example.test")
	f.requireAudit(t, sharedtelemetry.AuditMailAdapterUpdated, adapter.ID, []string{"config"})
	f.changes.requireOne(t, adapter.ID, emaildelivery.MailAdapterUpdated)
}

func TestUpdateMailAdapterNewPasswordReplacesTheStoredOne(t *testing.T) {
	f := newFixture(t)
	adapter := f.seedSMTP("")
	fromName := "SMTP Sender"

	_, err := f.update(t, f.admin, &managev1.UpdateMailAdapterRequest{
		Id: adapter.ID,
		Config: &managev1.UpdateMailAdapterRequest_SmtpConfig{SmtpConfig: &managev1.SMTPConfig{
			Host: "127.0.0.1", Port: 2525, User: "smtp-user", Password: "rotated-secret",
			FromEmail: "smtp-sender@example.test", FromName: &fromName,
		}},
	})

	require.NoError(t, err)
	require.Equal(t, []string{"smtp-sender@example.test"}, f.providers.recipients())
	config, err := f.storedAdapter(t, adapter.ID).SMTPConfig()
	require.NoError(t, err)
	require.Equal(t, "rotated-secret", config.Password)
	f.requireAudit(t, sharedtelemetry.AuditMailAdapterUpdated, adapter.ID, []string{"config"})
	f.changes.requireOne(t, adapter.ID, emaildelivery.MailAdapterUpdated)
}

func TestUpdateMailAdapterResendingTheStoredConfigSkipsVerification(t *testing.T) {
	f := newFixture(t)
	adapter := f.seedSMTP("")
	fromName := "SMTP Sender"
	// With and without the stored password, the request matches the stored
	// config.
	for _, password := range []string{"smtp-secret", ""} {
		_, err := f.update(t, f.admin, &managev1.UpdateMailAdapterRequest{
			Id: adapter.ID,
			Config: &managev1.UpdateMailAdapterRequest_SmtpConfig{SmtpConfig: &managev1.SMTPConfig{
				Host: "127.0.0.1", Port: 2525, User: "smtp-user", Password: password,
				FromEmail: "smtp-sender@example.test", FromName: &fromName,
			}},
		})
		require.NoError(t, err)
	}
	require.Empty(t, f.providers.recipients())
	f.requireUnchanged(t, adapter)
}

func TestUpdateMailAdapterProviderSwitchVerifiesTheNewProvider(t *testing.T) {
	f := newFixture(t)
	adapter := f.store.SeedMailAdapter(stored(emaildelivery.AdapterTypeSES, sesConfig))
	smtpType := managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP

	response, err := f.update(t, f.admin, &managev1.UpdateMailAdapterRequest{
		Id: adapter.ID, Type: &smtpType,
		Config: &managev1.UpdateMailAdapterRequest_SmtpConfig{SmtpConfig: &managev1.SMTPConfig{
			Host: "127.0.0.1", Port: 2525, FromEmail: "switched@example.test",
		}},
	})

	require.NoError(t, err)
	require.Equal(t, []string{"switched@example.test"}, f.providers.recipients())
	require.Equal(t, emaildelivery.AdapterTypeSMTP, f.providers.opened[0].Type, "the new provider verifies")
	f.providers.requireAllClosed(t)
	stored := f.storedAdapter(t, adapter.ID)
	require.Equal(t, emaildelivery.AdapterTypeSMTP, stored.Type)
	require.JSONEq(t, `{
		"host":"127.0.0.1","port":2525,"secure":false,
		"user":"","password":"","from_email":"switched@example.test"
	}`, string(stored.Config))
	requireResponse(t, response, stored, "switched@example.test")
	f.requireAudit(t, sharedtelemetry.AuditMailAdapterUpdated, adapter.ID, []string{"config", "type"})
	f.changes.requireOne(t, adapter.ID, emaildelivery.MailAdapterUpdated)
}

func TestUpdateMailAdapterSenderKeepsCredentialsAndUnknownFields(t *testing.T) {
	f := newFixture(t)
	adapter := f.seedSMTP(`"provider_hint":"kept"`)
	fromName, fromEmail := "Renamed Sender", "renamed-sender@example.test"

	updated, err := f.delivery.UpdateMailAdapterSender(f.admin.ctx, emaildelivery.MailAdapterSenderUpdate{
		ID: adapter.ID, FromName: &fromName, FromEmail: &fromEmail,
	})

	require.NoError(t, err)
	require.Equal(t, []string{"renamed-sender@example.test"}, f.providers.recipients())
	f.providers.requireAllClosed(t)
	stored := f.storedAdapter(t, adapter.ID)
	require.JSONEq(t, `{
		"host":"127.0.0.1","port":2525,"secure":false,
		"user":"smtp-user","password":"smtp-secret","from_email":"renamed-sender@example.test",
		"from_name":"Renamed Sender","provider_hint":"kept"
	}`, string(stored.Config))
	require.Equal(t, stored, updated)
	f.requireAudit(t, sharedtelemetry.AuditMailAdapterUpdated, adapter.ID, []string{"config"})
	f.changes.requireOne(t, adapter.ID, emaildelivery.MailAdapterUpdated)
}

func TestUpdateMailAdapterUnchangedSenderSkipsVerification(t *testing.T) {
	f := newFixture(t)
	adapter := f.seedSMTP("")
	fromName := "SMTP Sender"

	_, err := f.delivery.UpdateMailAdapterSender(f.admin.ctx, emaildelivery.MailAdapterSenderUpdate{ID: adapter.ID, FromName: &fromName})

	require.NoError(t, err)
	require.Empty(t, f.providers.recipients())
	f.requireUnchanged(t, adapter)
}

func TestUpdateMailAdapterRejectionsChangeNothing(t *testing.T) {
	missingID := uuid.NewString()
	emptyName, longName := "   ", strings.Repeat("n", 256)
	invalidType := managev1.MailAdapterType(99)
	loggingType := managev1.MailAdapterType_MAIL_ADAPTER_TYPE_LOGGING
	sesType := managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES
	badSender := "not-an-address"
	rejected := errors.New("550 5.7.1 sender rejected")
	for _, tc := range []struct {
		name      string
		outsider  bool
		rejecting bool
		update    func(id string) *managev1.UpdateMailAdapterRequest
		sender    func(id string) emaildelivery.MailAdapterSenderUpdate
		code      connect.Code
		message   string
	}{
		{
			name: "missing adapter",
			update: func(string) *managev1.UpdateMailAdapterRequest {
				return &managev1.UpdateMailAdapterRequest{Id: missingID, Name: &emptyName}
			},
			code: connect.CodeNotFound, message: "mail adapter not found: " + missingID,
		},
		{
			name: "missing adapter is hidden from a non administrator", outsider: true,
			update: func(string) *managev1.UpdateMailAdapterRequest {
				return &managev1.UpdateMailAdapterRequest{Id: missingID}
			},
			code: connect.CodePermissionDenied, message: "admin access required",
		},
		{
			name: "non administrator", outsider: true,
			update: func(id string) *managev1.UpdateMailAdapterRequest {
				return &managev1.UpdateMailAdapterRequest{Id: id, Name: &longName}
			},
			code: connect.CodePermissionDenied, message: "admin access required",
		},
		{
			name: "blank name",
			update: func(id string) *managev1.UpdateMailAdapterRequest {
				return &managev1.UpdateMailAdapterRequest{Id: id, Name: &emptyName}
			},
			code: connect.CodeInvalidArgument, message: "name: cannot be empty",
		},
		{
			name: "long name",
			update: func(id string) *managev1.UpdateMailAdapterRequest {
				return &managev1.UpdateMailAdapterRequest{Id: id, Name: &longName}
			},
			code: connect.CodeInvalidArgument, message: "name: must be at most 255 characters",
		},
		{
			name: "unknown type",
			update: func(id string) *managev1.UpdateMailAdapterRequest {
				return &managev1.UpdateMailAdapterRequest{Id: id, Type: &invalidType}
			},
			code: connect.CodeInvalidArgument, message: "type: invalid adapter type",
		},
		{
			name: "logging type",
			update: func(id string) *managev1.UpdateMailAdapterRequest {
				return &managev1.UpdateMailAdapterRequest{Id: id, Type: &loggingType}
			},
			code: connect.CodeInvalidArgument, message: "logging adapter is non-delivery and cannot be configured",
		},
		{
			name: "type switch without config",
			update: func(id string) *managev1.UpdateMailAdapterRequest {
				return &managev1.UpdateMailAdapterRequest{Id: id, Type: &sesType}
			},
			code: connect.CodeInvalidArgument, message: "SES config is required for SES adapter",
		},
		{
			name: "invalid smtp sender",
			update: func(id string) *managev1.UpdateMailAdapterRequest {
				return &managev1.UpdateMailAdapterRequest{Id: id, Config: &managev1.UpdateMailAdapterRequest_SmtpConfig{
					SmtpConfig: &managev1.SMTPConfig{Host: "127.0.0.1", Port: 25, FromEmail: badSender},
				}}
			},
			code: connect.CodeInvalidArgument, message: "invalid SMTP from email: not-an-address",
		},
		{
			name: "provider rejects verification", rejecting: true,
			update: func(id string) *managev1.UpdateMailAdapterRequest {
				return &managev1.UpdateMailAdapterRequest{Id: id, Config: &managev1.UpdateMailAdapterRequest_SmtpConfig{
					SmtpConfig: &managev1.SMTPConfig{Host: "127.0.0.1", Port: 2525, FromEmail: "rejected@example.test"},
				}}
			},
			code: connect.CodeInvalidArgument, message: "config: verification send failed: " + rejected.Error(),
		},
		{
			name: "sender with invalid address",
			sender: func(id string) emaildelivery.MailAdapterSenderUpdate {
				return emaildelivery.MailAdapterSenderUpdate{ID: id, FromEmail: &badSender}
			},
			code: connect.CodeInvalidArgument, message: "invalid SMTP from email: not-an-address",
		},
		{
			name: "sender verification failure hides provider detail", rejecting: true,
			sender: func(id string) emaildelivery.MailAdapterSenderUpdate {
				fromEmail := "rejected-sender@example.test"
				return emaildelivery.MailAdapterSenderUpdate{ID: id, FromEmail: &fromEmail}
			},
			code:    connect.CodeInvalidArgument,
			message: "config: sender configuration verification failed; check provider configuration and sender verification",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.rejecting {
				f.providers.sendErr = rejected
			}
			adapter := f.seedSMTP("")
			caller := f.admin
			if tc.outsider {
				caller = f.outsider
			}
			var err error
			if tc.sender != nil {
				_, err = f.delivery.UpdateMailAdapterSender(caller.ctx, tc.sender(adapter.ID))
			} else {
				_, err = f.update(t, caller, tc.update(adapter.ID))
			}
			requireAnswer(t, tc.code, tc.message, err)
			f.requireUnchanged(t, adapter)
			require.Empty(t, f.providers.recipients())
			f.providers.requireAllClosed(t)
			require.Equal(t, mutation.StepRollback, f.units.Steps()[len(f.units.Steps())-1])
		})
	}
}

func TestDeleteMailAdapterAuthorizesBeforeLoading(t *testing.T) {
	f := newFixture(t)
	adapter := f.seedSMTP("")
	for _, id := range []string{adapter.ID, uuid.NewString()} {
		err := f.delivery.DeleteMailAdapter(f.outsider.ctx, emaildelivery.DeleteMailAdapterCommand{ID: id})
		requireAnswer(t, connect.CodePermissionDenied, "admin access required", err)
	}
	f.requireUnchanged(t, adapter)
}

func TestMailAdapterLifecycleAuditsExactChangesWithoutSecrets(t *testing.T) {
	f := newFixture(t)
	handler := emaildelivery.NewMailAdapterConnectHandler(f.delivery)

	created, err := handler.Create(f.admin.ctx, connect.NewRequest(&managev1.CreateMailAdapterRequest{
		Name: " Created audited mail adapter ", Type: managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES, Priority: new(int32(4)),
		Config: &managev1.CreateMailAdapterRequest_SesConfig{SesConfig: &managev1.SESConfig{
			Region: "ap-northeast-2", AccessKeyId: "created-mail-key",
			SecretAccessKey: "created-mail-secret", FromEmail: "created-mail@example.test",
		}},
	}))
	require.NoError(t, err)
	createdID := created.Msg.Adapter.Id
	stored := f.storedAdapter(t, createdID)
	require.Equal(t, "Created audited mail adapter", stored.Name)
	require.Equal(t, 4, stored.Priority)
	require.JSONEq(t, `{"region":"ap-northeast-2","access_key_id":"created-mail-key","secret_access_key":"created-mail-secret","from_email":"created-mail@example.test"}`, string(stored.Config))
	requireResponse(t, created.Msg.Adapter, stored, "created-mail@example.test")
	require.Empty(t, f.providers.recipients(), "a create stores the config without a provider send")
	f.changes.requireOne(t, createdID, emaildelivery.MailAdapterCreated)

	_, err = handler.Delete(f.admin.ctx, connect.NewRequest(&managev1.DeleteMailAdapterRequest{Id: createdID}))
	require.NoError(t, err)
	_, ok := f.store.StoredMailAdapter(createdID)
	require.False(t, ok)
	_, err = handler.Delete(f.admin.ctx, connect.NewRequest(&managev1.DeleteMailAdapterRequest{Id: createdID}))
	requireAnswer(t, connect.CodeNotFound, "mail adapter not found: "+createdID, err)

	records := f.audit.Records()
	require.Len(t, records, 2)
	require.Equal(t, sharedtelemetry.AuditMailAdapterCreated, records[0].Action)
	require.Equal(t, sharedtelemetry.AuditMailAdapterDeleted, records[1].Action)
	for _, record := range records {
		require.Equal(t, "mail_adapter", record.TargetType)
		require.Equal(t, createdID, record.TargetID)
		require.Empty(t, record.ChangedFields)
		encoded, err := json.Marshal(record)
		require.NoError(t, err)
		for _, secret := range []string{"created-mail-secret", "created-mail@example.test", "created-mail-key"} {
			require.NotContains(t, string(encoded), secret)
		}
	}
	require.Equal(t, 2, f.cache.invalidated())
}

func TestMailAdapterAuditFailureRollsBackTheMutation(t *testing.T) {
	f := newFixtureWith(t, fixtureOptions{audit: &audittest.Recorder{Err: errAuditDown}})
	adapter := f.store.SeedMailAdapter(stored(emaildelivery.AdapterTypeSES, sesConfig))
	name := "must not persist"

	_, err := f.delivery.CreateMailAdapter(f.admin.ctx, emaildelivery.CreateMailAdapterCommand{
		Name: "failed audit mail create", Type: emaildelivery.AdapterTypeSES,
		SES: &emaildelivery.SESConfigInput{Region: "ap-northeast-2", AccessKeyID: "key", SecretAccessKey: "secret", FromEmail: "failed@example.test"},
	})
	require.ErrorIs(t, err, errAuditDown)
	_, err = f.delivery.UpdateMailAdapter(f.admin.ctx, emaildelivery.UpdateMailAdapterCommand{ID: adapter.ID, Name: &name})
	require.ErrorIs(t, err, errAuditDown)
	err = f.delivery.DeleteMailAdapter(f.admin.ctx, emaildelivery.DeleteMailAdapterCommand{ID: adapter.ID})
	require.ErrorIs(t, err, errAuditDown)

	require.Equal(t, 1, f.store.MailAdapterCount(), "the create rolled back")
	f.requireUnchanged(t, adapter)
	steps := f.units.Steps()
	require.Equal(t, 3, countSteps(steps, mutation.StepRollback))
	require.Zero(t, countSteps(steps, mutation.StepCommit))
}

func countSteps(steps []mutation.Step, step mutation.Step) int {
	count := 0
	for _, recorded := range steps {
		if recorded == step {
			count++
		}
	}
	return count
}

func TestMailAdapterChangePublishFailureKeepsTheCommittedChange(t *testing.T) {
	f := newFixture(t)
	f.changes.err = errors.New("queue unavailable")
	adapter := f.seedSMTP("")
	active := true

	updated, err := f.delivery.UpdateMailAdapter(f.admin.ctx, emaildelivery.UpdateMailAdapterCommand{ID: adapter.ID, IsActive: &active})

	require.NoError(t, err)
	require.True(t, updated.IsActive)
	require.True(t, f.storedAdapter(t, adapter.ID).IsActive)
	require.Equal(t, 1, f.cache.invalidated(), "the local cache is invalidated before the publish")
}

func TestUpdateMailAdapterSenderFailureKeepsTheStoredSecretsPrivate(t *testing.T) {
	f := newFixture(t)
	adapter := f.seedSMTP("")
	writeFailure := errors.New("write failed for smtp-user with smtp-secret")
	f.store.Fail = map[string]error{"UpdateMailAdapter": writeFailure}
	fromEmail := "changed@example.test"

	_, err := f.delivery.UpdateMailAdapterSender(f.admin.ctx, emaildelivery.MailAdapterSenderUpdate{ID: adapter.ID, FromEmail: &fromEmail})

	require.ErrorIs(t, err, writeFailure)
	require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
	cause := failurecause.Message(err)
	require.Contains(t, cause, "mail adapter sender update failed: write failed")
	require.NotContains(t, cause, "smtp-user")
	require.NotContains(t, cause, "smtp-secret")
	f.requireUnchanged(t, adapter)
}

func TestListMailAdaptersPagesInPriorityOrder(t *testing.T) {
	f := newFixture(t)
	base := time.Now().UTC().Truncate(time.Microsecond)
	for i, name := range []string{"Second", "First", "Third"} {
		adapter := stored(emaildelivery.AdapterTypeSES, sesConfig)
		adapter.Name, adapter.Priority, adapter.IsActive, adapter.CreatedAt = name, map[string]int{"First": 1, "Second": 2, "Third": 2}[name], name != "Second", base.Add(time.Duration(i)*time.Second)
		f.store.SeedMailAdapter(adapter)
	}
	handler := emaildelivery.NewMailAdapterConnectHandler(f.delivery)

	all, err := handler.ListMailAdaptersAdmin(f.admin.ctx, connect.NewRequest(&managev1.ListMailAdaptersAdminRequest{}))
	require.NoError(t, err)
	require.Equal(t, []string{"First", "Second", "Third"}, adapterNames(all.Msg.Adapters))
	require.EqualValues(t, 3, all.Msg.Pagination.Total)
	require.EqualValues(t, 20, all.Msg.Pagination.Limit)
	require.False(t, all.Msg.Pagination.HasMore)
	for _, adapter := range all.Msg.Adapters {
		require.Empty(t, adapter.GetSesConfig().GetSecretAccessKey())
		require.Equal(t, "ses-key", adapter.GetSesConfig().GetAccessKeyId())
	}

	page, err := handler.ListMailAdaptersAdmin(f.admin.ctx, connect.NewRequest(&managev1.ListMailAdaptersAdminRequest{
		Pagination: &commonv1.PaginationRequest{Limit: 500, Offset: 1},
	}))
	require.NoError(t, err)
	require.Equal(t, []string{"Second", "Third"}, adapterNames(page.Msg.Adapters))
	require.EqualValues(t, 100, page.Msg.Pagination.Limit, "the limit is capped")

	active, err := handler.ListMailAdaptersAdmin(f.admin.ctx, connect.NewRequest(&managev1.ListMailAdaptersAdminRequest{
		Filters: []*commonv1.FilterSpec{nil, {Field: "is_active", Value: "false"}, {Field: "is_active", Value: "true"}},
	}))
	require.NoError(t, err)
	require.Equal(t, []string{"First", "Third"}, adapterNames(active.Msg.Adapters))
	require.EqualValues(t, 2, active.Msg.Pagination.Total)

	_, err = handler.ListMailAdaptersAdmin(f.outsider.ctx, connect.NewRequest(&managev1.ListMailAdaptersAdminRequest{}))
	requireAnswer(t, connect.CodePermissionDenied, "admin access required", err)
}

func adapterNames(adapters []*managev1.MailAdapter) []string {
	names := make([]string, len(adapters))
	for i, adapter := range adapters {
		names[i] = adapter.GetName()
	}
	return names
}

func TestTestMailAdapterSendsThroughTheStoredAdapterAndClosesIt(t *testing.T) {
	f := newFixture(t)
	adapter := f.seedSMTP("")

	result, err := f.delivery.TestMailAdapter(f.admin.ctx, emaildelivery.TestMailAdapterCommand{ID: adapter.ID, TestEmail: " admin@example.test "})

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "provider-message-admin@example.test", *result.MessageID)
	require.Equal(t, []string{"admin@example.test"}, f.providers.recipients())
	require.Equal(t, adapter.ID, f.providers.opened[0].ID)
	f.providers.requireAllClosed(t)

	f.providers.sendErr = errors.New("mailbox unavailable")
	result, err = f.delivery.TestMailAdapter(f.admin.ctx, emaildelivery.TestMailAdapterCommand{ID: adapter.ID, TestEmail: "admin@example.test"})
	require.NoError(t, err, "a provider failure is the result")
	require.False(t, result.Success)
	require.Equal(t, "mailbox unavailable", *result.Error)
	f.providers.requireAllClosed(t)

	missing := uuid.NewString()
	_, err = f.delivery.TestMailAdapter(f.admin.ctx, emaildelivery.TestMailAdapterCommand{ID: missing, TestEmail: "admin@example.test"})
	requireAnswer(t, connect.CodeNotFound, "mail adapter not found: "+missing, err)
	_, err = f.delivery.TestMailAdapter(f.outsider.ctx, emaildelivery.TestMailAdapterCommand{ID: adapter.ID, TestEmail: "admin@example.test"})
	requireAnswer(t, connect.CodePermissionDenied, "admin access required", err)
}

func TestTestMailAdapterConfigReportsAProviderThatCannotOpen(t *testing.T) {
	f := newFixture(t)
	f.providers.openErr = errors.New("invalid SES config: bad region")

	result, err := f.delivery.TestMailAdapterConfig(f.admin.ctx, emaildelivery.TestMailAdapterConfigCommand{
		Type: emaildelivery.AdapterTypeSES, TestEmail: "admin@example.test",
		SES: &emaildelivery.SESConfigInput{Region: "r", AccessKeyID: "k", SecretAccessKey: "s", FromEmail: "sender@example.test"},
	})

	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, "invalid SES config: bad region", *result.Error)

	_, err = f.delivery.TestMailAdapterConfig(f.admin.ctx, emaildelivery.TestMailAdapterConfigCommand{Type: "FUTURE", TestEmail: "admin@example.test"})
	requireAnswer(t, connect.CodeInvalidArgument, "type: invalid adapter type", err)
	_, err = f.delivery.TestMailAdapterConfig(f.admin.ctx, emaildelivery.TestMailAdapterConfigCommand{Type: emaildelivery.AdapterTypeSES, TestEmail: "not-an-address"})
	requireAnswer(t, connect.CodeInvalidArgument, "test_email: invalid email address: not-an-address", err)
}
