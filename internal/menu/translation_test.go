package menu

import (
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestBuildTranslationCandidateAppliesMenuLabels(t *testing.T) {
	t.Parallel()
	source := &translation.SourceDocument{ContentJSON: []byte(`[
		{"id":"home","label":"Home","linkType":"page"},
		{"id":"about","label":"About","linkType":"page","children":[{"id":"team","label":"Team","linkType":"page"}]}
	]`)}
	candidate, err := BuildTranslationCandidate(source, map[string]translation.UnitResult{
		"item:home:label":  {UnitID: "item:home:label", TranslatedText: "홈"},
		"item:about:label": {UnitID: "item:about:label", TranslatedText: "소개"},
		"item:team:label":  {UnitID: "item:team:label", TranslatedText: "팀"},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `[
		{"id":"about","label":"소개"},
		{"id":"home","label":"홈"},
		{"id":"team","label":"팀"}
	]`, string(candidate.ContentJSON))
}

func TestBuildTranslationCandidatePreservesExplicitEmptyAndOmitsMissingResults(t *testing.T) {
	t.Parallel()
	source := &translation.SourceDocument{ContentJSON: []byte(`[
		{"id":"home","label":"Home"},
		{"id":"about","label":"About"}
	]`)}
	candidate, err := BuildTranslationCandidate(source, map[string]translation.UnitResult{
		"item:home:label": {UnitID: "item:home:label", TranslatedText: ""},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `[{"id":"home","label":""}]`, string(candidate.ContentJSON))
}

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

func TestLoadTranslationSourceDocumentUsesCurrentSourceValuesWithoutRootFallback(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE menu (id TEXT PRIMARY KEY, items BLOB, source_locale TEXT NOT NULL, updated_at DATETIME);
		CREATE TABLE menu_translation (entity_id TEXT, locale TEXT, items_json BLOB);
	`).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO menu (id, items, source_locale) VALUES (?, ?, 'en')`, "menu-1",
		[]byte(`[{"id":"home","label":"루트 한글"},{"id":"about","label":"루트 소개"}]`),
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json) VALUES ('menu-1', 'en', ?)`,
		[]byte(`[{"id":"home","label":""}]`),
	).Error)

	document, err := LoadTranslationSourceDocument(t.Context(), db, "menu-1")
	require.NoError(t, err)
	var items []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	require.NoError(t, json.Unmarshal(document.ContentJSON, &items))
	require.Empty(t, items[0].Label, "explicit empty source value must remain empty")
	require.Empty(t, items[1].Label, "missing source value must not fall back to root")
}

func TestReconcileTranslationRowsPrunesOnlyDeletedIDs(t *testing.T) {
	db := openMenuTranslationTestDB(t)
	previous := time.Unix(100, 0).UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, updated_at) VALUES (?, ?, ?, ?)`,
		"menu-1", "fr", []byte(`[{"id":"deleted","label":"Ancien"},{"id":"home","label":""}]`), previous,
	).Error)

	now := previous.Add(time.Second)
	require.NoError(t, ReconcileTranslationRowsWithSourceJSON(
		t.Context(), db, "menu-1", []byte(`[{"id":"home","label":"Home"}]`), now,
	))

	var row struct {
		ItemsJSON []byte    `gorm:"column:items_json"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	require.NoError(t, db.Table("menu_translation").Select("items_json, updated_at").
		Where("entity_id = ? AND locale = ?", "menu-1", "fr").Take(&row).Error)
	assert.JSONEq(t, `[{"id":"home","label":""}]`, string(row.ItemsJSON))
	require.True(t, row.UpdatedAt.After(previous))
}

func TestReconcileTranslationRowsLeavesUnchangedValuesAndRevisionUntouched(t *testing.T) {
	db := openMenuTranslationTestDB(t)
	previous := time.Unix(100, 0).UTC()
	original := []byte(`[{"id":"home","label":""},{"id":"about","label":"À propos"}]`)
	require.NoError(t, db.Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, updated_at) VALUES (?, ?, ?, ?)`,
		"menu-1", "fr", original, previous,
	).Error)

	require.NoError(t, ReconcileTranslationRowsWithSourceJSON(
		t.Context(), db, "menu-1",
		[]byte(`[{"id":"new","label":"New"},{"id":"about","label":"Renamed source"},{"id":"home","label":"Home","targetSlug":"changed"}]`),
		previous.Add(time.Hour),
	))

	var row struct {
		ItemsJSON []byte    `gorm:"column:items_json"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	require.NoError(t, db.Table("menu_translation").Select("items_json, updated_at").
		Where("entity_id = ? AND locale = ?", "menu-1", "fr").Take(&row).Error)
	require.Equal(t, original, row.ItemsJSON)
	require.True(t, row.UpdatedAt.Equal(previous), "source-only changes must preserve target revision")
}

func TestSyncCurrentSourceLabelValuesWritesOnlyCurrentSourceRow(t *testing.T) {
	db := openMenuTranslationTestDB(t)
	previous := time.Unix(100, 0).UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO menu (id, items, source_locale, updated_at) VALUES ('menu-1', '[]', 'en', ?)`, previous,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, updated_at) VALUES (?, ?, ?, ?)`,
		"menu-1", "en", []byte(`[{"id":"home","label":"Old"}]`), previous,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, updated_at) VALUES (?, ?, ?, ?)`,
		"menu-1", "fr", []byte(`[{"id":"home","label":"Accueil"}]`), previous,
	).Error)

	require.NoError(t, SyncCurrentSourceLabelValues(t.Context(), db, "menu-1", []model.MenuItem{
		{ID: "home", Label: ""},
		{ID: "about", Label: "About"},
	}, previous.Add(time.Second)))

	var rows []struct {
		Locale    string    `gorm:"column:locale"`
		ItemsJSON []byte    `gorm:"column:items_json"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	require.NoError(t, db.Table("menu_translation").Select("locale, items_json, updated_at").Order("locale").Find(&rows).Error)
	require.Len(t, rows, 2)
	require.Equal(t, "en", rows[0].Locale)
	require.JSONEq(t, `[{"id":"about","label":"About"},{"id":"home","label":""}]`, string(rows[0].ItemsJSON))
	require.True(t, rows[0].UpdatedAt.After(previous))
	require.Equal(t, "fr", rows[1].Locale)
	require.JSONEq(t, `[{"id":"home","label":"Accueil"}]`, string(rows[1].ItemsJSON))
	require.True(t, rows[1].UpdatedAt.Equal(previous), "non-source locale revision must remain unchanged")
}

func TestInitializeCurrentSourceLabelValuesCreatesExactRootSourceRowOnce(t *testing.T) {
	db := openMenuTranslationTestDB(t)
	now := time.Unix(100, 0).UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO menu (id, items, source_locale, updated_at) VALUES (?, ?, 'ko', ?)`,
		"menu-1", []byte(`[{"id":"home","label":"홈","linkType":"page"},{"id":"about","label":"","linkType":"page"}]`), now,
	).Error)
	require.NoError(t, initializeCurrentSourceLabelValues(
		t.Context(), db, "menu-1",
		[]byte(`[{"id":"home","label":"홈","linkType":"page"},{"id":"about","label":"","linkType":"page"}]`),
		now,
	))

	document, err := LoadTranslationSourceDocument(t.Context(), db, "menu-1")
	require.NoError(t, err)
	require.JSONEq(t, `[{"id":"home","label":"홈","linkType":"page"},{"id":"about","label":"","linkType":"page"}]`, string(document.ContentJSON))

	var row struct {
		Locale    string `gorm:"column:locale"`
		ItemsJSON []byte `gorm:"column:items_json"`
	}
	require.NoError(t, db.Table("menu_translation").Select("locale, items_json").
		Where("entity_id = ?", "menu-1").Take(&row).Error)
	require.Equal(t, "ko", row.Locale)
	require.JSONEq(t, `[{"id":"about","label":""},{"id":"home","label":"홈"}]`, string(row.ItemsJSON))

	require.NoError(t, db.Exec(
		`UPDATE menu SET items = ?, updated_at = ? WHERE id = 'menu-1'`,
		[]byte(`[{"id":"home","label":"changed root","linkType":"page"},{"id":"about","label":"changed root","linkType":"page"}]`), now.Add(time.Minute),
	).Error)
	document, err = LoadTranslationSourceDocument(t.Context(), db, "menu-1")
	require.NoError(t, err)
	require.JSONEq(t, `[{"id":"home","label":"홈","linkType":"page"},{"id":"about","label":"","linkType":"page"}]`, string(document.ContentJSON),
		"the exact root source row remains authoritative after initialization")
}

func TestLoadTranslationSourceDocumentRejectsMissingRootSourceValues(t *testing.T) {
	db := openMenuTranslationTestDB(t)
	now := time.Unix(100, 0).UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO menu (id, items, source_locale, updated_at) VALUES (?, ?, 'en', ?)`,
		"menu-1", []byte(`[{"id":"home","label":"root must not become source"}]`), now,
	).Error)

	_, err := LoadTranslationSourceDocument(t.Context(), db, "menu-1")
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	var count int64
	require.NoError(t, db.Table("menu_translation").Where("entity_id = ?", "menu-1").Count(&count).Error)
	require.Zero(t, count, "a missing source row must not be seeded from root labels")
}

func openMenuTranslationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE menu (
			id TEXT PRIMARY KEY, items BLOB,
			source_locale TEXT NOT NULL DEFAULT 'en', updated_at DATETIME
		);
		CREATE TABLE menu_translation (
			entity_id TEXT, locale TEXT, items_json BLOB, created_at DATETIME, updated_at DATETIME,
			PRIMARY KEY (entity_id, locale)
		)
	`).Error)
	return db
}
