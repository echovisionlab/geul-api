package programevent_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/programevent/programeventtest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// harness is one Program Event use-case test: the service over the fake
// store, its unit of work and the fakes of every other port.
type harness struct {
	service *programevent.Service
	store   *programeventtest.Store
	units   *mutationtest.UnitOfWork[programevent.Store]
	checker *authztest.Checker
	audit   *audittest.Recorder
	members *creditMembers
}

type harnessOption func(*harness)

func withChecker(checker *authztest.Checker) harnessOption {
	return func(h *harness) { h.checker = checker }
}

func withAudit(recorder *audittest.Recorder) harnessOption {
	return func(h *harness) { h.audit = recorder }
}

func newHarness(t *testing.T, options ...harnessOption) *harness {
	t.Helper()
	store, units := programeventtest.New()
	h := &harness{
		store: store, units: units, checker: authztest.AllowAll(), audit: &audittest.Recorder{},
		members: &creditMembers{summaries: map[string]*commonv1.MemberSummary{}},
	}
	for _, option := range options {
		option(h)
	}
	service, err := programevent.NewService(programevent.Deps{
		Units: units, SpiceDB: h.checker, Fence: store.Fence, Audit: h.audit, CreditMembers: h.members,
	})
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

// systemContext is a collaboration save: a propagated request of the
// collaboration service without a principal.
func systemContext(t *testing.T) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(
		uuid.NewString(), sharedtelemetry.SystemActor{ServiceName: sharedtelemetry.ServiceEditorCollab},
	)
	require.NoError(t, err)
	return sharedtelemetry.WithRequestContext(t.Context(), requestContext)
}

// seedType stores an active type with an English name.
func (h *harness) seedType(t *testing.T, slug string) programevent.Type {
	t.Helper()
	return h.store.PutType(programevent.Type{Slug: slug, Status: programevent.TypeStatusActive, CreatedAt: at(0), UpdatedAt: at(0)},
		programevent.TypeLocale{Locale: "en", Name: "Type " + slug, CreatedAt: at(0), UpdatedAt: at(0)})
}

// seedEvent stores a draft online event in English with an empty content
// document and its source locale row.
func (h *harness) seedEvent(t *testing.T, change ...func(*programevent.Event)) programevent.Event {
	t.Helper()
	documentID := uuid.New()
	documentReference := documentID.String()
	event := programevent.Event{
		Title: "Event", Slug: "event-" + uuid.NewString(), Status: programevent.StatusDraft, SourceLocale: "en",
		TypeID: uuid.NewString(), StartsAt: at(60), Timezone: "UTC", LocationMode: programevent.LocationModeOnline,
		ContentDocumentID: &documentReference, CreatedAt: at(0), UpdatedAt: at(0),
	}
	for _, apply := range change {
		apply(&event)
	}
	event = h.store.PutEvent(event)
	h.store.PutLocale(event.ID, programevent.LocaleMetadata{Locale: event.SourceLocale, Summary: text("Source summary"), UpdatedAt: at(0)})
	h.store.PutDocument(contentblock.Snapshot{
		Document:     contentblock.Document{ID: documentID, Profile: "program_event", Revision: uuid.New()},
		SourceLocale: event.SourceLocale,
	})
	return event
}

// revision is the current revision of the event's content document.
func (h *harness) revision(t *testing.T, event programevent.Event) uuid.UUID {
	t.Helper()
	snapshot, ok := h.store.StoredDocument(uuid.MustParse(*event.ContentDocumentID))
	require.True(t, ok)
	return snapshot.Document.Revision
}

// creditMembers is the Member summary projection of credits.
type creditMembers struct {
	summaries map[string]*commonv1.MemberSummary
	err       error
}

func (m *creditMembers) LoadCreditMemberSummaries(_ context.Context, memberIDs []string) (map[string]*commonv1.MemberSummary, error) {
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

func requireCode(t *testing.T, err error, code connect.Code, message string) {
	t.Helper()
	require.Error(t, err)
	wrapped := errs.Wrap(err)
	require.Equal(t, code, wrapped.Code(), wrapped.Error())
	if message != "" {
		require.Equal(t, message, wrapped.Message())
	}
}

func lastStep(t *testing.T, units *mutationtest.UnitOfWork[programevent.Store]) mutation.Step {
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

func text(value string) *string { return &value }

func int32Ptr(value int32) *int32 { return &value }

func boolPtr(value bool) *bool { return &value }

// at is a fixed instant at the precision PostgreSQL stores.
func at(minutes int) time.Time {
	return time.Date(2026, time.October, 1, 10, 0, 0, 0, time.UTC).Add(time.Duration(minutes) * time.Minute)
}

func timePtr(value time.Time) *time.Time { return &value }

type programeventtestStore = programeventtest.Store

// replaceDocumentOnLock plays a writer that gives the event another content
// document between a save's read of it and the lock of its root.
func replaceDocumentOnLock(eventID, documentID string) func(*programeventtest.Store) {
	reads := 0
	return func(store *programeventtest.Store) {
		reads++
		if reads != 2 {
			return
		}
		event, _ := store.StoredEvent(eventID)
		event.ContentDocumentID = &documentID
		store.PutEvent(event)
	}
}
