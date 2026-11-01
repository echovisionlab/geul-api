package emaildelivery_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/emaildelivery/emaildeliverytest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// fixture is the email delivery use cases over the in-memory store, with an
// administrator and an outsider the checker tells apart.
type fixture struct {
	store     *emaildeliverytest.Store
	units     *mutationtest.UnitOfWork[emaildelivery.Store]
	checker   *authztest.Checker
	fence     *identitystatetest.Fence
	audit     *audittest.Recorder
	providers *fakeProviders
	cache     *countingCache
	changes   *recordingChanges
	delivery  *emaildelivery.Service
	admin     principal
	outsider  principal
}

type fixtureOptions struct {
	audit *audittest.Recorder
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWith(t, fixtureOptions{})
}

func newFixtureWith(t *testing.T, options fixtureOptions) *fixture {
	t.Helper()
	store, units := emaildeliverytest.New()
	f := &fixture{
		store: store, units: units, checker: &authztest.Checker{}, fence: &identitystatetest.Fence{},
		audit: options.audit, providers: &fakeProviders{}, cache: &countingCache{}, changes: &recordingChanges{},
		admin: newPrincipal(t), outsider: newPrincipal(t),
	}
	if f.audit == nil {
		f.audit = &audittest.Recorder{}
	}
	f.allowAdmin()
	service, err := emaildelivery.NewService(emaildelivery.Deps{
		Units: units, SpiceDB: f.checker, Fence: f.fence, Audit: f.audit, Providers: f.providers,
		AdapterCache: f.cache, AdapterChanges: f.changes,
	})
	require.NoError(t, err)
	f.delivery = service
	return f
}

// allowAdmin grants the administrator every mail adapter and suppression
// action, and the platform administrator check.
func (f *fixture) allowAdmin() {
	admin := f.checker.For(f.admin.identityID)
	admin.Allow(policyv1.Platform.IsAdmin())
	admin.Allow(policyv1.MailAdapter.Create())
	admin.Allow(policyv1.MailAdapter.Update())
	admin.Allow(policyv1.MailAdapter.Delete())
	admin.Allow(policyv1.MailAdapter.List())
	admin.Allow(policyv1.MailAdapter.Test())
	admin.Allow(policyv1.EmailSuppression.Release())
}

// principal is an authenticated, onboarded Member with a request context,
// as the Connect and MCP gateways hand it to a use case.
type principal struct {
	identityID string
	memberID   string
	ctx        context.Context
}

func newPrincipal(t *testing.T) principal {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.77")
	require.NoError(t, err)
	identityID, memberID := uuid.NewString(), uuid.NewString()
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
	return principal{identityID: identityID, memberID: memberID, ctx: ctx}
}

func requireAnswer(t *testing.T, code connect.Code, message string, err error) {
	t.Helper()
	require.Error(t, err)
	wrapped := errs.Wrap(err)
	require.Equal(t, code, wrapped.Code(), err.Error())
	require.Equal(t, message, wrapped.Message())
}

// stored is a stored adapter an hour old, as the characterized rows were.
func stored(adapterType emaildelivery.AdapterType, config string) emaildelivery.MailAdapter {
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	return emaildelivery.MailAdapter{
		Name: "Characterized " + uuid.NewString()[:8], Type: adapterType, Priority: 3,
		Config: []byte(config), CreatedAt: at, UpdatedAt: &at,
	}
}

const (
	smtpConfig = `{"host":"127.0.0.1","port":2525,"secure":false,"user":"smtp-user","password":"smtp-secret","from_email":"smtp-sender@example.test","from_name":"SMTP Sender"}`
	sesConfig  = `{"region":"ap-northeast-2","access_key_id":"ses-key","secret_access_key":"ses-secret","from_email":"ses-sender@example.test"}`
)

func (f *fixture) seedSMTP(extra string) emaildelivery.MailAdapter {
	config := smtpConfig
	if extra != "" {
		config = config[:len(config)-1] + "," + extra + "}"
	}
	return f.store.SeedMailAdapter(stored(emaildelivery.AdapterTypeSMTP, config))
}

func (f *fixture) storedAdapter(t *testing.T, id string) emaildelivery.MailAdapter {
	t.Helper()
	adapter, ok := f.store.StoredMailAdapter(id)
	require.True(t, ok, "adapter %s is stored", id)
	return adapter
}

// requireUnchanged asserts that adapter is stored as it was, with no audit,
// cache invalidation or change event.
func (f *fixture) requireUnchanged(t *testing.T, adapter emaildelivery.MailAdapter) {
	t.Helper()
	got := f.storedAdapter(t, adapter.ID)
	require.Equal(t, adapter.Name, got.Name)
	require.Equal(t, adapter.Type, got.Type)
	require.Equal(t, adapter.IsActive, got.IsActive)
	require.Equal(t, adapter.Priority, got.Priority)
	require.JSONEq(t, string(adapter.Config), string(got.Config))
	require.Equal(t, *adapter.UpdatedAt, *got.UpdatedAt)
	require.Empty(t, f.audit.Records())
	f.changes.requireNone(t)
	require.Zero(t, f.cache.invalidated())
}

// requireAudit asserts the one record appended so far.
func (f *fixture) requireAudit(t *testing.T, action sharedtelemetry.AuditAction, targetID string, changedFields []string) {
	t.Helper()
	records := f.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, action, records[0].Action)
	require.Equal(t, targetID, records[0].TargetID)
	require.Equal(t, changedFields, records[0].ChangedFields)
	require.Equal(t, f.admin.memberID, records[0].MemberID)
}

// fakeProviders opens providers that record what they send. A provider
// fails every send with sendErr; Open fails with openErr.
type fakeProviders struct {
	mu      sync.Mutex
	sendErr error
	openErr error
	opened  []emaildelivery.MailAdapter
	sent    []string
	closed  int
}

func (p *fakeProviders) Open(adapter emaildelivery.MailAdapter) (emaildelivery.MailProvider, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.openErr != nil {
		return nil, p.openErr
	}
	p.opened = append(p.opened, adapter)
	return fakeProvider{providers: p}, nil
}

// recipients are the addresses every opened provider sent to.
func (p *fakeProviders) recipients() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.sent)
}

// requireAllClosed asserts that every opened provider was closed.
func (p *fakeProviders) requireAllClosed(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	require.Equal(t, len(p.opened), p.closed, "a provider opened for one send is still open")
}

type fakeProvider struct{ providers *fakeProviders }

func (p fakeProvider) Send(_ context.Context, message *email.Email) (*email.SendResult, error) {
	p.providers.mu.Lock()
	defer p.providers.mu.Unlock()
	if p.providers.sendErr != nil {
		return nil, p.providers.sendErr
	}
	p.providers.sent = append(p.providers.sent, message.To)
	return &email.SendResult{MessageID: "provider-message-" + message.To}, nil
}

func (p fakeProvider) Close() error {
	p.providers.mu.Lock()
	defer p.providers.mu.Unlock()
	p.providers.closed++
	return nil
}

type countingCache struct {
	mu    sync.Mutex
	count int
}

func (c *countingCache) InvalidateCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
}

func (c *countingCache) invalidated() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

type recordingChanges struct {
	mu      sync.Mutex
	err     error
	changes []emaildelivery.MailAdapterChange
}

func (r *recordingChanges) PublishMailAdapterChanged(_ context.Context, change emaildelivery.MailAdapterChange) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, change)
	return r.err
}

func (r *recordingChanges) requireNone(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.Empty(t, r.changes)
}

func (r *recordingChanges) requireOne(t *testing.T, adapterID string, changeType emaildelivery.MailAdapterChangeType) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.Len(t, r.changes, 1)
	require.Equal(t, adapterID, r.changes[0].AdapterID)
	require.Equal(t, changeType, r.changes[0].Type)
	require.False(t, r.changes[0].At.IsZero())
}

var errAuditDown = errors.New("audit unavailable")
