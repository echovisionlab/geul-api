package member_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/member/membertest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/structured"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

var errAuditUnavailable = errors.New("audit unavailable")

// fixture is one Member service over the fakes, and the principal its
// requests carry: an onboarded Member linked to an active Identity.
type fixture struct {
	store    *membertest.Store
	units    *mutationtest.UnitOfWork[member.Store]
	checker  *authztest.Checker
	spiceDB  *membertest.Authorizer
	fence    *identitystatetest.Fence
	audit    *audittest.Recorder
	accounts *fakeAccounts
	identity *fakeIdentity
	avatars  *fakeAvatarFiles
	service  *member.Service

	memberID   string
	identityID string
}

// newFixture serves the Member use cases to a principal the checker allows
// everything a test does not deny.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, units := membertest.New()
	f := &fixture{
		store: store, units: units, checker: authztest.AllowAll(), fence: &identitystatetest.Fence{},
		audit: &audittest.Recorder{}, accounts: &fakeAccounts{summaries: map[string]*member.AccountSummary{}},
		identity: &fakeIdentity{identities: map[string]*auth.Identity{}}, avatars: &fakeAvatarFiles{assets: map[string]*member.Asset{}},
		memberID: uuid.NewString(), identityID: uuid.NewString(),
	}
	f.spiceDB = membertest.NewAuthorizer(f.checker)
	store.SeedLinked(f.memberID, f.identityID, "principal")
	f.identity.put(&auth.Identity{ID: f.identityID, ExternalID: f.memberID, State: auth.KratosStateActive})
	f.service = f.newService(t, f.audit)
	return f
}

// newService serves the fixture's fakes with another audit appender.
func (f *fixture) newService(t *testing.T, audit member.AuditAppender) *member.Service {
	t.Helper()
	service, err := member.NewService(member.Deps{
		Units: f.units, SpiceDB: f.spiceDB, Fence: f.fence, Identity: f.identity,
		AvatarFiles: f.avatars, Accounts: f.accounts, Audit: audit, SiteOrigin: "https://www.example.test",
	})
	require.NoError(t, err)
	return service
}

// ctx is a member request of the principal, so mutations name their audit
// records.
func (f *fixture) ctx(t *testing.T) context.Context {
	t.Helper()
	return principalContext(t, f.identityID, f.memberID, true)
}

func principalContext(t *testing.T, identityID, memberID string, onboarded bool) context.Context {
	t.Helper()
	// A public request context carries the source IP personal-data access
	// records need.
	request, err := sharedtelemetry.NewPublicRequestContext("192.0.2.77")
	require.NoError(t, err)
	ctx := sharedtelemetry.WithActor(sharedtelemetry.WithRequestContext(t.Context(), request), sharedtelemetry.MemberActor{MemberID: memberID})
	return auth.WithUser(ctx, &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: onboarded,
	})
}

// seedMember stores another onboarded Member linked to an active Identity.
func (f *fixture) seedMember(nickname string) (memberID, identityID string) {
	memberID, identityID = uuid.NewString(), uuid.NewString()
	f.store.SeedLinked(memberID, identityID, nickname)
	f.identity.put(&auth.Identity{ID: identityID, ExternalID: memberID, State: auth.KratosStateActive})
	return memberID, identityID
}

// memberUpdates are the member.updated records appended so far.
func (f *fixture) memberUpdates() []sharedtelemetry.AuditRecord {
	var records []sharedtelemetry.AuditRecord
	for _, record := range f.audit.Records() {
		if record.Action == sharedtelemetry.AuditMemberUpdated {
			records = append(records, record)
		}
	}
	return records
}

func denyAdmin(f *fixture) {
	f.checker.Deny(policyv1.Platform.IsAdmin())
}

type fakeAccounts struct {
	mu        sync.Mutex
	summaries map[string]*member.AccountSummary
	err       error
}

func (a *fakeAccounts) summary(memberID string) (*member.AccountSummary, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return nil, a.err
	}
	if summary, found := a.summaries[memberID]; found {
		return summary, nil
	}
	return &member.AccountSummary{MemberID: memberID, Role: policyv1.AuthorizationRole_USER, Status: member.AdminStatusActive}, nil
}

func (a *fakeAccounts) SessionSummary(_ context.Context, memberID string) (*member.AccountSummary, error) {
	return a.summary(memberID)
}

func (a *fakeAccounts) Summary(_ context.Context, memberID string) (*member.AccountSummary, error) {
	return a.summary(memberID)
}

func (a *fakeAccounts) Summaries(_ context.Context, memberIDs []string) (map[string]*member.AccountSummary, error) {
	result := map[string]*member.AccountSummary{}
	for _, memberID := range memberIDs {
		summary, err := a.summary(memberID)
		if err != nil {
			return nil, err
		}
		result[memberID] = summary
	}
	return result, nil
}

func (a *fakeAccounts) AdminDetails(context.Context, *auth.Identity) (*member.AccountDetails, error) {
	return &member.AccountDetails{}, nil
}

type fakeIdentity struct {
	mu         sync.Mutex
	identities map[string]*auth.Identity
	externalID map[string]string
}

func (f *fakeIdentity) put(identity *auth.Identity) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identities[identity.ID] = identity
}

func (f *fakeIdentity) GetIdentity(_ context.Context, identityID string) (*auth.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	identity := f.identities[identityID]
	if identity == nil {
		return nil, nil
	}
	clone := *identity
	return &clone, nil
}

func (f *fakeIdentity) GetIdentityWithIncludeCredential(ctx context.Context, identityID, _ string) (*auth.Identity, error) {
	return f.GetIdentity(ctx, identityID)
}

func (f *fakeIdentity) UpdateIdentityExternalID(_ context.Context, identityID, externalID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.externalID == nil {
		f.externalID = map[string]string{}
	}
	f.externalID[identityID] = externalID
	if identity := f.identities[identityID]; identity != nil {
		identity.ExternalID = externalID
	}
	return nil
}

func (f *fakeIdentity) ListIdentities(context.Context, int, int) ([]*auth.Identity, int64, error) {
	return nil, 0, nil
}

func (f *fakeIdentity) UpdateIdentityTraits(context.Context, string, structured.Fields) error {
	return nil
}

type fakeAvatarFiles struct {
	mu     sync.Mutex
	assets map[string]*member.Asset
}

func (f *fakeAvatarFiles) PromoteUserAvatarAsset(_ context.Context, fileID string) (*member.Asset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	asset, found := f.assets[fileID]
	if !found {
		return nil, errors.New("avatar source was not promoted")
	}
	return asset, nil
}
