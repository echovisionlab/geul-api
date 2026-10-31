//go:build integration

package emaildelivery

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// These cases pin every branch of MailAdapterService.update, through the admin
// Update RPC and through UpdateSender, before it is decomposed: the response,
// the stored row, provider verification sends, the Domain Audit record and the
// cache invalidation event.
func TestMailAdapterUpdateCharacterizationIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	spiceDB := testutil.IntegrationSpiceDB(t)
	adminIdentityID := testutil.IntegrationUUID()
	adminMemberID := testutil.SeedPostIntegrationIdentity(t, db, adminIdentityID, "Mail adapter admin "+adminIdentityID[:8])
	testutil.GrantIntegrationGlobalRole(t, spiceDB, adminIdentityID, policyv1.Role.Admin())
	adminCtx := testutil.NewAuditContext(t, adminIdentityID, adminMemberID)
	outsiderIdentityID := testutil.IntegrationUUID()
	outsiderMemberID := testutil.SeedPostIntegrationIdentity(t, db, outsiderIdentityID, "Mail adapter outsider "+outsiderIdentityID[:8])
	outsiderCtx := testutil.NewAuditContext(t, outsiderIdentityID, outsiderMemberID)

	newService := func() (*MailAdapterService, *recordingMailAdapterPublisher) {
		publisher := &recordingMailAdapterPublisher{}
		return NewAuditedMailAdapterService(db, nil, publisher, apitelemetry.NewDurableWriter(db), spiceDB), publisher
	}

	t.Run("metadata change skips verification", func(t *testing.T) {
		server := newFakeSMTPServer(t, false)
		adapter := seedSMTPMailAdapter(t, db, server, `{}`)
		service, publisher := newService()
		name, active, priority := "  Renamed adapter  ", true, int32(7)
		response, err := service.Update(adminCtx, connect.NewRequest(&managev1.UpdateMailAdapterRequest{
			Id: adapter.ID, Name: &name, IsActive: &active, Priority: &priority,
		}))
		require.NoError(t, err)
		stored := loadMailAdapter(t, db, adapter.ID)
		require.Equal(t, "Renamed adapter", stored.Name)
		require.True(t, stored.IsActive)
		require.Equal(t, 7, stored.Priority)
		require.JSONEq(t, string(adapter.Config.RawMessage), string(stored.Config.RawMessage))
		require.NotNil(t, stored.UpdatedAt)
		require.True(t, stored.UpdatedAt.After(*adapter.UpdatedAt))
		requireMailAdapterResponse(t, response.Msg.Adapter, stored, "smtp-sender@example.test")
		require.Empty(t, server.recipients())
		requireMailAdapterAudits(t, db, adapter.ID, []string{"active", "name", "priority"})
		publisher.requireUpdated(t, adapter.ID)
	})

	t.Run("semantic no-op writes nothing", func(t *testing.T) {
		server := newFakeSMTPServer(t, false)
		adapter := seedSMTPMailAdapter(t, db, server, `{}`)
		service, publisher := newService()
		name, active, priority := adapter.Name, adapter.IsActive, int32(adapter.Priority)
		response, err := service.Update(adminCtx, connect.NewRequest(&managev1.UpdateMailAdapterRequest{
			Id: adapter.ID, Name: &name, IsActive: &active, Priority: &priority,
		}))
		require.NoError(t, err)
		stored := loadMailAdapter(t, db, adapter.ID)
		require.Equal(t, adapter.UpdatedAt.UTC(), stored.UpdatedAt.UTC())
		requireMailAdapterResponse(t, response.Msg.Adapter, stored, "smtp-sender@example.test")
		require.Empty(t, server.recipients())
		requireMailAdapterAudits(t, db, adapter.ID, nil)
		publisher.requireNone(t)
	})

	t.Run("config replacement verifies and drops omitted secrets", func(t *testing.T) {
		server := newFakeSMTPServer(t, false)
		adapter := seedSMTPMailAdapter(t, db, server, `{"provider_hint":"kept-only-by-sender-updates"}`)
		service, publisher := newService()
		response, err := service.Update(adminCtx, connect.NewRequest(&managev1.UpdateMailAdapterRequest{
			Id: adapter.ID,
			Config: &managev1.UpdateMailAdapterRequest_SmtpConfig{SmtpConfig: &managev1.SMTPConfig{
				Host: "127.0.0.1", Port: server.port(), User: "smtp-user",
				FromEmail: "replaced-sender@example.test",
			}},
		}))
		require.NoError(t, err)
		require.Equal(t, []string{"replaced-sender@example.test"}, server.recipients())
		server.requireClientsDisconnected(t)
		stored := loadMailAdapter(t, db, adapter.ID)
		// The admin RPC replaces the whole provider config: an omitted
		// password and unknown stored keys are not carried over.
		require.JSONEq(t, `{
			"host":"127.0.0.1","port":`+strconv.Itoa(int(server.port()))+`,"secure":false,
			"user":"smtp-user","password":"","from_email":"replaced-sender@example.test"
		}`, string(stored.Config.RawMessage))
		requireMailAdapterResponse(t, response.Msg.Adapter, stored, "replaced-sender@example.test")
		requireMailAdapterAudits(t, db, adapter.ID, []string{"config"})
		publisher.requireUpdated(t, adapter.ID)
	})

	t.Run("resending the same config still verifies", func(t *testing.T) {
		server := newFakeSMTPServer(t, false)
		adapter := seedSMTPMailAdapter(t, db, server, `{}`)
		service, publisher := newService()
		fromName := "SMTP Sender"
		_, err := service.Update(adminCtx, connect.NewRequest(&managev1.UpdateMailAdapterRequest{
			Id: adapter.ID,
			Config: &managev1.UpdateMailAdapterRequest_SmtpConfig{SmtpConfig: &managev1.SMTPConfig{
				Host: "127.0.0.1", Port: server.port(), User: "smtp-user", Password: "smtp-secret",
				FromEmail: "smtp-sender@example.test", FromName: &fromName,
			}},
		}))
		require.NoError(t, err)
		require.Equal(t, []string{"smtp-sender@example.test"}, server.recipients())
		stored := loadMailAdapter(t, db, adapter.ID)
		require.Equal(t, adapter.UpdatedAt.UTC(), stored.UpdatedAt.UTC())
		requireMailAdapterAudits(t, db, adapter.ID, nil)
		publisher.requireNone(t)
	})

	t.Run("provider switch verifies the new provider", func(t *testing.T) {
		server := newFakeSMTPServer(t, false)
		adapter := seedSESMailAdapter(t, db)
		service, publisher := newService()
		smtpType := managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP
		response, err := service.Update(adminCtx, connect.NewRequest(&managev1.UpdateMailAdapterRequest{
			Id: adapter.ID, Type: &smtpType,
			Config: &managev1.UpdateMailAdapterRequest_SmtpConfig{SmtpConfig: &managev1.SMTPConfig{
				Host: "127.0.0.1", Port: server.port(), FromEmail: "switched@example.test",
			}},
		}))
		require.NoError(t, err)
		require.Equal(t, []string{"switched@example.test"}, server.recipients())
		server.requireClientsDisconnected(t)
		stored := loadMailAdapter(t, db, adapter.ID)
		require.Equal(t, model.MailAdapterType("MAIL_ADAPTER_TYPE_SMTP"), stored.Type)
		require.JSONEq(t, `{
			"host":"127.0.0.1","port":`+strconv.Itoa(int(server.port()))+`,"secure":false,
			"user":"","password":"","from_email":"switched@example.test"
		}`, string(stored.Config.RawMessage))
		requireMailAdapterResponse(t, response.Msg.Adapter, stored, "switched@example.test")
		requireMailAdapterAudits(t, db, adapter.ID, []string{"config", "type"})
		publisher.requireUpdated(t, adapter.ID)
	})

	t.Run("sender update keeps credentials and unknown fields", func(t *testing.T) {
		server := newFakeSMTPServer(t, false)
		adapter := seedSMTPMailAdapter(t, db, server, `{"provider_hint":"kept"}`)
		service, publisher := newService()
		fromName, fromEmail := "Renamed Sender", "renamed-sender@example.test"
		response, err := service.UpdateSender(adminCtx, MailAdapterSenderUpdate{
			ID: adapter.ID, FromName: &fromName, FromEmail: &fromEmail,
		})
		require.NoError(t, err)
		require.Equal(t, []string{"renamed-sender@example.test"}, server.recipients())
		server.requireClientsDisconnected(t)
		stored := loadMailAdapter(t, db, adapter.ID)
		require.JSONEq(t, `{
			"host":"127.0.0.1","port":`+strconv.Itoa(int(server.port()))+`,"secure":false,
			"user":"smtp-user","password":"smtp-secret","from_email":"renamed-sender@example.test",
			"from_name":"Renamed Sender","provider_hint":"kept"
		}`, string(stored.Config.RawMessage))
		requireMailAdapterResponse(t, response.Msg.Adapter, stored, "renamed-sender@example.test")
		requireMailAdapterAudits(t, db, adapter.ID, []string{"config"})
		publisher.requireUpdated(t, adapter.ID)
	})

	t.Run("unchanged sender skips verification", func(t *testing.T) {
		server := newFakeSMTPServer(t, false)
		adapter := seedSMTPMailAdapter(t, db, server, `{}`)
		service, publisher := newService()
		fromName := "SMTP Sender"
		_, err := service.UpdateSender(adminCtx, MailAdapterSenderUpdate{ID: adapter.ID, FromName: &fromName})
		require.NoError(t, err)
		require.Empty(t, server.recipients())
		require.Equal(t, adapter.UpdatedAt.UTC(), loadMailAdapter(t, db, adapter.ID).UpdatedAt.UTC())
		requireMailAdapterAudits(t, db, adapter.ID, nil)
		publisher.requireNone(t)
	})

	t.Run("rejections", func(t *testing.T) {
		server := newFakeSMTPServer(t, false)
		rejecting := newFakeSMTPServer(t, true)
		missingID := testutil.IntegrationUUID()
		emptyName, longName := "   ", strings.Repeat("n", 256)
		invalidType := managev1.MailAdapterType(99)
		loggingType := managev1.MailAdapterType_MAIL_ADAPTER_TYPE_LOGGING
		sesType := managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES
		badSender := "not-an-address"
		cases := []struct {
			name       string
			ctx        context.Context
			server     *fakeSMTPServer
			update     func(id string) *managev1.UpdateMailAdapterRequest
			sender     func(id string) *MailAdapterSenderUpdate
			missing    bool
			code       connect.Code
			message    string
			prefix     bool
			recipients []string
		}{
			{
				name: "missing adapter", ctx: adminCtx, server: server, missing: true,
				update: func(string) *managev1.UpdateMailAdapterRequest {
					return &managev1.UpdateMailAdapterRequest{Id: missingID, Name: &emptyName}
				},
				code: connect.CodeNotFound, message: "mail adapter not found: " + missingID,
			},
			{
				name: "missing adapter is hidden from a non administrator", ctx: outsiderCtx, server: server, missing: true,
				update: func(string) *managev1.UpdateMailAdapterRequest {
					return &managev1.UpdateMailAdapterRequest{Id: missingID}
				},
				code: connect.CodePermissionDenied, message: "admin access required",
			},
			{
				name: "non administrator", ctx: outsiderCtx, server: server,
				update: func(id string) *managev1.UpdateMailAdapterRequest {
					return &managev1.UpdateMailAdapterRequest{Id: id, Name: &longName}
				},
				code: connect.CodePermissionDenied, message: "admin access required",
			},
			{
				name: "blank name", ctx: adminCtx, server: server,
				update: func(id string) *managev1.UpdateMailAdapterRequest {
					return &managev1.UpdateMailAdapterRequest{Id: id, Name: &emptyName}
				},
				code: connect.CodeInvalidArgument, message: "name: cannot be empty",
			},
			{
				name: "long name", ctx: adminCtx, server: server,
				update: func(id string) *managev1.UpdateMailAdapterRequest {
					return &managev1.UpdateMailAdapterRequest{Id: id, Name: &longName}
				},
				code: connect.CodeInvalidArgument, message: "name: must be at most 255 characters",
			},
			{
				name: "unknown type", ctx: adminCtx, server: server,
				update: func(id string) *managev1.UpdateMailAdapterRequest {
					return &managev1.UpdateMailAdapterRequest{Id: id, Type: &invalidType}
				},
				code: connect.CodeInvalidArgument, message: "type: invalid adapter type",
			},
			{
				name: "logging type", ctx: adminCtx, server: server,
				update: func(id string) *managev1.UpdateMailAdapterRequest {
					return &managev1.UpdateMailAdapterRequest{Id: id, Type: &loggingType}
				},
				code: connect.CodeInvalidArgument, message: "logging adapter is non-delivery and cannot be configured",
			},
			{
				name: "type switch without config", ctx: adminCtx, server: server,
				update: func(id string) *managev1.UpdateMailAdapterRequest {
					return &managev1.UpdateMailAdapterRequest{Id: id, Type: &sesType}
				},
				code: connect.CodeInvalidArgument, message: "SES config is required for SES adapter",
			},
			{
				name: "invalid smtp sender", ctx: adminCtx, server: server,
				update: func(id string) *managev1.UpdateMailAdapterRequest {
					return &managev1.UpdateMailAdapterRequest{Id: id, Config: &managev1.UpdateMailAdapterRequest_SmtpConfig{
						SmtpConfig: &managev1.SMTPConfig{Host: "127.0.0.1", Port: 25, FromEmail: badSender},
					}}
				},
				code: connect.CodeInvalidArgument, message: "invalid SMTP from email: not-an-address",
			},
			{
				name: "provider rejects verification", ctx: adminCtx, server: rejecting,
				update: func(id string) *managev1.UpdateMailAdapterRequest {
					return &managev1.UpdateMailAdapterRequest{Id: id, Config: &managev1.UpdateMailAdapterRequest_SmtpConfig{
						SmtpConfig: &managev1.SMTPConfig{Host: "127.0.0.1", Port: rejecting.port(), FromEmail: "rejected@example.test"},
					}}
				},
				code: connect.CodeInvalidArgument, message: "config: verification send failed: ", prefix: true,
				recipients: []string{},
			},
			{
				name: "sender with invalid address", ctx: adminCtx, server: server,
				sender: func(id string) *MailAdapterSenderUpdate {
					return &MailAdapterSenderUpdate{ID: id, FromEmail: &badSender}
				},
				code: connect.CodeInvalidArgument, message: "invalid SMTP from email: not-an-address",
			},
			{
				name: "sender verification failure hides provider detail", ctx: adminCtx, server: rejecting,
				sender: func(id string) *MailAdapterSenderUpdate {
					fromEmail := "rejected-sender@example.test"
					return &MailAdapterSenderUpdate{ID: id, FromEmail: &fromEmail}
				},
				code:    connect.CodeInvalidArgument,
				message: "config: sender configuration verification failed; check provider configuration and sender verification",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				adapter := seedSMTPMailAdapter(t, db, tc.server, `{}`)
				service, publisher := newService()
				var err error
				if tc.sender != nil {
					_, err = service.UpdateSender(tc.ctx, *tc.sender(adapter.ID))
				} else {
					_, err = service.Update(tc.ctx, connect.NewRequest(tc.update(adapter.ID)))
				}
				require.Error(t, err)
				require.Equal(t, tc.code, connect.CodeOf(err))
				var connectErr *connect.Error
				require.ErrorAs(t, err, &connectErr)
				if tc.prefix {
					require.True(t, strings.HasPrefix(connectErr.Message(), tc.message), connectErr.Message())
				} else {
					require.Equal(t, tc.message, connectErr.Message())
				}
				stored := loadMailAdapter(t, db, adapter.ID)
				require.Equal(t, adapter.Name, stored.Name)
				require.Equal(t, adapter.Type, stored.Type)
				require.JSONEq(t, string(adapter.Config.RawMessage), string(stored.Config.RawMessage))
				require.Equal(t, adapter.UpdatedAt.UTC(), stored.UpdatedAt.UTC())
				requireMailAdapterAudits(t, db, adapter.ID, nil)
				publisher.requireNone(t)
			})
		}
		require.Empty(t, server.recipients())
		require.Empty(t, rejecting.recipients(), "a rejected MAIL FROM never reaches RCPT TO")
	})

	t.Run("delete authorizes before loading", func(t *testing.T) {
		server := newFakeSMTPServer(t, false)
		adapter := seedSMTPMailAdapter(t, db, server, `{}`)
		service, publisher := newService()
		for _, id := range []string{adapter.ID, testutil.IntegrationUUID()} {
			_, err := service.Delete(outsiderCtx, connect.NewRequest(&managev1.DeleteMailAdapterRequest{Id: id}))
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "error: %v", err)
		}
		loadMailAdapter(t, db, adapter.ID)
		publisher.requireNone(t)
	})
}

func seedSMTPMailAdapter(t *testing.T, db *gorm.DB, server *fakeSMTPServer, extra string) model.MailAdapter {
	t.Helper()
	var fields map[string]any
	require.NoError(t, json.Unmarshal([]byte(extra), &fields))
	fields["host"], fields["port"], fields["secure"] = "127.0.0.1", server.port(), false
	fields["user"], fields["password"] = "smtp-user", "smtp-secret"
	fields["from_email"], fields["from_name"] = "smtp-sender@example.test", "SMTP Sender"
	config, err := json.Marshal(fields)
	require.NoError(t, err)
	return seedMailAdapterRow(t, db, "MAIL_ADAPTER_TYPE_SMTP", config)
}

func seedSESMailAdapter(t *testing.T, db *gorm.DB) model.MailAdapter {
	t.Helper()
	return seedMailAdapterRow(t, db, "MAIL_ADAPTER_TYPE_SES", []byte(`{
		"region":"ap-northeast-2","access_key_id":"ses-key","secret_access_key":"ses-secret",
		"from_email":"ses-sender@example.test"
	}`))
}

func seedMailAdapterRow(t *testing.T, db *gorm.DB, adapterType string, config []byte) model.MailAdapter {
	t.Helper()
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	adapter := model.MailAdapter{
		Name: "Characterized " + testutil.IntegrationUUID()[:8], Type: model.MailAdapterType(adapterType),
		Priority: 3, Config: model.MailAdapterConfig{RawMessage: config}, CreatedAt: now, UpdatedAt: &now,
	}
	require.NoError(t, db.Create(&adapter).Error)
	return loadMailAdapter(t, db, adapter.ID)
}

func loadMailAdapter(t *testing.T, db *gorm.DB, id string) model.MailAdapter {
	t.Helper()
	var adapter model.MailAdapter
	require.NoError(t, db.First(&adapter, "id = ?", id).Error)
	return adapter
}

// requireMailAdapterResponse pins the masked response projection: provider
// secrets never leave the service.
func requireMailAdapterResponse(t *testing.T, response *managev1.MailAdapter, stored model.MailAdapter, fromEmail string) {
	t.Helper()
	require.Equal(t, stored.ID, response.GetId())
	require.Equal(t, stored.Name, response.GetName())
	require.Equal(t, stored.IsActive, response.GetIsActive())
	require.EqualValues(t, stored.Priority, response.GetPriority())
	// Timestamps are RFC 3339 seconds; a changed adapter reports updated_at in
	// UTC while values read back from the row keep the process time zone.
	requireSameRFC3339Instant(t, stored.CreatedAt, response.GetCreatedAt())
	requireSameRFC3339Instant(t, *stored.UpdatedAt, response.GetUpdatedAt())
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

func requireSameRFC3339Instant(t *testing.T, want time.Time, got string) {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, got)
	require.NoError(t, err)
	require.True(t, want.Truncate(time.Second).Equal(parsed), "want %s, got %s", want, got)
}

func requireMailAdapterAudits(t *testing.T, db *gorm.DB, adapterID string, changedFields []string) {
	t.Helper()
	var rows []mailAdapterAuditRow
	require.NoError(t, db.Raw(`
		SELECT action, target_type, target_id, attributes
		FROM public.domain_audit
		WHERE target_type = 'mail_adapter' AND target_id = ?
		ORDER BY occurred_at, audit_id
	`, adapterID).Scan(&rows).Error)
	if changedFields == nil {
		require.Empty(t, rows)
		return
	}
	require.Len(t, rows, 1)
	assertMailAdapterAudit(t, rows[0], sharedtelemetry.AuditMailAdapterUpdated, adapterID, changedFields)
}

type recordingMailAdapterPublisher struct {
	mu     sync.Mutex
	events []*managev1.MailAdapterChangedEvent
}

func (p *recordingMailAdapterPublisher) PublishMailAdapterChanged(_ context.Context, event *managev1.MailAdapterChangedEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, proto.Clone(event).(*managev1.MailAdapterChangedEvent))
	return nil
}

func (p *recordingMailAdapterPublisher) requireNone(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	require.Empty(t, p.events)
}

func (p *recordingMailAdapterPublisher) requireUpdated(t *testing.T, adapterID string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	require.Len(t, p.events, 1)
	require.Equal(t, adapterID, p.events[0].GetAdapterId())
	require.Equal(t, managev1.MailAdapterChangeType_MAIL_ADAPTER_CHANGE_TYPE_UPDATED, p.events[0].GetChangeType())
	require.NotZero(t, p.events[0].GetTimestampMs())
}

// fakeSMTPServer speaks just enough plaintext SMTP for provider verification
// and records each accepted recipient. A rejecting server refuses MAIL FROM
// permanently.
type fakeSMTPServer struct {
	listener net.Listener
	reject   bool
	mu       sync.Mutex
	rcpts    []string
	conns    []net.Conn
	open     int
	wg       sync.WaitGroup
}

func newFakeSMTPServer(t *testing.T, reject bool) *fakeSMTPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &fakeSMTPServer{listener: listener, reject: reject}
	server.wg.Add(1)
	go server.serve()
	t.Cleanup(func() {
		_ = listener.Close()
		server.mu.Lock()
		for _, conn := range server.conns {
			_ = conn.Close()
		}
		server.mu.Unlock()
		server.wg.Wait()
	})
	return server
}

func (s *fakeSMTPServer) port() int32 {
	return int32(s.listener.Addr().(*net.TCPAddr).Port)
}

func (s *fakeSMTPServer) recipients() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.rcpts...)
}

// requireClientsDisconnected waits for every client connection to end. A
// verification adapter that is never closed keeps its pooled connection open.
func (s *fakeSMTPServer) requireClientsDisconnected(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.open == 0
	}, 10*time.Second, 10*time.Millisecond, "a verification connection is still open")
}

func (s *fakeSMTPServer) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.open++
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
			s.mu.Lock()
			s.open--
			s.mu.Unlock()
		}()
	}
}

func (s *fakeSMTPServer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	reply := func(lines ...string) bool {
		for _, line := range lines {
			if _, err := conn.Write([]byte(line + "\r\n")); err != nil {
				return false
			}
		}
		return true
	}
	if !reply("220 fake.test ESMTP") {
		return
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		var ok bool
		switch {
		case strings.HasPrefix(command, "EHLO"):
			ok = reply("250-fake.test", "250 AUTH PLAIN")
		case strings.HasPrefix(command, "AUTH"):
			ok = reply("235 2.7.0 accepted")
		case strings.HasPrefix(command, "MAIL FROM"):
			if s.reject {
				ok = reply("550 5.7.1 sender rejected")
			} else {
				ok = reply("250 2.1.0 ok")
			}
		case strings.HasPrefix(command, "RCPT TO"):
			address := strings.Trim(strings.TrimSpace(line)[len("RCPT TO:"):], "<> ")
			s.mu.Lock()
			s.rcpts = append(s.rcpts, address)
			s.mu.Unlock()
			ok = reply("250 2.1.5 ok")
		case command == "DATA":
			if !reply("354 end with .") {
				return
			}
			for {
				body, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if body == ".\r\n" {
					break
				}
			}
			ok = reply("250 2.0.0 queued")
		case command == "QUIT":
			_ = reply("221 bye")
			return
		default:
			ok = reply("250 ok")
		}
		if !ok {
			return
		}
	}
}
