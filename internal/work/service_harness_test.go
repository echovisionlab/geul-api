package work_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/work"
	"github.com/echovisionlab/geul-api/internal/work/worktest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// harness is one Work use-case test: the service over the fake store, its
// unit of work and the fakes of every other port.
type harness struct {
	service *work.Service
	store   *worktest.Store
	units   *mutationtest.UnitOfWork[work.Store]
	checker *authztest.Checker
	fence   *identitystatetest.Fence
	audit   *audittest.Recorder
	members *members
}

type harnessOption func(*harness)

func withChecker(checker *authztest.Checker) harnessOption {
	return func(h *harness) { h.checker = checker }
}

func newHarness(t *testing.T, options ...harnessOption) *harness {
	t.Helper()
	store, units := worktest.New()
	h := &harness{
		store: store, units: units, checker: authztest.AllowAll(),
		fence: &identitystatetest.Fence{}, audit: &audittest.Recorder{}, members: &members{summaries: map[string]*commonv1.MemberSummary{}},
	}
	for _, option := range options {
		option(h)
	}
	service, err := work.NewService(work.Deps{Units: units, SpiceDB: h.checker, Fence: h.fence, Audit: h.audit, Members: h.members})
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
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.81")
	require.NoError(t, err)
	ctx := sharedtelemetry.WithRequestContext(t.Context(), requestContext)
	ctx = sharedtelemetry.WithActor(ctx, sharedtelemetry.MemberActor{IdentityID: p.identityID, MemberID: p.memberID, SessionID: uuid.NewString()})
	return auth.WithUser(ctx, p.user())
}

// collabContext is editor-collab calling as itself, as its internal RPCs
// do.
func collabContext(t *testing.T) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(
		uuid.NewString(), sharedtelemetry.SystemActor{ServiceName: sharedtelemetry.ServiceEditorCollab},
	)
	require.NoError(t, err)
	return sharedtelemetry.WithRequestContext(t.Context(), requestContext)
}

// seedWork stores a present-tense draft Work with its source title and
// content document, and returns it.
func (h *harness) seedWork(t *testing.T, title string, change ...func(*work.Work)) work.Work {
	t.Helper()
	value := work.Work{Year: 2026, Month: 8, IsPresent: true}
	for _, apply := range change {
		apply(&value)
	}
	value = h.store.PutWork(value)
	h.store.PutLocale(value.ID, work.LocaleMetadata{Locale: value.SourceLocale, Title: &title})
	h.store.PutDocument(value.ID, worktest.Document{})
	value, _ = h.store.Stored(value.ID)
	return value
}

// documentID is the content document of a stored Work.
func (h *harness) documentID(t *testing.T, workID string) uuid.UUID {
	t.Helper()
	value, ok := h.store.Stored(workID)
	require.True(t, ok)
	require.NotNil(t, value.ContentDocumentID)
	return uuid.MustParse(*value.ContentDocumentID)
}

func (h *harness) revision(t *testing.T, workID string) uuid.UUID {
	t.Helper()
	document, ok := h.store.Document(h.documentID(t, workID))
	require.True(t, ok)
	return document.Revision
}

func (h *harness) stored(t *testing.T, workID string) work.Work {
	t.Helper()
	value, ok := h.store.Stored(workID)
	require.True(t, ok, "work %s is stored", workID)
	return value
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

func lastStep(t *testing.T, units *mutationtest.UnitOfWork[work.Store]) mutation.Step {
	t.Helper()
	steps := units.Steps()
	require.NotEmpty(t, steps)
	return steps[len(steps)-1]
}

// members is the Member summary fake.
type members struct {
	summaries map[string]*commonv1.MemberSummary
	err       error
}

func (m *members) LoadMemberSummaries(_ context.Context, memberIDs []string) (map[string]*commonv1.MemberSummary, error) {
	if m.err != nil {
		return nil, m.err
	}
	summaries := map[string]*commonv1.MemberSummary{}
	for _, id := range memberIDs {
		if summary := m.summaries[id]; summary != nil {
			summaries[id] = summary
		}
	}
	return summaries, nil
}

var errStoreDown = errors.New("store unavailable")

func stringPointer(value string) *string { return &value }

func int32Pointer(value int32) *int32 { return &value }

// emptyDocument is a Work document without Blocks in locale.
func emptyDocument(locale string) *contentv1.RichTextDocument {
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
		SourceLocale:            locale,
		Base:                    &contentv1.RichTextBlockGraph{},
		LocaleOverlays:          []*contentv1.RichTextLocaleOverlay{{Locale: locale}},
	}
}
