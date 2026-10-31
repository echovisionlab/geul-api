package menu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
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

func prepareMenuUpdate(command UpdateMenuCommand, now time.Time) (menuUpdate, error) {
	update := menuUpdate{now: now}
	if command.Name != nil {
		if err := validateMenuName(*command.Name); err != nil {
			return update, err
		}
		name := strings.TrimSpace(*command.Name)
		update.name = &name
	}
	if command.Items == nil {
		return update, nil
	}
	if err := validateMenuItems(*command.Items, 0); err != nil {
		return update, err
	}
	items, err := protoItemsToJSON(*command.Items)
	if err != nil {
		return update, domainerr.New(domainerr.Invalid, "", fmt.Sprintf("failed to convert menu items: %s", err.Error()))
	}
	update.items = items
	return update, nil
}

// menuUpdated is the outcome of an update: the stored Menu and the audit
// fields that changed, none when the update matched the stored Menu.
type menuUpdated struct {
	menu          model.Menu
	changedFields []string
}

func (s *Service) UpdateMenu(ctx context.Context, command UpdateMenuCommand) (_ *managev1.Menu, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("menu_id", command.ID))

	menuID := strings.TrimSpace(command.ID)
	if menuID == "" {
		return nil, domainerr.Required("id")
	}
	update, err := prepareMenuUpdate(command, time.Now())
	if err != nil {
		return nil, err
	}
	var targets []TargetReference
	if command.Items != nil {
		targets = collectMenuTargetReferences(*command.Items)
	}
	var locked *model.Menu
	updated, err := mutation.Run(ctx, s.mutations, mutation.Spec[menuUpdated]{
		// Target locks precede the Menu root throughout direct writes and
		// target lifecycle rewrites; the principal fence comes last.
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
			var targetErr error
			if command.Items != nil {
				targetErr = s.targets.ValidateAndLock(ctx, tx, targets)
			}
			var err error
			if locked, err = lockMenuForUpdate(ctx, tx, menuID); err != nil {
				return err
			}
			action := menuUpdateAction(ctx, *locked, command.Items != nil, update)
			if err := requireFreshMenuPermission(ctx, tx, require, menuID, action); err != nil {
				return err
			}
			return targetErr
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (menuUpdated, error) {
			return applyMenuUpdate(ctx, tx, *locked, command.Name, update)
		},
		Audit: func(updated menuUpdated) mutation.Audit {
			if len(updated.changedFields) == 0 {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditMenuUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMenuSourceUpdatedAuditRecord(metadata, updated.menu.ID, updated.changedFields)
			})
		},
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("menu", menuID)
		}
		return nil, fmt.Errorf("failed to update menu: %w", err)
	}
	protoMenu, err := toProtoMenu(&updated.menu)
	if err != nil {
		return nil, fmt.Errorf("failed to convert menu: %w", err)
	}
	return protoMenu, nil
}

// applyMenuUpdate writes the part of update that changes the locked Menu
// and advances its content document.
func applyMenuUpdate(ctx context.Context, tx *gorm.DB, locked model.Menu, name *string, update menuUpdate) (menuUpdated, error) {
	change, changedFields, err := lockedMenuChange(tx, locked, name, update)
	if err != nil || len(changedFields) == 0 {
		return menuUpdated{menu: locked}, err
	}
	document, err := loadMenuContentDocumentStateFromRoot(ctx, tx, locked, true)
	if err != nil {
		return menuUpdated{}, err
	}
	sort.Strings(changedFields)
	if err := updateMenuRow(tx, locked.ID, name, change); err != nil {
		return menuUpdated{}, err
	}
	var menu model.Menu
	if err := tx.First(&menu, "id = ?", locked.ID).Error; err != nil {
		return menuUpdated{}, fmt.Errorf("failed to reload menu: %w", err)
	}
	if change.items != nil {
		if err := syncMenuSourceItemValues(ctx, tx, menu, update.now); err != nil {
			return menuUpdated{}, err
		}
	}
	if err := advanceMenuContentDocument(ctx, tx, menu.ID, document.ID, document.Revision, update.now); err != nil {
		return menuUpdated{}, err
	}
	return menuUpdated{menu: menu, changedFields: changedFields}, nil
}

// lockedMenuChange compares update with the locked menu and returns the part
// that changes it, with the changed audit fields.
func lockedMenuChange(
	tx *gorm.DB,
	menu model.Menu,
	name *string,
	update menuUpdate,
) (menuUpdate, []string, error) {
	change := menuUpdate{now: update.now}
	changedFields := make([]string, 0, 2)
	if update.name != nil && *update.name != menu.Name {
		if err := ensureUpdatedMenuNameAvailable(tx, menu.ID, name); err != nil {
			return change, nil, err
		}
		change.name = update.name
		changedFields = append(changedFields, "name")
	}
	if update.items != nil && !semanticMenuItemsEqual(menu.Items, update.items) {
		change.items = update.items
		changedFields = append(changedFields, "items")
	}
	return change, changedFields, nil
}

// syncMenuSourceItemValues brings the source label values and translation rows
// in line with the items just written to menu.
func syncMenuSourceItemValues(ctx context.Context, tx *gorm.DB, menu model.Menu, now time.Time) error {
	nextItems, err := decodeSourceItems(menu.Items)
	if err != nil {
		return err
	}
	if err := SyncCurrentSourceLabelValues(ctx, tx, menu.ID, nextItems, now); err != nil {
		return err
	}
	return ReconcileTranslationRowsWithSourceJSON(ctx, tx, menu.ID, menu.Items, now)
}

func menuUpdateAction(
	ctx context.Context,
	current model.Menu,
	replacesItems bool,
	update menuUpdate,
) menuAction {
	if !replacesItems {
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
		return fmt.Errorf("failed to check menu name uniqueness: %w", err)
	}
	if existingCount > 0 {
		return domainerr.Duplicate("menu", "name", normalized)
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
			return domainerr.Duplicate("menu", "name", strings.TrimSpace(*name))
		}
		return fmt.Errorf("failed to update menu: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
