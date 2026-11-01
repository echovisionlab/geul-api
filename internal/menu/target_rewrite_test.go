package menu_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/menu"
)

func TestTargetSlugRewriteKeepsTargetLabelsAndAdvancesTheDocument(t *testing.T) {
	f := newMenuFixture(t)
	revision := uuid.New()
	targetUpdatedAt := time.Unix(100, 0).UTC()
	rewritten := f.store.Seed(menu.Root{
		Name: "Rewritten", SourceLocale: "en",
		ItemsJSON: []byte(`[{"id":"category-item","label":"","linkType":"category","targetId":"category-1","targetSlug":"news"}]`),
	}, revision, menu.LocaleValues{Locale: "fr", Labels: map[string]string{"category-item": ""}, UpdatedAt: targetUpdatedAt})
	untouched := f.store.Seed(menu.Root{Name: "Untouched", SourceLocale: "en", ItemsJSON: []byte(`[{"id":"home","label":"Home","linkType":"custom","url":"/"}]`)}, uuid.New())
	untouchedBefore, _ := f.store.Stored(untouched.ID)
	audit := &audittest.Recorder{}

	require.NoError(t, menu.UpdateTargetSlug(f.ctx, f.joined(audit), "category", "category-1", "news", "news-updated"))

	root, _ := f.store.Stored(rewritten.ID)
	require.JSONEq(t, `[{"id":"category-item","label":"","linkType":"category","targetId":"category-1","targetSlug":"news-updated"}]`, string(root.ItemsJSON))
	target, _ := f.store.StoredLocale(rewritten.ID, "fr")
	require.True(t, target.UpdatedAt.Equal(targetUpdatedAt), "a slug rewrite keeps the target revision")
	require.NotEqual(t, revision.String(), f.revision(t, rewritten.ID), "a source-owned rewrite advances the shared document revision")
	untouchedAfter, _ := f.store.Stored(untouched.ID)
	require.Equal(t, untouchedBefore, untouchedAfter)
	records := audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, rewritten.ID, records[0].TargetID)
	require.Equal(t, []string{"items"}, records[0].ChangedFields)
}

func TestTargetRemovalPrunesOnlyTheRemovedSubtreeLabels(t *testing.T) {
	f := newMenuFixture(t)
	previous := time.Unix(100, 0).UTC()
	root := f.store.Seed(menu.Root{
		Name: "Main", SourceLocale: "en",
		ItemsJSON: []byte(`[
			{"id":"keep","label":"Keep","linkType":"custom","url":"/keep"},
			{"id":"remove","label":"Remove","linkType":"category","targetId":"category-1","children":[
				{"id":"remove-child","label":"Child","linkType":"custom","url":"/child"}
			]}
		]`),
	}, uuid.New(), menu.LocaleValues{Locale: "fr", Labels: map[string]string{"keep": "", "remove": "Supprimer", "remove-child": "Enfant"}, UpdatedAt: previous})

	require.NoError(t, menu.RemoveTarget(t.Context(), menu.Joined{Store: f.store}, "category", "category-1", ""), "an unaudited caller joins without an appender")

	stored, _ := f.store.Stored(root.ID)
	require.JSONEq(t, `[{"id":"keep","label":"Keep","linkType":"custom","url":"/keep"}]`, string(stored.ItemsJSON))
	target, _ := f.store.StoredLocale(root.ID, "fr")
	require.Equal(t, map[string]string{"keep": ""}, target.Labels)
	require.True(t, target.UpdatedAt.After(previous), "an actual prune advances the target revision")
}

func TestTargetRewriteFailsOnAnUndecodableMenu(t *testing.T) {
	f := newMenuFixture(t)
	f.store.Seed(menu.Root{Name: "Broken", SourceLocale: "en", ItemsJSON: []byte(`{"broken"`)}, uuid.New())

	require.Error(t, menu.UpdateTargetSlug(t.Context(), menu.Joined{Store: f.store}, "category", "category-1", "news", "news-updated"))
	require.NoError(t, menu.UpdateTargetSlug(t.Context(), menu.Joined{Store: f.store}, "category", "", "", "news-updated"), "a reference without identity rewrites nothing")
}
