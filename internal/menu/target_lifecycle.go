package menu

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type menuTargetReference struct {
	linkType string
	id       string
	slug     string
}

// UpdateTargetSlug rewrites Menu targets in an owning domain's joined
// transaction after it changes a target's slug.
func UpdateTargetSlug(
	ctx context.Context,
	joined Joined,
	linkType string,
	targetID string,
	oldSlug string,
	nextSlug string,
) error {
	ref := menuTargetReference{
		linkType: strings.TrimSpace(linkType),
		id:       strings.TrimSpace(targetID),
		slug:     strings.TrimSpace(oldSlug),
	}
	nextSlug = strings.TrimSpace(nextSlug)
	if ref.linkType == "" || nextSlug == "" || (ref.id == "" && ref.slug == "") {
		return nil
	}

	return rewriteMenusForTarget(ctx, joined, func(items []Item) ([]Item, bool) {
		return rewriteMenuTargetSlug(items, ref, nextSlug)
	})
}

// RemoveTarget removes Menu targets in an owning domain's joined transaction
// after it deletes a target.
func RemoveTarget(
	ctx context.Context,
	joined Joined,
	linkType string,
	targetID string,
	targetSlug string,
) error {
	ref := menuTargetReference{
		linkType: strings.TrimSpace(linkType),
		id:       strings.TrimSpace(targetID),
		slug:     strings.TrimSpace(targetSlug),
	}
	if ref.linkType == "" || (ref.id == "" && ref.slug == "") {
		return nil
	}

	return rewriteMenusForTarget(ctx, joined, func(items []Item) ([]Item, bool) {
		return removeMenuTargetItems(items, ref)
	})
}

// rewriteMenusForTarget locks every Menu and rewrites the ones whose items
// reference the target. A joined transaction without an audit appender is
// an explicitly unaudited caller.
func rewriteMenusForTarget(
	ctx context.Context,
	joined Joined,
	rewrite func([]Item) ([]Item, bool),
) error {
	now := time.Now().UTC()
	roots, err := joined.Store.LockMenus(ctx)
	if err != nil {
		return err
	}
	for _, root := range roots {
		// Target rename/delete and every affected menu are one product
		// transaction. Silently skipping malformed source JSON would commit
		// a dangling menu reference with no recovery authority.
		items, err := decodeSourceItems(root.ItemsJSON)
		if err != nil {
			return err
		}
		nextItems, changed := rewrite(items)
		if !changed {
			continue
		}
		if err := rewriteMenuItems(ctx, joined.Store, root, nextItems, now); err != nil {
			return err
		}
		if err := domainaudit.AppendOptionalRequest(
			ctx, nil, joined.Audit, sharedtelemetry.AuditMenuUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMenuSourceUpdatedAuditRecord(metadata, root.ID, []string{"items"})
			},
		); err != nil {
			return err
		}
	}
	return nil
}

// rewriteMenuItems stores the rewritten items of root, prunes the labels of
// removed items and advances the content document.
func rewriteMenuItems(ctx context.Context, store Store, root Root, nextItems []Item, now time.Time) error {
	nextJSON, err := json.Marshal(nextItems)
	if err != nil {
		return err
	}
	document, err := loadMenuContentDocumentState(ctx, store, root, true)
	if err != nil {
		return err
	}
	if err := store.UpdateMenu(ctx, root.ID, RootChange{ItemsJSON: nextJSON, UpdatedAt: now}); err != nil {
		return err
	}
	if err := reconcileTranslationRowsWithSourceItems(ctx, store, root.ID, nextItems, now); err != nil {
		return err
	}
	return advanceMenuContentDocument(ctx, store, root.ID, document.ID, document.Revision, now)
}

func rewriteMenuTargetSlug(
	items []Item,
	ref menuTargetReference,
	nextSlug string,
) ([]Item, bool) {
	changed := false
	next := make([]Item, len(items))
	for index := range items {
		item := items[index]
		if menuItemReferencesTarget(&item, ref) {
			if item.TargetSlug == nil || strings.TrimSpace(*item.TargetSlug) != nextSlug {
				slug := nextSlug
				item.TargetSlug = &slug
				changed = true
			}
		}
		if len(item.Children) > 0 {
			children, childrenChanged := rewriteMenuTargetSlug(item.Children, ref, nextSlug)
			if childrenChanged {
				item.Children = children
				changed = true
			}
		}
		next[index] = item
	}
	return next, changed
}

func removeMenuTargetItems(items []Item, ref menuTargetReference) ([]Item, bool) {
	changed := false
	next := make([]Item, 0, len(items))
	for index := range items {
		item := items[index]
		if menuItemReferencesTarget(&item, ref) {
			changed = true
			continue
		}
		if len(item.Children) > 0 {
			children, childrenChanged := removeMenuTargetItems(item.Children, ref)
			if childrenChanged {
				item.Children = children
				changed = true
			}
		}
		next = append(next, item)
	}
	return next, changed
}

func menuItemReferencesTarget(item *Item, ref menuTargetReference) bool {
	if item == nil || strings.TrimSpace(string(item.LinkType)) != ref.linkType {
		return false
	}

	if item.TargetID != nil {
		targetID := strings.TrimSpace(*item.TargetID)
		return ref.id != "" && targetID == ref.id
	}

	if ref.slug == "" || item.TargetSlug == nil {
		return false
	}
	return strings.TrimSpace(*item.TargetSlug) == ref.slug
}
