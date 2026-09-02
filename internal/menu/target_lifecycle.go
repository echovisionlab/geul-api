package menu

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type menuTargetReference struct {
	linkType string
	id       string
	slug     string
}

// TargetLifecycle keeps Menu targets consistent with changes in owning domains.
type TargetLifecycle struct {
	auditWriter domainaudit.Appender
}

func NewTargetLifecycle(auditWriter domainaudit.Appender) TargetLifecycle {
	return TargetLifecycle{auditWriter: auditWriter}
}

// UpdateSlug rewrites Menu targets after an owning domain changes a slug.
func (l TargetLifecycle) UpdateSlug(
	ctx context.Context,
	tx *gorm.DB,
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

	return l.rewriteMenusForTarget(ctx, tx, func(items []model.MenuItem) ([]model.MenuItem, bool) {
		return rewriteMenuTargetSlug(items, ref, nextSlug)
	})
}

// Remove removes Menu targets after an owning domain deletes a target.
func (l TargetLifecycle) Remove(
	ctx context.Context,
	tx *gorm.DB,
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

	return l.rewriteMenusForTarget(ctx, tx, func(items []model.MenuItem) ([]model.MenuItem, bool) {
		return removeMenuTargetItems(items, ref)
	})
}

func (l TargetLifecycle) rewriteMenusForTarget(
	ctx context.Context,
	tx *gorm.DB,
	rewrite func([]model.MenuItem) ([]model.MenuItem, bool),
) error {
	now := time.Now().UTC()
	var menus []model.Menu
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Order("id ASC").
		Find(&menus).Error; err != nil {
		return errs.Internal(err)
	}

	for index := range menus {
		var items []model.MenuItem
		if len(menus[index].Items) > 0 {
			if err := json.Unmarshal(menus[index].Items, &items); err != nil {
				// Target rename/delete and every affected menu are one product
				// transaction. Silently skipping malformed source JSON would commit
				// a dangling menu reference with no recovery authority.
				return errs.Internal(err)
			}
		}

		nextItems, changed := rewrite(items)
		if !changed {
			continue
		}
		nextJSON, err := json.Marshal(nextItems)
		if err != nil {
			return errs.Internal(err)
		}
		document, err := loadMenuContentDocumentStateFromRoot(ctx, tx, menus[index], true)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Model(&model.Menu{}).
			Where("id = ?", menus[index].ID).
			Updates(structured.Fields{
				"items":      nextJSON,
				"updated_at": now,
			}).Error; err != nil {
			return errs.Internal(err)
		}
		if err := ReconcileTranslationRowsWithSourceItems(ctx, tx, menus[index].ID, nextItems, now); err != nil {
			return err
		}
		if _, err := advanceMenuContentDocument(
			ctx, tx, menus[index].ID, document.ID, document.Revision, now,
		); err != nil {
			return err
		}
		if err := domainaudit.AppendOptionalRequest(
			ctx,
			tx,
			l.auditWriter,
			sharedtelemetry.AuditMenuUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMenuSourceUpdatedAuditRecord(metadata, menus[index].ID, []string{"items"})
			},
		); err != nil {
			return err
		}
	}

	return nil
}

func rewriteMenuTargetSlug(
	items []model.MenuItem,
	ref menuTargetReference,
	nextSlug string,
) ([]model.MenuItem, bool) {
	changed := false
	next := make([]model.MenuItem, len(items))
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

func removeMenuTargetItems(items []model.MenuItem, ref menuTargetReference) ([]model.MenuItem, bool) {
	changed := false
	next := make([]model.MenuItem, 0, len(items))
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

func menuItemReferencesTarget(item *model.MenuItem, ref menuTargetReference) bool {
	if item == nil || strings.TrimSpace(item.LinkType) != ref.linkType {
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
