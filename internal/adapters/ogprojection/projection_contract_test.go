//go:build integration

package ogprojection_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	artistadapter "github.com/echovisionlab/geul-api/internal/adapters/artist"
	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/og"
)

func TestClearPendingOgProjectionCoversEveryGeneratedEntityShape(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE post_translation (entity_id text, locale text, og_asset_id text);
		CREATE TABLE page_translation (entity_id text, locale text, og_asset_id text);
		CREATE TABLE series_translation (entity_id text, locale text, og_asset_id text);
		CREATE TABLE form_translation (entity_id text, locale text, og_asset_id text);
		CREATE TABLE work_translation (entity_id text, locale text, og_asset_id text);
		CREATE TABLE artist_translation (entity_id text, locale text, og_asset_id text);
		CREATE TABLE work (id text, og_asset_id text);
		CREATE TABLE label (id text, og_asset_id text);
		CREATE TABLE artist (id text, og_asset_id text);
		CREATE TABLE site_settings (id integer, site_og_asset_id text);
		CREATE TABLE public_asset_binding (
			asset_id text, owner_type text, owner_id text, binding_key text,
			created_at datetime, updated_at datetime
		);
	`).Error)

	for _, table := range []string{"post_translation", "page_translation", "series_translation", "form_translation", "work_translation", "artist_translation"} {
		require.NoError(t, db.Exec("INSERT INTO "+table+" (entity_id, locale, og_asset_id) VALUES ('entity', 'ko', 'old')").Error)
	}
	for _, table := range []string{"work", "label", "artist"} {
		require.NoError(t, db.Exec("INSERT INTO "+table+" (id, og_asset_id) VALUES ('entity', 'old')").Error)
	}
	require.NoError(t, db.Exec("INSERT INTO site_settings (id, site_og_asset_id) VALUES (1, 'old')").Error)

	locale := "ko"
	translated := []struct {
		entityType string
		projection og.Projection
	}{
		{entityType: "post", projection: postadapter.NewProjection()},
		{entityType: "page", projection: pageadapter.NewProjection()},
		{entityType: "series", projection: seriesadapter.NewProjection()},
		{entityType: "form", projection: formogadapter.NewProjection()},
		{entityType: "work", projection: workadapter.NewProjection()},
		{entityType: "artist", projection: artistadapter.NewProjection()},
	}
	for _, target := range translated {
		require.NoError(t, target.projection.ReleasePending(t.Context(), db, og.Target{
			EntityType: target.entityType, EntityID: "entity", Locale: &locale, Kind: "locale",
		}, ""))
		var value *string
		require.NoError(t, db.Table(target.entityType+"_translation").Select("og_asset_id").
			Where("entity_id = ? AND locale = ?", "entity", "ko").Scan(&value).Error)
		require.Nil(t, value)
	}
	baseOnly := []struct {
		entityType string
		projection og.Projection
	}{
		{entityType: "label", projection: labeladapter.NewProjection()},
	}
	for _, target := range baseOnly {
		require.NoError(t, target.projection.ReleasePending(t.Context(), db, og.Target{
			EntityType: target.entityType, EntityID: "entity", Kind: "entity",
		}, ""))
		var value *string
		require.NoError(t, db.Table(target.entityType).Select("og_asset_id").
			Where("id = ?", "entity").Scan(&value).Error)
		require.Nil(t, value)
	}

	require.NoError(t, sitesettingsadapter.NewProjection().ReleasePending(t.Context(), db, og.Target{
		EntityType: "site", EntityID: "default", Kind: "entity",
	}, ""))
	var siteValue *string
	require.NoError(t, db.Table("site_settings").Select("site_og_asset_id").Where("id = ?", 1).Scan(&siteValue).Error)
	require.Nil(t, siteValue)
	for _, entityType := range []string{"privacy", "terms"} {
		require.NoError(t, legaladapter.NewProjection().ReleasePending(t.Context(), db, og.Target{
			EntityType: entityType, EntityID: legaladapter.RouteID(entityType), Locale: &locale, Kind: "locale",
		}, ""))
	}
	require.Error(t, postadapter.NewProjection().ReleasePending(t.Context(), db, og.Target{
		EntityType: "unknown", EntityID: "entity", Kind: "entity",
	}, ""))
}
