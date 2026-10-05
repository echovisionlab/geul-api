package public

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestArtistPublicLocalizationProjectsOnlyLocaleOwnedValues(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		"locale, title, NULL::text AS summary, NULL::jsonb AS content_json, NULL::text AS content_html, NULL::text AS content_text, og_asset_id",
		artistLocalizationSpec.SelectClause,
	)
}
