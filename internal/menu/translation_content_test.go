package menu

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTranslationExtractionPlanSkipsFixedLocaleItemsForOtherTargets(t *testing.T) {
	t.Parallel()
	plan, err := BuildTranslationExtractionPlan("menu-1", "ko", "fr", &translation.SourceDocument{
		ContentJSON: []byte(`[
			{"id":"home","label":"홈","linkType":"page"},
			{"id":"brand","label":"브랜드","linkType":"page","localizationMode":"fixed_locale","fixedLocale":"en"},
			{"id":"team","label":"팀","linkType":"page","children":[{"id":"contact","label":"문의","linkType":"page","localizationMode":"fixed_locale","fixedLocale":"ja"}]}
		]`),
	})
	require.NoError(t, err)
	unitIDs := make([]string, 0, len(plan.Units))
	for _, unit := range plan.Units {
		unitIDs = append(unitIDs, unit.UnitID)
	}
	assert.Contains(t, unitIDs, "item:home:label")
	assert.Contains(t, unitIDs, "item:team:label")
	assert.NotContains(t, unitIDs, "item:brand:label")
	assert.NotContains(t, unitIDs, "item:contact:label")
}
