package programevent_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func joined(h *harness, recorder *audittest.Recorder) programevent.Joined {
	return programevent.Joined{Store: h.store, Audit: recorder}
}

func TestApplyTypedTranslationCandidateCreatesTheTargetLocaleAuditedToItsRequester(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t)
	requester := uuid.NewString()
	recorder := &audittest.Recorder{}
	candidate := &translation.Candidate{Summary: text("번역 요약"), ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"}}

	err := programevent.ApplyTypedTranslationCandidate(systemContext(t), joined(h, recorder), programevent.TranslationJob{
		EntityType: programevent.EntityType, EntityID: event.ID, TargetLocale: "ko", RequestedByMemberID: requester,
	}, candidate, translation.EntryWrite{Now: at(30)})

	require.NoError(t, err)
	locale, ok := h.store.StoredLocale(event.ID, "ko")
	require.True(t, ok)
	require.Equal(t, "번역 요약", *locale.Summary)
	records := recorder.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, records[0].ItemOperation)
	require.Equal(t, requester, records[0].MemberID)
	require.Equal(t, "ko", records[0].Locale)
	require.Empty(t, h.audit.Records(), "the joined appender records the delivery")

	err = programevent.ApplyTypedTranslationCandidate(systemContext(t), joined(h, recorder), programevent.TranslationJob{
		EntityType: "post", EntityID: event.ID, TargetLocale: "ko", RequestedByMemberID: requester,
	}, candidate, translation.EntryWrite{Now: at(30)})
	require.ErrorContains(t, err, `typed Program Event translation received entity type "post"`)
	err = programevent.ApplyTypedTranslationCandidate(systemContext(t), joined(h, recorder), programevent.TranslationJob{
		EntityType: programevent.EntityType, EntityID: event.ID, TargetLocale: "ja", RequestedByMemberID: requester,
	}, candidate, translation.EntryWrite{Now: at(30)})
	requireCode(t, err, connect.CodeFailedPrecondition, "translated Program Event Block overlay locale does not match the target locale")
}

func TestApplyTranslationInterchangeCandidateChecksTheObservedTargetRevision(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t)
	h.store.PutLocale(event.ID, programevent.LocaleMetadata{Locale: "ko", Summary: text("기존"), UpdatedAt: at(5)})
	stale := "stale"
	err := programevent.ApplyTranslationInterchangeCandidate(systemContext(t), joined(h, &audittest.Recorder{}), programevent.TranslationJob{
		EntityType: programevent.EntityType, EntityID: event.ID, TargetLocale: "ko", RequestedByMemberID: uuid.NewString(),
	}, &translation.Candidate{Summary: text("새"), ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"}},
		translation.EntryWrite{Now: at(30)}, &stale)
	var conflict *translation.TargetRevisionConflict
	require.ErrorAs(t, err, &conflict)
	locale, _ := h.store.StoredLocale(event.ID, "ko")
	require.Equal(t, "기존", *locale.Summary)
}

func TestApplyTypedTranslationCandidateOfTheSourceNeedsItsRequester(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t)
	recorder := &audittest.Recorder{}
	candidate := &translation.Candidate{Summary: text("Provider summary"), ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en"}}
	before := h.revision(t, event)

	err := programevent.ApplyTypedTranslationCandidate(systemContext(t), joined(h, recorder), programevent.TranslationJob{
		EntityType: programevent.EntityType, EntityID: event.ID, TargetLocale: "en", RequestedByMemberID: "not-a-member",
	}, candidate, translation.EntryWrite{Now: at(30)})
	require.ErrorContains(t, err, "program Event translation audit requires requester Member")

	err = programevent.ApplyTypedTranslationCandidate(systemContext(t), joined(h, recorder), programevent.TranslationJob{
		EntityType: programevent.EntityType, EntityID: event.ID, TargetLocale: "en", RequestedByMemberID: uuid.NewString(),
	}, candidate, translation.EntryWrite{Now: at(30)})
	require.NoError(t, err)
	require.Equal(t, before, h.revision(t, event), "a summary the provider was not asked for stays")
	locale, _ := h.store.StoredLocale(event.ID, "en")
	require.Equal(t, "Source summary", *locale.Summary)
	require.Empty(t, recorder.Records())
}

func TestLoadTranslationSourceDocumentReadsTheLockedSource(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t, func(event *programevent.Event) { event.Title = "Protected Title" })
	source, err := programevent.LoadTranslationSourceDocument(t.Context(), h.store, event.ID)
	require.NoError(t, err)
	require.Equal(t, "Source summary", *source.Summary)
	require.Equal(t, []string{"Protected Title"}, source.ProtectedTerms)
	require.Equal(t, h.revision(t, event).String(), source.ContentDocumentRevision)

	missing := uuid.NewString()
	_, err = programevent.LoadTranslationSourceDocument(t.Context(), h.store, missing)
	requireCode(t, err, connect.CodeNotFound, "program_event not found: "+missing)
}

func TestLoadInterchangeLocaleReadsTheTargetAgainstTheSource(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	event := h.seedEvent(t)
	missing, err := programevent.LoadInterchangeLocale(t.Context(), h.store, event.ID, "en", "ko")
	require.NoError(t, err)
	require.False(t, missing.Exists)
	h.store.PutLocale(event.ID, programevent.LocaleMetadata{Locale: "ko", Summary: text("요약"), UpdatedAt: at(1)})
	present, err := programevent.LoadInterchangeLocale(t.Context(), h.store, event.ID, "en", "ko")
	require.NoError(t, err)
	require.True(t, present.Exists)
	require.Equal(t, "요약", *present.Summary)
	require.Equal(t, h.revision(t, event).String(), present.DocumentRevision)
	require.Equal(t, contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT, present.Localized.GetProfile())

	other := uuid.NewString()
	_, err = programevent.LoadInterchangeLocale(t.Context(), h.store, other, "en", "ko")
	requireCode(t, err, connect.CodeNotFound, "program event not found: "+other)
}

func TestRequireLockedViewAndEditAuthorizeInTheCallersTransaction(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	caller := newPrincipal()
	event := h.seedEvent(t, func(event *programevent.Event) { event.Status = programevent.StatusArchived })
	checker := &authztest.Checker{}
	active := func(*auth.UserInfo) (bool, error) { return true, nil }
	inactive := func(*auth.UserInfo) (bool, error) { return false, nil }

	err := programevent.RequireLockedView(caller.context(t), h.store, checker, active, event.ID)
	requireCode(t, err, connect.CodeNotFound, "program event not found: "+event.ID)
	viewArchived, err := policyv1.ProgramEvent.ViewArchived(event.ID)
	require.NoError(t, err)
	checker.Allow(viewArchived, nil)
	require.NoError(t, programevent.RequireLockedView(caller.context(t), h.store, checker, active, event.ID))
	requireCode(t, programevent.RequireLockedView(caller.context(t), h.store, checker, inactive, event.ID), connect.CodeNotFound, "program event not found")

	err = programevent.RequireLockedSourceLocaleEdit(caller.context(t), h.store, checker, active, event.ID)
	requireCode(t, err, connect.CodePermissionDenied, "no permission to edit_archived this program event")
	editArchived, err := policyv1.ProgramEvent.EditArchived(event.ID)
	require.NoError(t, err)
	checker.Allow(editArchived, nil)
	require.NoError(t, programevent.RequireLockedSourceLocaleEdit(caller.context(t), h.store, checker, active, event.ID))

	missing := uuid.NewString()
	requireCode(t, programevent.RequireExists(t.Context(), h.store, missing), connect.CodeNotFound, "program event not found: "+missing)
	require.NoError(t, programevent.RequireExists(t.Context(), h.store, event.ID))
	_ = time.Now
}
