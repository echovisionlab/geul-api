package translation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyCandidateFieldsIgnoresNestedTitleUnits(t *testing.T) {
	t.Parallel()
	candidate := &Candidate{}
	bundles := []Bundle{{Units: []Unit{
		{UnitID: "entity:title", ContainerType: ContainerTypeEntity, FieldName: "title"},
		{UnitID: "section:scene:title", ContainerType: ContainerTypeBlock, FieldName: "title"},
	}}}
	ApplyCandidateFields(candidate, bundles, map[string]UnitResult{
		"entity:title":        {TranslatedText: "Entity title"},
		"section:scene:title": {TranslatedText: "Nested title"},
	})
	require.NotNil(t, candidate.Title)
	require.Equal(t, "Entity title", *candidate.Title)
}

func TestApplyCandidateFieldsPreservesExplicitEmptyEntityUnit(t *testing.T) {
	t.Parallel()
	unit := NewEntityUnit("post", "post-1", "en", "summary", "")
	candidate := &Candidate{}
	ApplyCandidateFields(candidate, []Bundle{{Units: []Unit{unit}}}, map[string]UnitResult{
		unit.UnitID: {UnitID: unit.UnitID, TranslatedText: ""},
	})
	if candidate.Summary == nil || *candidate.Summary != "" {
		t.Fatalf("summary = %#v, want explicit empty", candidate.Summary)
	}
}
