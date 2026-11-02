package artist_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/artist/artisttest"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/memberref"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// harness is one Artist use-case test: the service over the fake store, its
// unit of work and the fakes of every other port.
type harness struct {
	service *artist.Service
	store   *artisttest.Store
	units   *mutationtest.UnitOfWork[artist.Store]
	checker *artisttest.Authorizer
	fence   *identitystatetest.Fence
	audit   *audittest.Recorder
	members *members
}

type harnessOption func(*harness)

func withAudit(recorder *audittest.Recorder) harnessOption {
	return func(h *harness) { h.audit = recorder }
}

func newHarness(t *testing.T, options ...harnessOption) *harness {
	t.Helper()
	store, units := artisttest.New()
	h := &harness{
		store: store, units: units, checker: &artisttest.Authorizer{Checker: authztest.AllowAll()},
		fence: &identitystatetest.Fence{}, audit: &audittest.Recorder{}, members: newMembers(),
	}
	for _, option := range options {
		option(h)
	}
	service, err := artist.NewService(artist.Deps{Units: units, SpiceDB: h.checker, Fence: h.fence, Audit: h.audit, Members: h.members})
	require.NoError(t, err)
	h.service = service
	return h
}

// principal is a signed-in Member.
type principal struct {
	identityID string
	memberID   string
}

func newPrincipal() principal {
	return principal{identityID: uuid.NewString(), memberID: uuid.NewString()}
}

func (p principal) user() *auth.UserInfo {
	return &auth.UserInfo{
		IdentityID: auth.IdentityID(p.identityID), MemberID: auth.MemberID(p.memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	}
}

// context carries p and the request correlation a request audit needs.
func (p principal) context(t *testing.T) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.80")
	require.NoError(t, err)
	ctx := sharedtelemetry.WithRequestContext(t.Context(), requestContext)
	ctx = sharedtelemetry.WithActor(ctx, sharedtelemetry.MemberActor{IdentityID: p.identityID, MemberID: p.memberID, SessionID: uuid.NewString()})
	return auth.WithUser(ctx, p.user())
}

// seedArtist stores a draft Artist with its source title and content
// document, and returns it.
func (h *harness) seedArtist(t *testing.T, title string, change ...func(*artist.Artist)) artist.Artist {
	t.Helper()
	value := artist.Artist{}
	for _, apply := range change {
		apply(&value)
	}
	value = h.store.PutArtist(value)
	h.store.PutLocale(value.ID, artist.LocaleTitle{Locale: value.SourceLocale, Title: &title, UpdatedAt: value.CreatedAt})
	h.store.PutDocument(value.ID, artisttest.Document{})
	value, _ = h.store.Artist(value.ID)
	return value
}

// documentID is the content document of a stored Artist.
func (h *harness) documentID(t *testing.T, artistID string) uuid.UUID {
	t.Helper()
	value, ok := h.store.Artist(artistID)
	require.True(t, ok)
	require.NotNil(t, value.ContentDocumentID)
	return uuid.MustParse(*value.ContentDocumentID)
}

func (h *harness) revision(t *testing.T, artistID string) uuid.UUID {
	t.Helper()
	document, ok := h.store.Document(h.documentID(t, artistID))
	require.True(t, ok)
	return document.Revision
}

func requireCode(t *testing.T, err error, code connect.Code, message string) {
	t.Helper()
	require.Error(t, err)
	wrapped := errs.Wrap(err)
	require.Equal(t, code, wrapped.Code(), wrapped.Error())
	if message != "" {
		require.Equal(t, message, wrapped.Message())
	}
}

func lastStep(t *testing.T, units *mutationtest.UnitOfWork[artist.Store]) mutation.Step {
	t.Helper()
	steps := units.Steps()
	require.NotEmpty(t, steps)
	return steps[len(steps)-1]
}

func auditActions(recorder *audittest.Recorder) []sharedtelemetry.AuditAction {
	var actions []sharedtelemetry.AuditAction
	for _, record := range recorder.Records() {
		actions = append(actions, record.Action)
	}
	return actions
}

// members is the Member projection fake: every Member it knows is
// eligible and acts through its Identity.
type members struct {
	summaries  map[string]*memberref.Summary
	identities map[string]string
}

func newMembers() *members {
	return &members{summaries: map[string]*memberref.Summary{}, identities: map[string]string{}}
}

func (m *members) add(p principal) {
	m.summaries[p.memberID] = &memberref.Summary{ID: p.memberID}
	m.identities[p.memberID] = p.identityID
}

func (m *members) LoadMemberSummaries(_ context.Context, memberIDs []string) (map[string]*memberref.Summary, error) {
	summaries := map[string]*memberref.Summary{}
	for _, id := range memberIDs {
		if summary := m.summaries[id]; summary != nil {
			summaries[id] = summary
		}
	}
	return summaries, nil
}

func (m *members) LoadAuthorizationEligibleMemberSummary(_ context.Context, memberID string) (*memberref.Summary, error) {
	summary := m.summaries[memberID]
	if summary == nil {
		return nil, errors.New("member not eligible")
	}
	return summary, nil
}

func (m *members) LoadAuthorizationEligibleIdentities(_ context.Context, memberIDs []string) (map[string]string, error) {
	identities := map[string]string{}
	for _, id := range memberIDs {
		if identity, ok := m.identities[id]; ok {
			identities[id] = identity
		}
	}
	return identities, nil
}

// member adds p to the store and the projection, as the Member and
// Identity rows would.
func (h *harness) member(p principal) principal {
	h.store.PutMember(p.memberID, p.identityID)
	h.members.add(p)
	return p
}

func stringPointer(value string) *string { return &value }
