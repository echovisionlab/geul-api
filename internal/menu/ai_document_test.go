package menu

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/translation"
)

func TestMenuAIDocumentTargetCompilerPreservesExplicitEmptyAndProtectsFixedLocale(t *testing.T) {
	fixedMode := LocalizationFixedLocale
	english := "en"
	japanese := "ja"
	snapshot := AIDocumentSnapshot{
		ID: "menu-1", Name: "Main", SourceLocale: "ko", Locale: "en",
		LocaleExists: true, DocumentRevision: "revision-1", Labels: map[string]string{"translated": "Home"},
		sourceLabels: map[string]string{"translated": "홈", "about": ""},
		Items: []AIDocumentItem{
			{ID: "translated", Label: "홈", LinkType: "custom", URL: stringTestPointer("/"), Children: nil},
			{ID: "about", Label: "소개", LinkType: "custom", URL: stringTestPointer("/about")},
			{ID: "brand", Label: "Brand", LinkType: "custom", URL: stringTestPointer("/brand"), LocalizationMode: &fixedMode, FixedLocale: &english},
			{ID: "fixed", Label: "고정", LinkType: "custom", URL: stringTestPointer("/fixed"), LocalizationMode: &fixedMode, FixedLocale: &japanese},
		},
	}
	service := &Service{}

	compiled, issues := service.compileAIDocument(snapshot, []AIDocumentOperation{{
		Kind: AIDocumentSetItemField, ItemID: "translated", Field: "label",
		Value: AIDocumentValue{Kind: AIDocumentText, Text: ""},
	}})
	require.Empty(t, issues)
	require.True(t, compiled.changed)
	value, exists := compiled.snapshot.Labels["translated"]
	require.True(t, exists)
	require.Empty(t, value, "explicit empty is a stored value, not a missing label")

	compiled, issues = service.compileAIDocument(snapshot, []AIDocumentOperation{{
		Kind: AIDocumentUnsetItemField, ItemID: "translated", Field: "label",
	}})
	require.Empty(t, issues)
	require.True(t, compiled.targetChanged)
	require.NotContains(t, compiled.snapshot.Labels, "translated")
	require.Equal(t, "Home", snapshot.Labels["translated"], "target unset must not remove the locale unit")

	_, issues = service.compileAIDocument(snapshot, []AIDocumentOperation{{
		Kind: AIDocumentSetItemField, ItemID: "fixed", Field: "label",
		Value: AIDocumentValue{Kind: AIDocumentText, Text: "Fixed"},
	}})
	require.Len(t, issues, 1)
	require.Equal(t, AIDocumentIssueTargetForbidden, issues[0].Code)

	_, issues = service.compileAIDocument(snapshot, []AIDocumentOperation{{
		Kind: AIDocumentSetItemField, ItemID: "translated", Field: "url",
		Value: AIDocumentValue{Kind: AIDocumentText, Text: "/changed"},
	}})
	require.Len(t, issues, 1)
	require.Equal(t, AIDocumentIssueTargetForbidden, issues[0].Code)

	absent := snapshot
	absent.LocaleExists = false
	absent.TargetRevision = nil
	absent.Labels = map[string]string{}
	compiled, issues = service.compileAIDocument(absent, []AIDocumentOperation{{
		Kind: AIDocumentSetItemField, ItemID: "translated", Field: "label",
		Value: AIDocumentValue{Kind: AIDocumentText, Text: "Home"},
	}})
	require.Empty(t, issues)
	require.True(t, compiled.targetChanged)
	require.True(t, compiled.snapshot.LocaleExists)
	require.Equal(t, "Home", compiled.snapshot.Labels["translated"])
	require.Contains(t, compiled.snapshot.Labels, "about")
	require.Empty(t, compiled.snapshot.Labels["about"], "source explicit empty must be seeded as an explicit target value")
	require.Empty(t, compiled.snapshot.Labels["brand"], "fixed-locale creation must not revive the legacy root label")
	require.NotContains(t, compiled.snapshot.Labels, "fixed", "another fixed locale is not owned by this target")

	created, issues := service.compileAIDocument(absent, []AIDocumentOperation{{Kind: AIDocumentCreateTranslation}})
	require.Empty(t, issues)
	require.True(t, created.snapshot.LocaleExists)
	require.Equal(t, map[string]string{
		"translated": "홈", "about": "", "brand": "",
	}, created.snapshot.Labels)
}

func TestMenuAIDocumentSourceCompilerUsesStableTreeIDsAndValidatesFinalTree(t *testing.T) {
	snapshot := AIDocumentSnapshot{
		ID: "menu-1", Name: "Main", SourceLocale: "ko", Locale: "ko",
		LocaleExists: true, SourceValuesStored: true, DocumentRevision: "revision-1", Labels: map[string]string{"home": "홈"},
		Items: []AIDocumentItem{{ID: "home", Label: "홈", LinkType: "custom", URL: stringTestPointer("/")}},
	}
	service := &Service{}
	compiled, issues := service.compileAIDocument(snapshot, []AIDocumentOperation{
		{Kind: AIDocumentInsertItem, ItemID: "about", ParentID: "menu-1", AfterID: "home"},
		{Kind: AIDocumentSetItemField, ItemID: "about", Field: "label", Value: AIDocumentValue{Kind: AIDocumentText, Text: "소개"}},
		{Kind: AIDocumentSetItemField, ItemID: "about", Field: "link_type", Value: AIDocumentValue{Kind: AIDocumentText, Text: "custom"}},
		{Kind: AIDocumentSetItemField, ItemID: "about", Field: "url", Value: AIDocumentValue{Kind: AIDocumentText, Text: "/about"}},
		{Kind: AIDocumentMoveItem, ItemID: "home", ParentID: "about"},
	})
	require.Empty(t, issues)
	require.True(t, compiled.sourceChanged)
	require.Equal(t, "about", compiled.snapshot.Items[0].ID)
	require.Equal(t, "home", compiled.snapshot.Items[0].Children[0].ID)

	_, issues = service.compileAIDocument(snapshot, []AIDocumentOperation{{
		Kind: AIDocumentDeleteItem, ItemID: "menu-1",
	}})
	require.Len(t, issues, 1)
}

func TestMenuAIDocumentTargetRevisionBindsDocumentAndExactLocaleFacts(t *testing.T) {
	firstTime := time.Unix(100, 0).UTC()
	secondTime := firstTime.Add(time.Microsecond)
	base := uuid.NewString()
	nextDocument := uuid.NewString()
	firstTarget, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{LocaleExists: true, DocumentRevision: base, LocaleUpdatedAt: &firstTime})
	require.NoError(t, err)
	secondTarget, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{LocaleExists: true, DocumentRevision: base, LocaleUpdatedAt: &secondTime})
	require.NoError(t, err)
	require.NotEqual(t, firstTarget, secondTarget)
	invalidatedTarget, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{LocaleExists: true, DocumentRevision: nextDocument, LocaleUpdatedAt: &firstTime})
	require.NoError(t, err)
	require.NotEqual(t, firstTarget, invalidatedTarget)
}

func stringTestPointer(value string) *string { return &value }
