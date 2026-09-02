package translationadapter

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestProgramEventInterchangeImportKeepsTitleSourceOwnedAndExplicitEmptySummary(t *testing.T) {
	blockID := uuid.NewString()
	summary := "Source summary"
	source := &core.SourceDocument{
		Title: "Source event title", Summary: &summary, ContentDocumentRevision: uuid.NewString(),
		ContentBlockDocument: localizedInterchangeDocument(
			contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT,
			"en",
			interchangeBaseGraph([]string{blockID}),
			map[string]string{blockID: "Source description"},
		),
	}
	plan, err := core.BuildRichTextExtractionPlan(
		&model.TranslationJob{
			EntityType: "program_event", EntityID: "event-1", SourceLocale: "en", TargetLocale: "ko",
		},
		source,
		core.RichTextDocumentFields{Summary: true},
	)
	require.NoError(t, err)
	require.False(t, interchangePlanHasUnit(plan, "entity:title"), "Program Event title remains source-owned")

	blockHandle := requireBlockInterchangeHandle(t, plan, blockID)
	command := application.TranslationInterchangeApply{
		EntityType: "program_event", EntityID: "event-1", SourceLocale: "en", TargetLocale: "ko",
		Mode:   managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
		Source: source, Plan: plan,
		Targets: map[string]core.UnitResult{
			"entity:summary": {UnitID: "entity:summary", TranslatedText: ""},
			blockHandle:      {UnitID: blockHandle, TranslatedText: "Target description"},
		},
	}
	candidate, err := buildBlockInterchangeCandidate(command, nil, command.Targets)
	require.NoError(t, err)
	candidate.Summary = entityInterchangeTarget(command.Targets, plan, "summary")
	require.Nil(t, candidate.Title)
	require.NotNil(t, candidate.Summary)
	require.Empty(t, *candidate.Summary)
	require.Equal(t, "Target description", paragraphInterchangeText(candidate.ContentBlockLocaleOverlay.GetBlocks()[0]))
}
