//go:build integration

package application

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestTranslationOverviewUsesCompleteCatalogIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	service := &TranslationService{db: db}
	_, err := service.getTranslationOverviewStats(t.Context())
	require.NoError(t, err)
	_, err = service.listTranslationLocaleHealth(t.Context())
	require.NoError(t, err)

	health, err := service.listTranslationEntityHealth(t.Context())
	require.NoError(t, err)
	require.Len(t, health, len(translation.Definitions()))

	want := make(map[managev1.TranslationEntityType]struct{}, len(health))
	for _, definition := range translation.Definitions() {
		want[definition.Proto] = struct{}{}
	}
	for _, item := range health {
		delete(want, item.EntityType)
	}
	require.Empty(t, want)
}
