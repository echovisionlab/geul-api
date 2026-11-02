package menu

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMenuTargetRewriteUpdatesAndRemovesNestedReferences(t *testing.T) {
	categoryID := "category-1"
	tagID := "tag-1"
	oldCategorySlug := "news"
	oldTagSlug := "ambient"
	items := []Item{
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
			Children: []Item{
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
