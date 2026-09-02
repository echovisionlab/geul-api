//go:build integration

package emaildelivery

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestEmailRenderDataUsesRuntimeSiteOriginIntegration(t *testing.T) {
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	require.NoError(t, db.Exec("UPDATE site_settings SET site_title = ? WHERE id = ?", "Example Studio", 1).Error)

	data := BuildEmailRenderData(
		t.Context(),
		db,
		"https://cdn.example.com",
		"https://www.example.test",
		"en",
		map[string]string{"site_origin": "https://input.example.test"},
	)

	require.Equal(t, "Example Studio", data["site_name"])
	require.Equal(t, "https://www.example.test", data["site_origin"])
}
