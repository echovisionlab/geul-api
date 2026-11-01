package programeventpublicpostgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProgramEventLocalizationSpecOwnsCurrentSummaryOnly(t *testing.T) {
	t.Parallel()

	require.Equal(t, "program_event_translation", localizationSpec.TableName)
	require.Contains(t, localizationSpec.SelectClause, "summary")
	require.Contains(t, localizationSpec.SelectClause, "NULL::text AS content_html")
	require.Contains(t, localizationSpec.SelectClause, "NULL::text AS content_text")
	require.NotContains(t, localizationSpec.SelectClause, "og_asset_id")
	require.Contains(t, localizationSpec.SelectClause, "locale")
	require.NotContains(t, localizationSpec.SelectClause, "status")
}
