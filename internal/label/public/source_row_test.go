package public

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLabelPublicConfigUsesSourceLocaleTitle(t *testing.T) {
	t.Parallel()

	sourceTitleSQL := labelSourceTitleSQL("label")

	field, ok := LabelFilterConfig.Fields["search"]
	require.True(t, ok)
	assert.Equal(t, []string{sourceTitleSQL}, field.SearchColumns)
	assert.Equal(t, sourceTitleSQL, LabelSortConfig.AllowedFields["name"])
	assert.Equal(t, sourceTitleSQL+" ASC", LabelSortConfig.DefaultSort)
}

func TestLabelPublicLocalizationProjectsOnlyLocaleOwnedValues(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		"locale, title, NULL::text AS summary, NULL::jsonb AS content_json, NULL::text AS content_html, NULL::text AS content_text, NULL::uuid AS og_asset_id",
		labelLocalizationSpec.SelectClause,
	)
}

func TestLocalizedLabelName(t *testing.T) {
	t.Parallel()

	localized := "Localized label"
	explicitEmpty := ""
	assert.Equal(t, "Source label", localizedLabelName("Source label", nil))
	assert.Equal(t, localized, localizedLabelName("Source label", &localized))
	assert.Empty(t, localizedLabelName("Source label", &explicitEmpty))
}

func TestLabelHasEffectiveLogo(t *testing.T) {
	t.Parallel()

	light := " light-file "
	dark := "dark-file"
	blank := "   "

	assert.True(t, labelHasEffectiveLogo(&light, nil))
	assert.True(t, labelHasEffectiveLogo(nil, &dark))
	assert.False(t, labelHasEffectiveLogo(nil, nil))
	assert.False(t, labelHasEffectiveLogo(&blank, nil))
}
