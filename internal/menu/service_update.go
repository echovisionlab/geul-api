package menu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// menuUpdate is a validated Menu update. name and items are nil when the
// request leaves them alone or, once compared with the locked row, when they
// are unchanged.
type menuUpdate struct {
	name  *string
	items json.RawMessage
	now   time.Time
}

func (s *MenuService) prepareMenuUpdate(request *managev1.UpdateMenuRequest, now time.Time) (menuUpdate, error) {
	update := menuUpdate{now: now}
	if request.Name != nil {
		if err := s.validateMenuName(*request.Name); err != nil {
			return update, err
		}
		name := strings.TrimSpace(*request.Name)
		update.name = &name
	}
	if request.Items == nil {
		return update, nil
	}
	if err := s.validateMenuItems(request.Items.Items, 0); err != nil {
		return update, err
	}
	items, err := s.protoItemsToJSON(request.Items.Items)
	if err != nil {
		return update, errs.InvalidArgumentMsg(fmt.Sprintf("failed to convert menu items: %s", err.Error()))
	}
	update.items = items
	return update, nil
}

func (s *MenuService) applyMenuUpdate(
	ctx context.Context,
	menuID string,
	request *managev1.UpdateMenuRequest,
	update menuUpdate,
) (*model.Menu, bool, error) {
	var menu model.Menu
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Target locks precede the Menu root throughout direct writes and target
		// lifecycle rewrites. Preserve that global order before the final
		// principal/permission fence below.
		var targetErr error
		if request.Items != nil {
			targetErr = s.targets.ValidateAndLock(ctx, tx, collectMenuTargetReferences(request.Items.Items))
		}
		locked, err := lockMenuForUpdate(ctx, tx, menuID)
		if err != nil {
			return err
		}
		action := menuUpdateAction(ctx, *locked, request, update)
		if err := requireFreshMenuPermission(ctx, tx, s.permissions, menuID, action); err != nil {
			return err
		}
		if targetErr != nil {
			return targetErr
		}
		menu = *locked
		change := menuUpdate{now: update.now}
		changedFields := make([]string, 0, 2)
		if update.name != nil && *update.name != menu.Name {
			if err := ensureUpdatedMenuNameAvailable(tx, menuID, request.Name); err != nil {
				return err
			}
			change.name = update.name
			changedFields = append(changedFields, "name")
		}
		if update.items != nil && !semanticMenuItemsEqual(menu.Items, update.items) {
			change.items = update.items
			changedFields = append(changedFields, "items")
		}
		if len(changedFields) == 0 {
			return nil
		}
		document, err := loadMenuContentDocumentStateFromRoot(ctx, tx, *locked, true)
		if err != nil {
			return err
		}
		sort.Strings(changedFields)
		if err := updateMenuRow(tx, menuID, request.Name, change); err != nil {
			return err
		}
		if err := tx.First(&menu, "id = ?", menuID).Error; err != nil {
			return errs.Internal(fmt.Errorf("failed to reload menu: %w", err))
		}
		if change.items != nil {
			nextItems, err := decodeSourceItems(menu.Items)
			if err != nil {
				return err
			}
			if err := SyncCurrentSourceLabelValues(ctx, tx, menu.ID, nextItems, update.now); err != nil {
				return err
			}
			if err := ReconcileTranslationRowsWithSourceJSON(
				ctx, tx, menu.ID, menu.Items, update.now,
			); err != nil {
				return err
			}
		}
		if err := advanceMenuContentDocument(
			ctx, tx, menu.ID, document.ID, document.Revision, update.now,
		); err != nil {
			return err
		}
		if err := domainaudit.AppendOptionalRequest(
			ctx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditMenuUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMenuSourceUpdatedAuditRecord(metadata, menu.ID, changedFields)
			},
		); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return &menu, changed, err
}

func menuUpdateAction(
	ctx context.Context,
	current model.Menu,
	request *managev1.UpdateMenuRequest,
	update menuUpdate,
) menuAction {
	if request.Items == nil {
		return policyv1.Menu.Edit
	}
	if update.items == nil || menuItemTopologyEqual(ctx, current.ID, current.Items, update.items) {
		return policyv1.Menu.Edit
	}
	return policyv1.Menu.Manage
}

func menuItemTopologyEqual(ctx context.Context, menuID string, stored, requested []byte) bool {
	var storedItems []model.MenuItem
	if len(stored) != 0 {
		if err := json.Unmarshal(stored, &storedItems); err != nil {
			slog.ErrorContext(ctx, "Stored Menu items are not valid JSON; requiring Manage permission", "domain", "menu", "operation", "compare_stored_topology", "outcome", "failed", "menu_id", menuID, failurecause.Attr(err))
			return false
		}
	}
	var requestedItems []model.MenuItem
	if len(requested) != 0 {
		if err := json.Unmarshal(requested, &requestedItems); err != nil {
			slog.WarnContext(ctx, "Encoded Menu request items are not valid JSON; requiring Manage permission", "domain", "menu", "operation", "compare_requested_topology", "outcome", "failed", "menu_id", menuID, failurecause.Attr(err))
			return false
		}
	}
	return reflectMenuItemTopology(storedItems, "") == reflectMenuItemTopology(requestedItems, "")
}

func reflectMenuItemTopology(items []model.MenuItem, parentID string) string {
	var topology strings.Builder
	for index := range items {
		topology.WriteString(parentID)
		topology.WriteByte('/')
		topology.WriteString(items[index].ID)
		topology.WriteByte('@')
		fmt.Fprintf(&topology, "%d;", index)
		topology.WriteString(reflectMenuItemTopology(items[index].Children, items[index].ID))
	}
	return topology.String()
}

func ensureUpdatedMenuNameAvailable(tx *gorm.DB, menuID string, name *string) error {
	if name == nil {
		return nil
	}
	normalized := strings.TrimSpace(*name)
	var existingCount int64
	if err := tx.Model(&model.Menu{}).Where("name = ? AND id != ?", normalized, menuID).Count(&existingCount).Error; err != nil {
		return errs.Internal(fmt.Errorf("failed to check menu name uniqueness: %w", err))
	}
	if existingCount > 0 {
		return errs.AlreadyExists("menu", "name", normalized)
	}
	return nil
}

// updateMenuRow writes the changed name and items of change. A unique
// violation is reported against the requested name.
func updateMenuRow(tx *gorm.DB, menuID string, name *string, change menuUpdate) error {
	columns := structured.Fields{"updated_at": change.now}
	if change.name != nil {
		columns["name"] = *change.name
	}
	if change.items != nil {
		columns["items"] = change.items
	}
	result := tx.Model(&model.Menu{}).Where("id = ?", menuID).Updates(columns)
	if result.Error != nil {
		if dberrors.IsUniqueViolation(result.Error) && name != nil {
			return errs.AlreadyExists("menu", "name", strings.TrimSpace(*name))
		}
		return errs.Internal(fmt.Errorf("failed to update menu: %w", result.Error))
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
