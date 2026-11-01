package work_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/work"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestApplyTypedTranslationCandidateCreatesThenUpdatesTheTargetForItsRequester(t *testing.T) {
	h := newHarness(t)
	requester := newPrincipal()
	value := h.seedWork(t, "Source")
	recorder := &audittest.Recorder{}
	joined := work.Joined{Store: h.store, Audit: recorder}
	job := work.TranslationJob{EntityType: "work", EntityID: value.ID, SourceLocale: "en", TargetLocale: "ko", RequestedByMemberID: requester.memberID}
	candidate := func() *translation.Candidate {
		return &translation.Candidate{
			ContentDocumentRevision:   h.revision(t, value.ID).String(),
			ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"},
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)

	require.NoError(t, work.ApplyTypedTranslationCandidate(t.Context(), joined, job, candidate(), translation.EntryWrite{Title: stringPointer("제목"), Now: now}))
	target, ok := h.store.Locale(value.ID, "ko")
	require.True(t, ok)
	require.Equal(t, "제목", *target.Title)
	require.NoError(t, work.ApplyTypedTranslationCandidate(t.Context(), joined, job, candidate(), translation.EntryWrite{Title: stringPointer("새 제목"), Now: now.Add(time.Second)}))
	target, _ = h.store.Locale(value.ID, "ko")
	require.Equal(t, "새 제목", *target.Title)
	require.NoError(t, work.ApplyTypedTranslationCandidate(t.Context(), joined, job, candidate(), translation.EntryWrite{Title: stringPointer("새 제목"), Now: now.Add(2 * time.Second)}))

	var operations []sharedtelemetry.AuditItemOperation
	for _, record := range recorder.Records() {
		require.Equal(t, requester.memberID, record.MemberID)
		require.Equal(t, "ko", record.Locale)
		operations = append(operations, record.ItemOperation)
	}
	require.Equal(t, []sharedtelemetry.AuditItemOperation{sharedtelemetry.AuditItemOperationCreated, sharedtelemetry.AuditItemOperationUpdated}, operations,
		"an unchanged delivery is not audited")
}

func TestApplyTypedTranslationCandidateRejectsAStaleRevisionAndAnAnonymousDelivery(t *testing.T) {
	h := newHarness(t)
	value := h.seedWork(t, "Source")
	joined := work.Joined{Store: h.store, Audit: &audittest.Recorder{}}
	job := work.TranslationJob{EntityType: "work", EntityID: value.ID, SourceLocale: "en", TargetLocale: "ko"}

	err := work.ApplyTypedTranslationCandidate(t.Context(), joined, job, &translation.Candidate{
		ContentDocumentRevision: uuid.NewString(), ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"},
	}, translation.EntryWrite{Title: stringPointer("제목"), Now: time.Now()})
	requireCode(t, err, connect.CodeInvalidArgument, "batch: Work target document identity and revision must match the locked state")

	err = work.ApplyTypedTranslationCandidate(t.Context(), joined, job, &translation.Candidate{
		ContentDocumentRevision: h.revision(t, value.ID).String(), ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"},
	}, translation.EntryWrite{Title: stringPointer("제목"), Now: time.Now()})
	require.ErrorContains(t, err, "requires its requesting Member")
}

func TestApplyTranslationInterchangeImportsTheTargetUnderItsRevision(t *testing.T) {
	h := newHarness(t)
	value := h.seedWork(t, "Source")
	store := h.store
	plan := &translation.ExtractionPlan{
		EntityType: "work", EntityID: value.ID, SourceLocale: "en", TargetLocale: "ko",
		Units: []translation.Unit{{UnitID: "entity:title"}},
	}
	source, err := work.LoadTypedTranslationSourceDocument(t.Context(), store, value.ID)
	require.NoError(t, err)
	project := func(*translation.ExtractionPlan, *contentv1.LocalizedRichTextDocument) (map[string]translation.UnitResult, error) {
		return map[string]translation.UnitResult{}, nil
	}
	patch := func(_ *translation.ExtractionPlan, _, _ *contentv1.LocalizedRichTextDocument, _ map[string]translation.UnitResult) (*contentv1.RichTextLocaleOverlay, error) {
		return &contentv1.RichTextLocaleOverlay{Locale: "ko"}, nil
	}
	mutation := work.TranslationInterchangeMutation{
		WorkID: value.ID, SourceLocale: "en", TargetLocale: "ko", Mode: work.InterchangePatch,
		Source: source, Plan: plan, Now: time.Now(), ProjectTargets: project, BuildPatch: patch,
		Targets:     map[string]translation.UnitResult{"entity:title": {UnitID: "entity:title", TranslatedText: "가져온 제목"}},
		UnitHandles: []string{"entity:title"},
	}

	result, err := work.ApplyTranslationInterchange(t.Context(), store, mutation)

	require.NoError(t, err)
	require.True(t, result.Changed)
	require.False(t, result.TargetPreviouslyExists)
	require.Equal(t, []string{"entity:title"}, result.AffectedUnitHandles)
	target, ok := store.Locale(value.ID, "ko")
	require.True(t, ok)
	require.Equal(t, "가져온 제목", *target.Title)
	loaded, err := work.LoadTranslationInterchangeTarget(t.Context(), store, value.ID, "ko", plan, project)
	require.NoError(t, err)
	require.True(t, loaded.Exists)
	require.Equal(t, result.Revision, loaded.Revision)
	require.Equal(t, "가져온 제목", loaded.Targets["entity:title"].TranslatedText)

	mutation.ExpectedRevision = stringPointer("stale")
	_, err = work.ApplyTranslationInterchange(t.Context(), store, mutation)
	requireCode(t, err, connect.CodeFailedPrecondition, "")
	mutation.Mode = 0
	_, err = work.ApplyTranslationInterchange(t.Context(), store, mutation)
	requireCode(t, err, connect.CodeInvalidArgument, "mode: PATCH or REPLACE is required")
}
