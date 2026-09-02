package menu

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMenuTargetRewriteUpdatesAndRemovesNestedReferences(t *testing.T) {
	categoryID := "category-1"
	tagID := "tag-1"
	oldCategorySlug := "news"
	oldTagSlug := "ambient"
	items := []model.MenuItem{
		{
			ID:         "category",
			Label:      "News",
			LinkType:   "category",
			TargetID:   &categoryID,
			TargetSlug: &oldCategorySlug,
		},
		{
			ID:       "parent",
			Label:    "Topics",
			LinkType: "custom",
			Children: []model.MenuItem{
				{
					ID:         "tag",
					Label:      "Ambient",
					LinkType:   "tag",
					TargetID:   &tagID,
					TargetSlug: &oldTagSlug,
				},
			},
		},
	}

	updated, changed := rewriteMenuTargetSlug(
		items,
		menuTargetReference{linkType: "category", id: categoryID, slug: oldCategorySlug},
		"news-updated",
	)
	require.True(t, changed)
	require.Equal(t, "news-updated", *updated[0].TargetSlug)

	pruned, changed := removeMenuTargetItems(
		updated,
		menuTargetReference{linkType: "tag", id: tagID, slug: oldTagSlug},
	)
	require.True(t, changed)
	require.Len(t, pruned, 2)
	require.Empty(t, pruned[1].Children)
}

func TestMenuTargetLifecycleRejectsMalformedSourceAtomically(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:menu-target-malformed?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE menu (id TEXT PRIMARY KEY, items BLOB)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO menu (id, items) VALUES (?, ?)`, "menu-1", []byte(`{"broken"`)).Error)

	err = NewTargetLifecycle(nil).UpdateSlug(
		context.Background(),
		db,
		"category",
		"category-1",
		"news",
		"news-updated",
	)
	require.Error(t, err)
}

func TestMenuTargetLifecyclePreservesTargetRevisionDuringRewrite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:menu-target-caller-transaction?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE content_document (id TEXT PRIMARY KEY, profile TEXT, revision TEXT, updated_at DATETIME);
		CREATE TABLE menu (
			id TEXT PRIMARY KEY, items BLOB, source_locale TEXT,
			content_document_id TEXT, updated_at DATETIME
		);
		CREATE TABLE menu_translation (entity_id TEXT, locale TEXT, items_json BLOB, updated_at DATETIME)
	`).Error)

	targetID := "category-1"
	oldSlug := "news"
	items, err := json.Marshal([]model.MenuItem{{
		ID:         "category-item",
		LinkType:   "category",
		TargetID:   &targetID,
		TargetSlug: &oldSlug,
	}})
	require.NoError(t, err)
	documentID, documentRevision := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision) VALUES (?, ?, ?)`,
		documentID, menuContentDocumentProfile, documentRevision,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO menu (id, items, source_locale, content_document_id) VALUES (?, ?, 'en', ?)`,
		"menu-1", items, documentID,
	).Error)
	targetUpdatedAt := time.Unix(100, 0).UTC()
	targetJSON := []byte(`[{"id":"category-item","label":""}]`)
	require.NoError(t, db.Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, updated_at) VALUES (?, ?, ?, ?)`,
		"menu-1", "fr", targetJSON, targetUpdatedAt,
	).Error)

	lifecycle := NewTargetLifecycle(nil)
	require.NoError(t, lifecycle.UpdateSlug(
		t.Context(), db, "category", targetID, oldSlug, "news-updated",
	))

	var stored []byte
	require.NoError(t, db.Raw(`SELECT items FROM menu WHERE id = ?`, "menu-1").Row().Scan(&stored))
	var updated []model.MenuItem
	require.NoError(t, json.Unmarshal(stored, &updated))
	require.NotNil(t, updated[0].TargetSlug)
	require.Equal(t, "news-updated", *updated[0].TargetSlug)
	var target struct {
		ItemsJSON []byte    `gorm:"column:items_json"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	require.NoError(t, db.Table("menu_translation").Select("items_json, updated_at").
		Where("entity_id = ? AND locale = ?", "menu-1", "fr").Take(&target).Error)
	require.Equal(t, targetJSON, target.ItemsJSON)
	require.True(t, target.UpdatedAt.Equal(targetUpdatedAt), "slug rewrite must preserve target revision")
	var nextDocumentRevision string
	require.NoError(t, db.Table("content_document").Select("revision").Where("id = ?", documentID).Scan(&nextDocumentRevision).Error)
	require.NotEqual(t, documentRevision, nextDocumentRevision, "source-owned target rewrite advances the shared document revision")
}

func TestMenuTargetLifecycleRemovalPrunesOnlyRemovedSubtreeLabels(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:menu-target-prune?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE content_document (id TEXT PRIMARY KEY, profile TEXT, revision TEXT, updated_at DATETIME);
		CREATE TABLE menu (
			id TEXT PRIMARY KEY, items BLOB, source_locale TEXT,
			content_document_id TEXT, updated_at DATETIME
		);
		CREATE TABLE menu_translation (entity_id TEXT, locale TEXT, items_json BLOB, updated_at DATETIME);
	`).Error)

	targetID := "category-1"
	items := []byte(`[
		{"id":"keep","label":"Keep","linkType":"custom","url":"/keep"},
		{"id":"remove","label":"Remove","linkType":"category","targetId":"category-1","children":[
			{"id":"remove-child","label":"Child","linkType":"custom","url":"/child"}
		]}
	]`)
	previous := time.Unix(100, 0).UTC()
	documentID, documentRevision := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision) VALUES (?, ?, ?)`,
		documentID, menuContentDocumentProfile, documentRevision,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO menu (id, items, source_locale, content_document_id) VALUES (?, ?, 'en', ?)`,
		"menu-1", items, documentID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, updated_at) VALUES (?, ?, ?, ?)`,
		"menu-1", "fr", []byte(`[
			{"id":"keep","label":""},
			{"id":"remove","label":"Supprimer"},
			{"id":"remove-child","label":"Enfant"}
		]`), previous,
	).Error)

	require.NoError(t, NewTargetLifecycle(nil).Remove(
		t.Context(), db, "category", targetID, "",
	))

	var row struct {
		ItemsJSON []byte    `gorm:"column:items_json"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	require.NoError(t, db.Table("menu_translation").Select("items_json, updated_at").
		Where("entity_id = ? AND locale = ?", "menu-1", "fr").Take(&row).Error)
	require.JSONEq(t, `[{"id":"keep","label":""}]`, string(row.ItemsJSON))
	require.True(t, row.UpdatedAt.After(previous), "actual prune advances the target revision")
	var nextDocumentRevision string
	require.NoError(t, db.Table("content_document").Select("revision").Where("id = ?", documentID).Scan(&nextDocumentRevision).Error)
	require.NotEqual(t, documentRevision, nextDocumentRevision)
}
