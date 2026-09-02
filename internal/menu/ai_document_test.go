package menu

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
)

func TestMenuAIDocumentTargetCompilerPreservesExplicitEmptyAndProtectsFixedLocale(t *testing.T) {
	fixedMode := model.MenuItemLocalizationModeFixedLocale
	english := "en"
	japanese := "ja"
	snapshot := AIDocumentSnapshot{
		ID: "menu-1", Name: "Main", SourceLocale: "ko", Locale: "en",
		LocaleExists: true, DocumentRevision: "revision-1", Labels: map[string]string{"translated": "Home"},
		sourceLabels: map[string]string{"translated": "홈", "about": ""},
		Items: []AIDocumentItem{
			{ID: "translated", Label: "홈", LinkType: "custom", URL: stringTestPointer("/"), Children: nil},
			{ID: "about", Label: "소개", LinkType: "custom", URL: stringTestPointer("/about")},
			{ID: "brand", Label: "Brand", LinkType: "custom", URL: stringTestPointer("/brand"), LocalizationMode: &fixedMode, FixedLocale: &english},
			{ID: "fixed", Label: "고정", LinkType: "custom", URL: stringTestPointer("/fixed"), LocalizationMode: &fixedMode, FixedLocale: &japanese},
		},
	}
	service := &MenuService{}

	compiled, issues := service.compileAIDocument(snapshot, []AIDocumentOperation{{
		Kind: AIDocumentSetItemField, ItemID: "translated", Field: "label",
		Value: AIDocumentValue{Kind: AIDocumentText, Text: ""},
	}})
	require.Empty(t, issues)
	require.True(t, compiled.changed)
	value, exists := compiled.snapshot.Labels["translated"]
	require.True(t, exists)
	require.Empty(t, value, "explicit empty is a stored value, not a missing label")

	compiled, issues = service.compileAIDocument(snapshot, []AIDocumentOperation{{
		Kind: AIDocumentUnsetItemField, ItemID: "translated", Field: "label",
	}})
	require.Empty(t, issues)
	require.True(t, compiled.targetChanged)
	require.NotContains(t, compiled.snapshot.Labels, "translated")
	require.Equal(t, "Home", snapshot.Labels["translated"], "target unset must not remove the locale unit")

	_, issues = service.compileAIDocument(snapshot, []AIDocumentOperation{{
		Kind: AIDocumentSetItemField, ItemID: "fixed", Field: "label",
		Value: AIDocumentValue{Kind: AIDocumentText, Text: "Fixed"},
	}})
	require.Len(t, issues, 1)
	require.Equal(t, AIDocumentIssueTargetForbidden, issues[0].Code)

	_, issues = service.compileAIDocument(snapshot, []AIDocumentOperation{{
		Kind: AIDocumentSetItemField, ItemID: "translated", Field: "url",
		Value: AIDocumentValue{Kind: AIDocumentText, Text: "/changed"},
	}})
	require.Len(t, issues, 1)
	require.Equal(t, AIDocumentIssueTargetForbidden, issues[0].Code)

	absent := snapshot
	absent.LocaleExists = false
	absent.TargetRevision = nil
	absent.Labels = map[string]string{}
	compiled, issues = service.compileAIDocument(absent, []AIDocumentOperation{{
		Kind: AIDocumentSetItemField, ItemID: "translated", Field: "label",
		Value: AIDocumentValue{Kind: AIDocumentText, Text: "Home"},
	}})
	require.Empty(t, issues)
	require.True(t, compiled.targetChanged)
	require.True(t, compiled.snapshot.LocaleExists)
	require.Equal(t, "Home", compiled.snapshot.Labels["translated"])
	require.Contains(t, compiled.snapshot.Labels, "about")
	require.Empty(t, compiled.snapshot.Labels["about"], "source explicit empty must be seeded as an explicit target value")
	require.Empty(t, compiled.snapshot.Labels["brand"], "fixed-locale creation must not revive the legacy root label")
	require.NotContains(t, compiled.snapshot.Labels, "fixed", "another fixed locale is not owned by this target")

	created, issues := service.compileAIDocument(absent, []AIDocumentOperation{{Kind: AIDocumentCreateTranslation}})
	require.Empty(t, issues)
	require.True(t, created.snapshot.LocaleExists)
	require.Equal(t, map[string]string{
		"translated": "홈", "about": "", "brand": "",
	}, created.snapshot.Labels)
}

func TestMenuAIDocumentSourceCompilerUsesStableTreeIDsAndValidatesFinalTree(t *testing.T) {
	snapshot := AIDocumentSnapshot{
		ID: "menu-1", Name: "Main", SourceLocale: "ko", Locale: "ko",
		LocaleExists: true, SourceValuesStored: true, DocumentRevision: "revision-1", Labels: map[string]string{"home": "홈"},
		Items: []AIDocumentItem{{ID: "home", Label: "홈", LinkType: "custom", URL: stringTestPointer("/")}},
	}
	service := &MenuService{}
	compiled, issues := service.compileAIDocument(snapshot, []AIDocumentOperation{
		{Kind: AIDocumentInsertItem, ItemID: "about", ParentID: "menu-1", AfterID: "home"},
		{Kind: AIDocumentSetItemField, ItemID: "about", Field: "label", Value: AIDocumentValue{Kind: AIDocumentText, Text: "소개"}},
		{Kind: AIDocumentSetItemField, ItemID: "about", Field: "link_type", Value: AIDocumentValue{Kind: AIDocumentText, Text: "custom"}},
		{Kind: AIDocumentSetItemField, ItemID: "about", Field: "url", Value: AIDocumentValue{Kind: AIDocumentText, Text: "/about"}},
		{Kind: AIDocumentMoveItem, ItemID: "home", ParentID: "about"},
	})
	require.Empty(t, issues)
	require.True(t, compiled.sourceChanged)
	require.Equal(t, "about", compiled.snapshot.Items[0].ID)
	require.Equal(t, "home", compiled.snapshot.Items[0].Children[0].ID)

	_, issues = service.compileAIDocument(snapshot, []AIDocumentOperation{{
		Kind: AIDocumentDeleteItem, ItemID: "menu-1",
	}})
	require.Len(t, issues, 1)
}

func TestLoadMenuAIDocumentSnapshotProjectsValuesOnlyAndExplicitEmpty(t *testing.T) {
	db := openMenuAIDocumentStateTestDB(t)
	now := time.Now().UTC()
	_, revision := seedMenuAIDocumentStateTestRoot(
		t, db, "menu-1", "ko",
		[]byte(`[{"id":"home","label":"홈","linkType":"custom","url":"/"},{"id":"about","label":"소개","linkType":"custom","url":"/about"}]`),
		now,
	)
	require.NoError(t, db.Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, created_at, updated_at)
		 VALUES ('menu-1', 'ko', '[{"id":"about","label":"소개"},{"id":"home","label":"홈"}]', ?, ?),
		        ('menu-1', 'en', '[{"id":"home","label":""}]', ?, ?)`, now, now, now, now,
	).Error)

	snapshot, err := loadMenuAIDocumentSnapshot(t.Context(), db, "menu-1", "en", false)
	require.NoError(t, err)
	require.True(t, snapshot.LocaleExists)
	require.Contains(t, snapshot.Labels, "home")
	require.Empty(t, snapshot.Labels["home"])
	require.NotContains(t, snapshot.Labels, "about", "missing target label must not borrow source content")
	require.Equal(t, revision, snapshot.DocumentRevision)
	require.NotNil(t, snapshot.TargetRevision)
}

func TestLoadMenuAIDocumentSnapshotUsesStoredSourceValuesWithoutRootFallback(t *testing.T) {
	db := openMenuAIDocumentStateTestDB(t)
	now := time.Now().UTC()
	seedMenuAIDocumentStateTestRoot(
		t, db, "menu-1", "en",
		[]byte(`[{"id":"home","label":"루트 한글","linkType":"custom","url":"/"},{"id":"about","label":"루트 소개","linkType":"custom","url":"/about"}]`),
		now,
	)
	require.NoError(t, db.Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, created_at, updated_at)
		 VALUES ('menu-1', 'ko', '[{"id":"home","label":"루트 한글"},{"id":"about","label":"루트 소개"}]', ?, ?),
		        ('menu-1', 'en', '[{"id":"home","label":""}]', ?, ?)`, now, now, now, now,
	).Error)
	snapshot, err := loadMenuAIDocumentSnapshot(t.Context(), db, "menu-1", "en", false)
	require.NoError(t, err)
	require.True(t, snapshot.SourceValuesStored)
	require.Contains(t, snapshot.Labels, "home")
	require.Empty(t, snapshot.Labels["home"], "explicit empty source value must remain present")
	require.NotContains(t, snapshot.Labels, "about", "missing source value must not borrow the root label")

	compiled, issues := (&MenuService{}).compileAIDocument(snapshot, []AIDocumentOperation{{
		Kind: AIDocumentSetItemField, ItemID: "about", Field: "label",
		Value: AIDocumentValue{Kind: AIDocumentText, Text: "About"},
	}})
	require.Empty(t, issues)
	require.True(t, compiled.sourceValuesChanged)
	require.False(t, compiled.sourceChanged)
	require.Equal(t, "About", compiled.snapshot.Labels["about"])
	require.Equal(t, "루트 소개", compiled.snapshot.Items[1].Label, "source locale edit must not rewrite the root label")

	require.NoError(t, persistMenuAIDocumentSourceValues(t.Context(), db, snapshot, compiled.snapshot, now))
	var stored struct {
		ItemsJSON string `gorm:"column:items_json"`
	}
	require.NoError(t, db.Table("menu_translation").Select("items_json").
		Where("entity_id = 'menu-1' AND locale = 'en'").Take(&stored).Error)
	require.JSONEq(t, `[{"id":"about","label":"About"},{"id":"home","label":""}]`, stored.ItemsJSON)
}

func TestRootSourceLocaleWithExplicitEmptyValuesDoesNotBorrowRootLabels(t *testing.T) {
	db := openMenuAIDocumentStateTestDB(t)
	now := time.Now().UTC()
	seedMenuAIDocumentStateTestRoot(
		t, db, "menu-1", "en", []byte(`[{"id":"home","label":"루트 한글"}]`), now,
	)
	require.NoError(t, db.Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, created_at, updated_at)
		 VALUES ('menu-1', 'en', '[]', ?, ?)`, now, now,
	).Error)

	snapshot, err := loadMenuAIDocumentSnapshot(t.Context(), db, "menu-1", "en", false)
	require.NoError(t, err)
	require.True(t, snapshot.SourceValuesStored)
	require.Empty(t, snapshot.Labels)
	require.Equal(t, "루트 한글", snapshot.Items[0].Label, "root remains structure storage, not source label fallback")

	document, err := LoadTranslationSourceDocument(t.Context(), db, "menu-1")
	require.NoError(t, err)
	var sourceItems []model.MenuItem
	require.NoError(t, json.Unmarshal(document.ContentJSON, &sourceItems))
	require.Empty(t, sourceItems[0].Label)
}

func TestLoadMenuAIDocumentSnapshotFailsClosedForMissingCurrentSourceValues(t *testing.T) {
	db := openMenuAIDocumentStateTestDB(t)
	now := time.Now().UTC()
	seedMenuAIDocumentStateTestRoot(
		t, db, "menu-1", "ko", []byte(`[{"id":"home","label":"root is not source fallback"}]`), now,
	)

	_, err := loadMenuAIDocumentSnapshot(t.Context(), db, "menu-1", "ko", false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "source locale values are not initialized")
}

func TestMenuAIDocumentTargetRevisionBindsDocumentAndExactLocaleFacts(t *testing.T) {
	firstTime := time.Unix(100, 0).UTC()
	secondTime := firstTime.Add(time.Microsecond)
	base := uuid.NewString()
	nextDocument := uuid.NewString()
	firstTarget, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{LocaleExists: true, DocumentRevision: base, LocaleUpdatedAt: &firstTime})
	require.NoError(t, err)
	secondTarget, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{LocaleExists: true, DocumentRevision: base, LocaleUpdatedAt: &secondTime})
	require.NoError(t, err)
	require.NotEqual(t, firstTarget, secondTarget)
	invalidatedTarget, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{LocaleExists: true, DocumentRevision: nextDocument, LocaleUpdatedAt: &firstTime})
	require.NoError(t, err)
	require.NotEqual(t, firstTarget, invalidatedTarget)
}

func TestPersistMenuAIDocumentTargetUsesValuesOnlyCRUDAndAdvancesRevisionFact(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE menu_translation (
		entity_id TEXT, locale TEXT, items_json BLOB, created_at DATETIME, updated_at DATETIME,
		PRIMARY KEY (entity_id, locale)
	)`).Error)
	current := time.Unix(100, 0).UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, created_at, updated_at)
		 VALUES ('menu-1', 'en', '[]', ?, ?)`, current, current,
	).Error)
	previous := AIDocumentSnapshot{
		ID: "menu-1", Locale: "en", LocaleExists: true,
		Items: []AIDocumentItem{{ID: "z-last"}, {ID: "a-empty"}},
	}
	next := previous
	next.Labels = map[string]string{"z-last": "Last", "a-empty": ""}
	require.NoError(t, persistMenuAIDocumentTarget(t.Context(), db, previous, next, false, current))

	var stored struct {
		ItemsJSON []byte    `gorm:"column:items_json"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	require.NoError(t, db.Table("menu_translation").Select("items_json, updated_at").
		Where("entity_id = 'menu-1' AND locale = 'en'").Take(&stored).Error)
	require.True(t, stored.UpdatedAt.After(current))
	var values []map[string]any
	require.NoError(t, json.Unmarshal(stored.ItemsJSON, &values))
	require.Equal(t, "a-empty", values[0]["id"], "values-only rows use deterministic stable-ID order")
	require.Contains(t, values[0], "label")
	require.Equal(t, "", values[0]["label"])
	require.NotContains(t, values[0], "linkType", "target rows cannot own source structure")

	require.NoError(t, persistMenuAIDocumentTarget(t.Context(), db, next, AIDocumentSnapshot{}, true, current))
	var count int64
	require.NoError(t, db.Table("menu_translation").Where("entity_id = 'menu-1' AND locale = 'en'").Count(&count).Error)
	require.Zero(t, count)
}

func TestMenuAIDocumentTargetMutationIsLocaleExactAndSourceInvalidatesEveryTarget(t *testing.T) {
	db := openMenuAIDocumentStateTestDB(t)
	base := time.Date(2026, 8, 23, 6, 0, 0, 0, time.UTC)
	documentID, _ := seedMenuAIDocumentStateTestRoot(
		t, db, "menu-1", "ko",
		[]byte(`[{"id":"home","label":"홈","linkType":"custom","url":"/"}]`),
		base,
	)
	for _, locale := range []struct {
		code   string
		labels string
		time   time.Time
	}{
		{code: "ko", labels: `[{"id":"home","label":"홈"}]`, time: base},
		{code: "en", labels: `[{"id":"home","label":"Home"}]`, time: base.Add(time.Second)},
		{code: "ja", labels: `[{"id":"home","label":"ホーム"}]`, time: base.Add(2 * time.Second)},
	} {
		require.NoError(t, db.Exec(
			`INSERT INTO menu_translation (entity_id, locale, items_json, created_at, updated_at) VALUES ('menu-1', ?, ?, ?, ?)`,
			locale.code, locale.labels, locale.time, locale.time,
		).Error)
	}

	service := &MenuService{}
	enBefore, err := loadMenuAIDocumentSnapshot(t.Context(), db, "menu-1", "en", false)
	require.NoError(t, err)
	jaBefore, err := loadMenuAIDocumentSnapshot(t.Context(), db, "menu-1", "ja", false)
	require.NoError(t, err)
	require.Equal(t, enBefore.DocumentRevision, jaBefore.DocumentRevision)

	compiledTarget, issues := service.compileAIDocument(enBefore, []AIDocumentOperation{{
		Kind: AIDocumentSetItemField, ItemID: "home", Field: "label",
		Value: AIDocumentValue{Kind: AIDocumentText, Text: "Start"},
	}})
	require.Empty(t, issues)
	require.NoError(t, persistMenuAIDocumentTarget(
		t.Context(), db, enBefore, compiledTarget.snapshot, false, base.Add(time.Minute),
	))
	enUpdated, err := loadMenuAIDocumentSnapshot(t.Context(), db, "menu-1", "en", false)
	require.NoError(t, err)
	require.Equal(t, enBefore.DocumentRevision, enUpdated.DocumentRevision)
	require.NotEqual(t, *enBefore.TargetRevision, *enUpdated.TargetRevision)
	jaAfterTargetWrite, err := loadMenuAIDocumentSnapshot(t.Context(), db, "menu-1", "ja", false)
	require.NoError(t, err)
	require.Equal(t, *jaBefore.TargetRevision, *jaAfterTargetWrite.TargetRevision)

	var targetTimesBefore []time.Time
	require.NoError(t, db.Table("menu_translation").Where("entity_id = 'menu-1' AND locale IN ?", []string{"en", "ja"}).
		Order("locale").Pluck("updated_at", &targetTimesBefore).Error)
	koBefore, err := loadMenuAIDocumentSnapshot(t.Context(), db, "menu-1", "ko", false)
	require.NoError(t, err)
	compiledSource, issues := service.compileAIDocument(koBefore, []AIDocumentOperation{{
		Kind: AIDocumentSetItemField, ItemID: "home", Field: "label",
		Value: AIDocumentValue{Kind: AIDocumentText, Text: "첫 화면"},
	}})
	require.Empty(t, issues)
	require.True(t, compiledSource.sourceValuesChanged)
	require.NoError(t, persistMenuAIDocumentSourceValues(
		t.Context(), db, koBefore, compiledSource.snapshot, base.Add(2*time.Minute),
	))
	expectedRevision, err := uuid.Parse(koBefore.DocumentRevision)
	require.NoError(t, err)
	_, err = advanceMenuContentDocument(
		t.Context(), db, "menu-1", uuid.MustParse(documentID), expectedRevision, base.Add(2*time.Minute),
	)
	require.NoError(t, err)
	enAfterSourceWrite, err := loadMenuAIDocumentSnapshot(t.Context(), db, "menu-1", "en", false)
	require.NoError(t, err)
	jaAfterSourceWrite, err := loadMenuAIDocumentSnapshot(t.Context(), db, "menu-1", "ja", false)
	require.NoError(t, err)
	require.NotEqual(t, koBefore.DocumentRevision, enAfterSourceWrite.DocumentRevision)
	require.Equal(t, enAfterSourceWrite.DocumentRevision, jaAfterSourceWrite.DocumentRevision)
	require.NotEqual(t, *enUpdated.TargetRevision, *enAfterSourceWrite.TargetRevision)
	require.NotEqual(t, *jaAfterTargetWrite.TargetRevision, *jaAfterSourceWrite.TargetRevision)
	var targetTimesAfter []time.Time
	require.NoError(t, db.Table("menu_translation").Where("entity_id = 'menu-1' AND locale IN ?", []string{"en", "ja"}).
		Order("locale").Pluck("updated_at", &targetTimesAfter).Error)
	require.Equal(t, targetTimesBefore, targetTimesAfter, "source mutation must invalidate target tokens without rewriting target rows")
}

func stringTestPointer(value string) *string { return &value }

func openMenuAIDocumentStateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE content_document (
			id TEXT PRIMARY KEY, profile TEXT NOT NULL, revision TEXT NOT NULL,
			created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE menu (
			id TEXT PRIMARY KEY, name TEXT, items BLOB, source_locale TEXT NOT NULL,
			content_document_id TEXT NOT NULL, created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE menu_translation (
			entity_id TEXT, locale TEXT, items_json BLOB, created_at DATETIME, updated_at DATETIME,
			PRIMARY KEY (entity_id, locale)
		)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	return db
}

func seedMenuAIDocumentStateTestRoot(
	t *testing.T,
	db *gorm.DB,
	menuID string,
	sourceLocale string,
	items []byte,
	now time.Time,
) (string, string) {
	t.Helper()
	documentID, revision := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		documentID, menuContentDocumentProfile, revision, now, now,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO menu (
			id, name, items, source_locale, content_document_id, created_at, updated_at
		) VALUES (?, 'Main', ?, ?, ?, ?, ?)`,
		menuID, items, sourceLocale, documentID, now, now,
	).Error)
	return documentID, revision
}
