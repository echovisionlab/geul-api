package menu

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// menuUpdate is a validated Menu update. name and items are nil when the
// request leaves them alone or, once compared with the locked row, when they
// are unchanged.
type menuUpdate struct {
	name  *string
	items []byte
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
	items, err := encodeRequestedItems(*command.Items)
	if err != nil {
		return update, err
	}
	update.items = items
	return update, nil
}

// menuUpdated is the outcome of an update: the stored Menu and the audit
// fields that changed, none when the update matched the stored Menu.
type menuUpdated struct {
	root          Root
	changedFields []string
}

func (s *Service) UpdateMenu(ctx context.Context, command UpdateMenuCommand) (_ Menu, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("menu_id", command.ID))

	menuID := strings.TrimSpace(command.ID)
	if menuID == "" {
		return Menu{}, domainerr.Required("id")
	}
	update, err := prepareMenuUpdate(command, time.Now())
	if err != nil {
		return Menu{}, err
	}
	var targets []TargetReference
	if command.Items != nil {
		targets = collectMenuTargetReferences(*command.Items)
	}
	var locked Root
	updated, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[menuUpdated, Store]{
		// Target locks precede the Menu root throughout direct writes and
		// target lifecycle rewrites; the principal fence comes last.
		Access: mutation.Locked(func(ctx context.Context, store Store, require mutation.Require) error {
			var targetErr error
			if command.Items != nil {
				targetErr = lockTargets(ctx, store, targets)
			}
			var err error
			if locked, err = store.LockMenu(ctx, menuID); err != nil {
				return err
			}
			action := menuUpdateAction(ctx, locked, command.Items != nil, update)
			if err := requireFreshMenuPermission(ctx, require, menuID, action); err != nil {
				return err
			}
			return targetErr
		}),
		Change: func(ctx context.Context, store Store) (menuUpdated, error) {
			return applyMenuUpdate(ctx, store, locked, command.Name, update)
		},
		Audit: func(updated menuUpdated) mutation.Audit {
			if len(updated.changedFields) == 0 {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditMenuUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMenuSourceUpdatedAuditRecord(metadata, updated.root.ID, updated.changedFields)
			})
		},
	})
	if err != nil {
		if errors.Is(err, ErrMenuNotFound) {
			return Menu{}, domainerr.ResourceNotFound("menu", menuID)
		}
		return Menu{}, fmt.Errorf("failed to update menu: %w", err)
	}
	menu, err := menuOf(updated.root)
	if err != nil {
		return Menu{}, fmt.Errorf("failed to convert menu: %w", err)
	}
	return menu, nil
}

// applyMenuUpdate writes the part of update that changes the locked Menu
// and advances its content document.
func applyMenuUpdate(ctx context.Context, store Store, locked Root, name *string, update menuUpdate) (menuUpdated, error) {
	change, changedFields, err := lockedMenuChange(ctx, store, locked, name, update)
	if err != nil || len(changedFields) == 0 {
		return menuUpdated{root: locked}, err
	}
	document, err := loadMenuContentDocumentState(ctx, store, locked, true)
	if err != nil {
		return menuUpdated{}, err
	}
	sort.Strings(changedFields)
	if err := updateMenuRow(ctx, store, locked.ID, name, change); err != nil {
		return menuUpdated{}, err
	}
	root, err := store.Menu(ctx, locked.ID)
	if err != nil {
		return menuUpdated{}, fmt.Errorf("failed to reload menu: %w", err)
	}
	if change.items != nil {
		if err := syncMenuSourceItemValues(ctx, store, root, update.now); err != nil {
			return menuUpdated{}, err
		}
	}
	if err := advanceMenuContentDocument(ctx, store, root.ID, document.ID, document.Revision, update.now); err != nil {
		return menuUpdated{}, err
	}
	return menuUpdated{root: root, changedFields: changedFields}, nil
}

// lockedMenuChange compares update with the locked menu and returns the part
// that changes it, with the changed audit fields.
func lockedMenuChange(
	ctx context.Context,
	store Store,
	menu Root,
	name *string,
	update menuUpdate,
) (menuUpdate, []string, error) {
	change := menuUpdate{now: update.now}
	changedFields := make([]string, 0, 2)
	if update.name != nil && *update.name != menu.Name {
		if err := ensureUpdatedMenuNameAvailable(ctx, store, menu.ID, name); err != nil {
			return change, nil, err
		}
		change.name = update.name
		changedFields = append(changedFields, "name")
	}
	if update.items != nil && !semanticMenuItemsEqual(menu.ItemsJSON, update.items) {
		change.items = update.items
		changedFields = append(changedFields, "items")
	}
	return change, changedFields, nil
}

// syncMenuSourceItemValues brings the source label values and translation rows
// in line with the items just written to root.
func syncMenuSourceItemValues(ctx context.Context, store Store, root Root, now time.Time) error {
	nextItems, err := decodeSourceItems(root.ItemsJSON)
	if err != nil {
		return err
	}
	if err := syncCurrentSourceLabelValues(ctx, store, root, nextItems, now); err != nil {
		return err
	}
	return reconcileTranslationRowsWithSourceItems(ctx, store, root.ID, nextItems, now)
}

func menuUpdateAction(
	ctx context.Context,
	current Root,
	replacesItems bool,
	update menuUpdate,
) menuAction {
	if !replacesItems {
		return policyv1.Menu.Edit
	}
	if update.items == nil || menuItemTopologyEqual(ctx, current.ID, current.ItemsJSON, update.items) {
		return policyv1.Menu.Edit
	}
	return policyv1.Menu.Manage
}

func ensureUpdatedMenuNameAvailable(ctx context.Context, store Store, menuID string, name *string) error {
	if name == nil {
		return nil
	}
	normalized := strings.TrimSpace(*name)
	taken, err := store.MenuNameTaken(ctx, normalized, menuID)
	if err != nil {
		return fmt.Errorf("failed to check menu name uniqueness: %w", err)
	}
	if taken {
		return domainerr.Duplicate("menu", "name", normalized)
	}
	return nil
}

// updateMenuRow writes the changed name and items of change. A unique
// violation is reported against the requested name.
func updateMenuRow(ctx context.Context, store Store, menuID string, name *string, change menuUpdate) error {
	err := store.UpdateMenu(ctx, menuID, RootChange{Name: change.name, ItemsJSON: change.items, UpdatedAt: change.now})
	if errors.Is(err, ErrMenuNameTaken) && name != nil {
		return domainerr.Duplicate("menu", "name", strings.TrimSpace(*name))
	}
	return err
}
