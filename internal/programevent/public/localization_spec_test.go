package public

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProgramEventLocalizationSpecOwnsCurrentSummaryOnly(t *testing.T) {
	t.Parallel()

	require.Equal(t, "program_event_translation", programEventLocalizationSpec.TableName)
	require.Contains(t, programEventLocalizationSpec.SelectClause, "summary")
	require.Contains(t, programEventLocalizationSpec.SelectClause, "NULL::text AS content_html")
	require.Contains(t, programEventLocalizationSpec.SelectClause, "NULL::text AS content_text")
	require.NotContains(t, programEventLocalizationSpec.SelectClause, "og_asset_id")
	require.Contains(t, programEventLocalizationSpec.SelectClause, "locale")
	require.NotContains(t, programEventLocalizationSpec.SelectClause, "status")
}
