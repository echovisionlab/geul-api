package menu

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Deps are the collaborators of the Menu use cases. Every field is required;
// NewService rejects a missing one.
type Deps struct {
	Units   mutation.UnitOfWork[Store] // menupostgres.NewUnits(db, ...)
	SpiceDB mutation.Authorizer
	Fence   identitystate.Fence // identitystate.PostgresFence{}
	Audit   domainaudit.Appender
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("menu dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Menu use cases. Connect, MCP and AI document adapters
// translate their requests into its commands and queries.
type Service struct {
	units       mutation.UnitOfWork[Store]
	permissions menuPermissionChecker
	mutations   *mutation.StoreRunner[Store]
}

// NewService validates deps and returns the Menu use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{units: deps.Units, permissions: deps.SpiceDB, mutations: mutations}, nil
}

// GetMenuQuery reads one Menu.
type GetMenuQuery struct {
	ID string
}

// ListMenusQuery pages Menus by name. A zero Limit reads 100.
type ListMenusQuery struct {
	Limit  int32
	Offset int32
}

// CreateMenuCommand creates a Menu. Items carry the requested item tree the
// Menu validates as a whole.
type CreateMenuCommand struct {
	Name  string
	Items []Item
}

// UpdateMenuCommand updates the name, the item tree, or both. A nil field
// is left alone; Items is set when the request replaces the tree.
type UpdateMenuCommand struct {
	ID    string
	Name  *string
	Items *[]Item
}

// DeleteMenuCommand deletes a Menu.
type DeleteMenuCommand struct {
	ID string
}

func (s *Service) GetMenu(ctx context.Context, query GetMenuQuery) (_ Menu, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("menu_id", query.ID))

	id := strings.TrimSpace(query.ID)
	if id == "" {
		return Menu{}, domainerr.Required("id")
	}

	var root Root
	if err := s.units.Read(ctx, func(store Store) (err error) {
		root, err = store.Menu(ctx, id)
		return err
	}); err != nil {
		if errors.Is(err, ErrMenuNotFound) {
			return Menu{}, domainerr.ResourceNotFound("menu", id)
		}
		return Menu{}, fmt.Errorf("failed to get menu: %w", err)
	}
	if err := s.requireExistingMenuPermission(ctx, id, policyv1.Menu.View); err != nil {
		return Menu{}, err
	}

	menu, err := menuOf(root)
	if err != nil {
		return Menu{}, fmt.Errorf("failed to convert menu: %w", err)
	}
	return menu, nil
}

func (s *Service) ListMenus(ctx context.Context, query ListMenusQuery) (MenuPage, error) {
	can, err := policyv1.Menu.List()
	if err != nil {
		return MenuPage{}, err
	}
	if err := requireMenuGlobalCan(ctx, s.permissions, can); err != nil {
		return MenuPage{}, err
	}

	limit := int32(100)
	if query.Limit > 0 {
		limit = query.Limit
	}
	offset := query.Offset
	var roots []Root
	var total int64
	if err := s.units.Read(ctx, func(store Store) (err error) {
		roots, total, err = store.ListMenus(ctx, int(limit), int(offset))
		return err
	}); err != nil {
		return MenuPage{}, err
	}

	menus := make([]Menu, 0, len(roots))
	for _, root := range roots {
		menu, err := menuOf(root)
		if err != nil {
			// One corrupted menu must not break the whole list.
			slog.ErrorContext(ctx, "Stored Menu items are not valid; omitting the menu from the list", "domain", "menu", "operation", "decode_menu_items", "outcome", "failed", "menu_id", root.ID, failurecause.Attr(err))
			continue
		}
		menus = append(menus, menu)
	}
	return MenuPage{
		Menus: menus, Total: int32(total), Limit: limit, Offset: offset,
		HasMore: offset+limit < int32(total),
	}, nil
}

func (s *Service) CreateMenu(ctx context.Context, command CreateMenuCommand) (Menu, error) {
	can, err := policyv1.Menu.Create()
	if err != nil {
		return Menu{}, err
	}
	if err := validateMenuName(command.Name); err != nil {
		return Menu{}, err
	}
	var taken bool
	if err := s.units.Read(ctx, func(store Store) (err error) {
		taken, err = store.MenuNameTaken(ctx, command.Name, "")
		return err
	}); err != nil {
		return Menu{}, fmt.Errorf("failed to check menu name uniqueness: %w", err)
	}
	if taken {
		return Menu{}, domainerr.Duplicate("menu", "name", command.Name)
	}
	if err := validateMenuItems(command.Items, 0); err != nil {
		return Menu{}, err
	}
	items, err := encodeRequestedItems(command.Items)
	if err != nil {
		return Menu{}, err
	}

	now := dbtime.Now()
	root, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Root, Store]{
		// Target locks precede the principal fence, as in every Menu write.
		Access: afterTargetLocks(collectMenuTargetReferences(command.Items), func(require mutation.Require) error {
			if err := require(can); err != nil {
				if mutation.IsDenied(err) {
					return domainerr.AdminRequired().WithCause(err)
				}
				return err
			}
			return nil
		}),
		Change: func(ctx context.Context, store Store) (Root, error) {
			documentID, err := createMenuContentDocument(ctx, store, now)
			if err != nil {
				return Root{}, err
			}
			root, err := store.InsertMenu(ctx, Root{
				Name: strings.TrimSpace(command.Name), ItemsJSON: items,
				SourceLocale: translation.DefaultLocale, ContentDocumentID: documentID.String(),
				CreatedAt: now, UpdatedAt: now,
			})
			if err != nil {
				return Root{}, err
			}
			return root, initializeSourceLabelValues(ctx, store, root)
		},
		Audit: func(root Root) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMenuCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMenuCreatedAuditRecord(metadata, root.ID)
			})
		},
		Relationships: func(root Root) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return menuPolicy(policyv1.Menu.TouchPolicy, policyv1.Menu.DeletePolicy, root.ID)
		},
	})
	if err != nil {
		return Menu{}, fmt.Errorf("failed to create menu: %w", err)
	}
	menu, err := menuOf(root)
	if err != nil {
		return Menu{}, fmt.Errorf("failed to convert menu: %w", err)
	}
	return menu, nil
}

func (s *Service) DeleteMenu(ctx context.Context, command DeleteMenuCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("menu_id", command.ID))

	menuID := strings.TrimSpace(command.ID)
	if menuID == "" {
		return domainerr.Required("id")
	}
	var locked Root
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Access: mutation.Locked(func(ctx context.Context, store Store, require mutation.Require) error {
			// Site Settings mutations always lock the singleton first. Deleting a
			// selected Menu follows that order before taking the Menu lock too;
			// a denied delete rolls the cleared slots back.
			if err := store.ClearSiteSettingsMenu(ctx, menuID); err != nil {
				return err
			}
			var err error
			if locked, err = store.LockMenu(ctx, menuID); err != nil {
				if errors.Is(err, ErrMenuNotFound) {
					return domainerr.ResourceNotFound("menu", menuID)
				}
				return err
			}
			return requireFreshMenuPermission(ctx, require, menuID, policyv1.Menu.Delete)
		}),
		Change: func(ctx context.Context, store Store) (struct{}, error) {
			document, err := loadMenuContentDocumentState(ctx, store, locked, true)
			if err != nil {
				return struct{}{}, err
			}
			if err := store.DeleteMenu(ctx, locked.ID); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, deleteMenuContentDocument(ctx, store, document.ID)
		},
		Audit: func(struct{}) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMenuDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMenuDeletedAuditRecord(metadata, menuID)
			})
		},
		Relationships: func(struct{}) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return menuPolicy(policyv1.Menu.DeletePolicy, policyv1.Menu.TouchPolicy, menuID)
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete menu: %w", err)
	}
	return nil
}

// menuPolicy returns the one Menu policy relationship apply writes and its
// inverse.
func menuPolicy(apply, compensate func(string) (policyv1.RelationshipMutation, error), menuID string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	applied, err := apply(menuID)
	if err != nil {
		return nil, nil, err
	}
	compensated, err := compensate(menuID)
	if err != nil {
		return nil, nil, err
	}
	return []policyv1.RelationshipMutation{applied}, []policyv1.RelationshipMutation{compensated}, nil
}

// afterTargetLocks validates and locks the Menu's item targets before the
// fresh check authorize runs. A target error is answered after the check, so
// a caller without authority learns nothing about the targets.
func afterTargetLocks(targets []TargetReference, authorize func(mutation.Require) error) mutation.Access {
	return mutation.Locked(func(ctx context.Context, store Store, require mutation.Require) error {
		targetErr := lockTargets(ctx, store, targets)
		if err := authorize(require); err != nil {
			return err
		}
		return targetErr
	})
}

// lockTargets share-locks targets and rejects a missing one as an invalid
// item.
func lockTargets(ctx context.Context, store Store, targets []TargetReference) error {
	err := store.LockTargets(ctx, targets)
	var missing TargetNotFoundError
	if errors.As(err, &missing) {
		return domainerr.InvalidField("items", string(missing.LinkType)+" target does not exist")
	}
	return err
}
