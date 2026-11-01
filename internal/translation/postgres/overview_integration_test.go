//go:build integration

package translationpostgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
)

func TestOverviewRunsAcrossTheWholeCatalogIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	store := &store{tx: db}
	_, err := store.OverviewStats(t.Context())
	require.NoError(t, err)
	_, err = store.LocaleHealth(t.Context())
	require.NoError(t, err)

	health, err := store.EntityHealth(t.Context())
	require.NoError(t, err)
	require.Len(t, health, len(translation.Definitions()))

	want := make(map[string]struct{}, len(health))
	for _, definition := range translation.Definitions() {
		want[string(definition.Kind)] = struct{}{}
	}
	for _, item := range health {
		delete(want, item.EntityType)
	}
	require.Empty(t, want)
}
