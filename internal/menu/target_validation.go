package menu

import (
	"strings"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func collectMenuTargetReferences(items []*managev1.MenuItem) []TargetReference {
	refs := make([]TargetReference, 0)
	for _, item := range items {
		if item == nil {
			continue
		}
		switch item.LinkType {
		case managev1.MenuLinkType_MENU_LINK_TYPE_PAGE,
			managev1.MenuLinkType_MENU_LINK_TYPE_CATEGORY,
			managev1.MenuLinkType_MENU_LINK_TYPE_TAG,
			managev1.MenuLinkType_MENU_LINK_TYPE_SERIES:
			ref := TargetReference{LinkType: item.LinkType}
			if item.TargetId != nil {
				ref.ID = strings.TrimSpace(*item.TargetId)
			}
			if ref.ID == "" && item.TargetSlug != nil {
				ref.Slug = strings.TrimSpace(*item.TargetSlug)
			}
			if ref.ID != "" || ref.Slug != "" {
				refs = append(refs, ref)
			}
		}
		refs = append(refs, collectMenuTargetReferences(item.Children)...)
	}
	return refs
}
