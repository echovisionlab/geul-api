//go:build integration

package form_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestResolveFormOgGenerationPreservesExactSourceAndTranslationTitlesIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	now := time.Now().UTC()
	formID := seedFormSourceLocaleBaseRowAt(t, db, now)
	seedFormSourceLocaleTranslationRow(t, db, formID, "ko", "원본 신청서 제목", []byte(`{"id":"source","steps":[]}`), nil, now)
	seedFormSourceLocaleTranslationRow(t, db, formID, "ja", "日本語フォーム題名", []byte(`{"id":"ja","steps":[]}`), nil, now)
	seedFormSourceLocaleTranslationRow(t, db, formID, "fr", "Titre français exact", []byte(`{"id":"fr","steps":[]}`), nil, now)
	require.NoError(t, db.Table("form").Where("id = ?", formID).Update("source_locale", "ko").Error)
	resolve := func(t *testing.T, selection *managev1.OgTargetSelection) []og.Request {
		t.Helper()
		requests, err := og.NewResolver(formogadapter.NewRequests()).Resolve(t.Context(), db, &managev1.RegenerateOgImageRequest{
			EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_FORM,
			EntityId:   &formID,
			Selection:  selection,
		})
		require.NoError(t, err)
		return requests
	}

	primary := resolve(t, &managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_Primary{
		Primary: &managev1.OgPrimaryTarget{},
	}})
	require.Equal(t, []og.Request{{
		Target: og.Target{
			EntityType: "form", EntityID: formID, Locale: ptrString("ko"), Kind: "locale",
		},
		Title: "원본 신청서 제목",
	}}, primary)

	localized := resolve(t, &managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_Locale{
		Locale: "ja",
	}})
	require.Equal(t, []og.Request{{
		Target: og.Target{
			EntityType: "form", EntityID: formID, Locale: ptrString("ja"), Kind: "locale",
		},
		Title: "日本語フォーム題名",
	}}, localized)

	allLocales := resolve(t, &managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_AllLocales{
		AllLocales: &managev1.OgAllLocaleTargets{},
	}})
	require.Len(t, allLocales, 3)
	byLocale := make(map[string]string, len(allLocales))
	for _, request := range allLocales {
		require.NotNil(t, request.Locale)
		require.NotEqual(t, "Untitled Form", request.Title)
		byLocale[*request.Locale] = request.Title
	}
	require.Equal(t, map[string]string{
		"ko": "원본 신청서 제목",
		"ja": "日本語フォーム題名",
		"fr": "Titre français exact",
	}, byLocale)

	globalRequests, err := formogadapter.NewRequests().All(t.Context(), db)
	require.NoError(t, err)
	require.Len(t, globalRequests, 3)
	globalByLocale := make(map[string]string, len(globalRequests))
	for _, request := range globalRequests {
		require.NotNil(t, request.Locale)
		globalByLocale[*request.Locale] = request.Title
	}
	require.Equal(t, byLocale, globalByLocale, "global regeneration must use the same source row and locale policy")
}
