package work_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/work"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

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
