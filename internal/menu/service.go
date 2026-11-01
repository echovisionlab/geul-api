package menu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Menu validation constants
const (
	menuNameMaxLength      = 100
	menuItemLabelMaxLength = 255
	menuMaxNestingDepth    = 5
	menuURLMaxLength       = 2048
)

// Deps are the collaborators of the Menu use cases. Every field is required;
// NewService rejects a missing one.
type Deps struct {
	DB           *gorm.DB
	SpiceDB      *auth.SpiceDBClient
	SiteSettings SiteSettingsReferences
	Targets      TargetReferences
	// Checkpoints authorize the contributors of a collaboration save.
	Checkpoints MenuCollaborationCheckpointFence
	Audit       domainaudit.Appender
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "SiteSettings": d.SiteSettings == nil,
		"Targets": d.Targets == nil, "Checkpoints": d.Checkpoints == nil, "Audit": d.Audit == nil,
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
	db           *gorm.DB
	permissions  menuPermissionChecker
	auditWriter  domainaudit.Appender
	siteSettings SiteSettingsReferences
	targets      TargetReferences
	checkpoints  MenuCollaborationCheckpointFence
	mutations    *mutation.Runner
}

// NewService validates deps and returns the Menu use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, permissions: deps.SpiceDB, auditWriter: deps.Audit,
		siteSettings: deps.SiteSettings, targets: deps.Targets, checkpoints: deps.Checkpoints,
		mutations: mutations,
	}, nil
}

func lockMenuForUpdate(ctx context.Context, tx *gorm.DB, menuID string) (*model.Menu, error) {
	var menu model.Menu
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&menu, "id = ?", menuID).Error; err != nil {
		return nil, err
	}
	return &menu, nil
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

// CreateMenuCommand creates a Menu. Items carry the API item tree the Menu
// validates as a whole.
type CreateMenuCommand struct {
	Name  string
	Items []*managev1.MenuItem
}

// UpdateMenuCommand updates the name, the item tree, or both. A nil field
// is left alone; Items is set when the request replaces the tree.
type UpdateMenuCommand struct {
	ID    string
	Name  *string
	Items *[]*managev1.MenuItem
}

// DeleteMenuCommand deletes a Menu.
type DeleteMenuCommand struct {
	ID string
}

func (s *Service) GetMenu(ctx context.Context, query GetMenuQuery) (_ *managev1.Menu, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("menu_id", query.ID))

	id := strings.TrimSpace(query.ID)
	if id == "" {
		return nil, domainerr.Required("id")
	}

	var menu model.Menu
	if err := s.db.WithContext(ctx).First(&menu, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("menu", id)
		}
		return nil, fmt.Errorf("failed to get menu: %w", err)
	}
	if err := s.requireExistingMenuPermission(ctx, id, policyv1.Menu.View); err != nil {
		return nil, err
	}

	protoMenu, err := toProtoMenu(&menu)
	if err != nil {
		return nil, fmt.Errorf("failed to convert menu: %w", err)
	}
	return protoMenu, nil
}

func (s *Service) ListMenus(ctx context.Context, query ListMenusQuery) (*managev1.ListMenusResponse, error) {
	can, err := policyv1.Menu.List()
	if err != nil {
		return nil, err
	}
	if err := requireMenuGlobalCan(ctx, s.permissions, can); err != nil {
		return nil, err
	}
	var menus []model.Menu
	var total int64

	stored := s.db.WithContext(ctx).Model(&model.Menu{})
	if err := stored.Count(&total).Error; err != nil {
		return nil, err
	}

	limit := int32(100)
	if query.Limit > 0 {
		limit = query.Limit
	}
	offset := query.Offset
	if err := stored.
		Order("name ASC").
		Limit(int(limit)).
		Offset(int(offset)).
		Find(&menus).Error; err != nil {
		return nil, err
	}

	protoMenus := make([]*managev1.Menu, 0, len(menus))
	for i := range menus {
		protoMenu, err := toProtoMenu(&menus[i])
		if err != nil {
			// One corrupted menu must not break the whole list.
			slog.ErrorContext(ctx, "Stored Menu items are not valid; omitting the menu from the list", "domain", "menu", "operation", "decode_menu_items", "outcome", "failed", "menu_id", menus[i].ID, failurecause.Attr(err))
			continue
		}
		protoMenus = append(protoMenus, protoMenu)
	}

	return &managev1.ListMenusResponse{
		Menus: protoMenus,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}, nil
}

func (s *Service) CreateMenu(ctx context.Context, command CreateMenuCommand) (*managev1.Menu, error) {
	can, err := policyv1.Menu.Create()
	if err != nil {
		return nil, err
	}
	if err := validateMenuName(command.Name); err != nil {
		return nil, err
	}
	var existingCount int64
	if err := s.db.WithContext(ctx).Model(&model.Menu{}).Where("name = ?", command.Name).Count(&existingCount).Error; err != nil {
		return nil, fmt.Errorf("failed to check menu name uniqueness: %w", err)
	}
	if existingCount > 0 {
		return nil, domainerr.Duplicate("menu", "name", command.Name)
	}
	if err := validateMenuItems(command.Items, 0); err != nil {
		return nil, err
	}
	items, err := protoItemsToJSON(command.Items)
	if err != nil {
		return nil, domainerr.New(domainerr.Invalid, "", fmt.Sprintf("failed to convert menu items: %s", err.Error()))
	}

	now := dbtime.Now()
	menu, err := mutation.Run(ctx, s.mutations, mutation.Spec[*model.Menu]{
		// Target locks precede the principal fence, as in every Menu write.
		Access: s.afterTargetLocks(collectMenuTargetReferences(command.Items), func(require mutation.Require) error {
			if err := require(can); err != nil {
				if mutation.IsDenied(err) {
					return domainerr.AdminRequired().WithCause(err)
				}
				return err
			}
			return nil
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (*model.Menu, error) {
			documentID, err := createMenuContentDocument(ctx, tx, now)
			if err != nil {
				return nil, err
			}
			menu := &model.Menu{
				Name: strings.TrimSpace(command.Name), Items: items,
				SourceLocale: translation.DefaultLocale, ContentDocumentID: documentID.String(),
				CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(menu).Error; err != nil {
				return nil, err
			}
			return menu, initializeCurrentSourceLabelValues(ctx, tx, menu.ID, menu.Items, menu.UpdatedAt)
		},
		Audit: func(menu *model.Menu) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMenuCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMenuCreatedAuditRecord(metadata, menu.ID)
			})
		},
		Relationships: func(menu *model.Menu) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return menuPolicy(policyv1.Menu.TouchPolicy, policyv1.Menu.DeletePolicy, menu.ID)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create menu: %w", err)
	}
	protoMenu, err := toProtoMenu(menu)
	if err != nil {
		return nil, fmt.Errorf("failed to convert menu: %w", err)
	}
	return protoMenu, nil
}

func (s *Service) DeleteMenu(ctx context.Context, command DeleteMenuCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("menu_id", command.ID))

	menuID := strings.TrimSpace(command.ID)
	if menuID == "" {
		return domainerr.Required("id")
	}
	var locked *model.Menu
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
			// Site Settings mutations always lock the singleton first. Deleting a
			// selected Menu follows that order before taking the Menu lock too;
			// a denied delete rolls the cleared slots back.
			if err := s.siteSettings.ClearMenuReferences(ctx, tx, menuID); err != nil {
				return err
			}
			var err error
			if locked, err = lockMenuForUpdate(ctx, tx, menuID); err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return domainerr.ResourceNotFound("menu", menuID)
				}
				return err
			}
			return requireFreshMenuPermission(ctx, tx, require, menuID, policyv1.Menu.Delete)
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (struct{}, error) {
			document, err := loadMenuContentDocumentStateFromRoot(ctx, tx, *locked, true)
			if err != nil {
				return struct{}{}, err
			}
			if err := tx.Delete(locked).Error; err != nil {
				return struct{}{}, err
			}
			return struct{}{}, deleteMenuContentDocument(ctx, tx, document.ID)
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
func (s *Service) afterTargetLocks(targets []TargetReference, authorize func(mutation.Require) error) mutation.Access {
	return mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
		targetErr := s.targets.ValidateAndLock(ctx, tx, targets)
		if err := authorize(require); err != nil {
			return err
		}
		return targetErr
	})
}

// semanticMenuItemsEqual treats undecodable items as changed. It runs after
// menuUpdateAction, whose topology comparison already logged a decode failure
// of the same stored and requested items.
func semanticMenuItemsEqual(stored, requested []byte) bool {
	var storedItems []model.MenuItem
	if len(stored) > 0 {
		if err := json.Unmarshal(stored, &storedItems); err != nil {
			return false
		}
	}
	var requestedItems []model.MenuItem
	if len(requested) > 0 {
		if err := json.Unmarshal(requested, &requestedItems); err != nil {
			return false
		}
	}
	return reflect.DeepEqual(storedItems, requestedItems)
}

// ==================== Validation Methods ====================

// validateMenuName validates the menu name
func validateMenuName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return domainerr.InvalidField("name", "cannot be empty")
	}
	if len(name) > menuNameMaxLength {
		return domainerr.InvalidField("name", fmt.Sprintf("must be at most %d characters", menuNameMaxLength))
	}
	return nil
}

// validateMenuItems validates menu items recursively
func validateMenuItems(items []*managev1.MenuItem, depth int) error {
	if depth > menuMaxNestingDepth {
		return domainerr.InvalidField("items", fmt.Sprintf("menu nesting depth exceeds maximum of %d levels", menuMaxNestingDepth))
	}

	for i, item := range items {
		if err := validateMenuItem(item, i, depth); err != nil {
			return err
		}
	}
	return nil
}

// validateMenuItem validates a single menu item
func validateMenuItem(item *managev1.MenuItem, index, depth int) error {
	itemPath := fmt.Sprintf("item[%d]", index)
	if depth > 0 {
		itemPath = fmt.Sprintf("child at depth %d, %s", depth, itemPath)
	}

	// Validate ID
	id := strings.TrimSpace(item.Id)
	if id == "" {
		return domainerr.InvalidField(itemPath, "id cannot be empty")
	}

	// Validate label
	label := strings.TrimSpace(item.Label)
	if label == "" {
		return domainerr.InvalidField(itemPath, "label cannot be empty")
	}
	if len(label) > menuItemLabelMaxLength {
		return domainerr.InvalidField(itemPath, fmt.Sprintf("label must be at most %d characters", menuItemLabelMaxLength))
	}

	// Validate linkType
	validLinkTypes := map[managev1.MenuLinkType]bool{
		managev1.MenuLinkType_MENU_LINK_TYPE_CUSTOM:   true,
		managev1.MenuLinkType_MENU_LINK_TYPE_PAGE:     true,
		managev1.MenuLinkType_MENU_LINK_TYPE_CATEGORY: true,
		managev1.MenuLinkType_MENU_LINK_TYPE_TAG:      true,
		managev1.MenuLinkType_MENU_LINK_TYPE_SERIES:   true,
	}
	if item.LinkType == managev1.MenuLinkType_MENU_LINK_TYPE_UNSPECIFIED {
		return domainerr.InvalidField(itemPath, "linkType must be specified")
	}
	if !validLinkTypes[item.LinkType] {
		return domainerr.InvalidField(itemPath, "invalid linkType")
	}

	if err := validateMenuItemLink(item, itemPath); err != nil {
		return err
	}

	// Validate visibility if provided
	if item.Visibility != nil {
		validModes := map[managev1.MenuVisibilityMode]bool{
			managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_ALL:           true,
			managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_AUTHENTICATED: true,
			managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_GUEST:         true,
			managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_ROLES:         true,
		}
		if !validModes[item.Visibility.Mode] && item.Visibility.Mode != managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_UNSPECIFIED {
			return domainerr.InvalidField(itemPath, "invalid visibility mode")
		}
		roles, err := normalizeUserRoleList(itemPath+".visibility.roles", item.Visibility.Roles)
		if err != nil {
			return err
		}
		item.Visibility.Roles = roles
		// If mode is "roles", at least one role should be specified
		if item.Visibility.Mode == managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_ROLES && len(item.Visibility.Roles) == 0 {
			return domainerr.InvalidField(itemPath, "at least one role is required when visibility mode is 'roles'")
		}
	}

	fixedLocaleTrimmed := ""
	if item.FixedLocale != nil {
		fixedLocaleTrimmed = strings.TrimSpace(*item.FixedLocale)
		if fixedLocaleTrimmed != "" && NormalizeItemFixedLocale(item.FixedLocale) == nil {
			return domainerr.InvalidField(itemPath, "fixedLocale must be a supported locale")
		}
	}

	mode := NormalizeItemLocalizationMode(
		protoLocalizationModeToStringPtr(item.LocalizationMode),
		item.FixedLocale,
	)
	if mode == model.MenuItemLocalizationModeFixedLocale && fixedLocaleTrimmed == "" {
		return domainerr.InvalidField(itemPath, "fixedLocale is required when localization mode is fixed locale")
	}

	// Recursively validate children
	if len(item.Children) > 0 {
		if err := validateMenuItems(item.Children, depth+1); err != nil {
			return err
		}
	}

	return nil
}

func validateMenuItemLink(item *managev1.MenuItem, itemPath string) error {
	if item.LinkType != managev1.MenuLinkType_MENU_LINK_TYPE_CUSTOM {
		hasTargetID := item.TargetId != nil && strings.TrimSpace(*item.TargetId) != ""
		hasTargetSlug := item.TargetSlug != nil && strings.TrimSpace(*item.TargetSlug) != ""
		if !hasTargetID && !hasTargetSlug {
			return domainerr.InvalidField(itemPath, fmt.Sprintf("targetId or targetSlug is required for %s link type", item.LinkType.String()))
		}
		return nil
	}
	if item.Url == nil || strings.TrimSpace(*item.Url) == "" {
		return domainerr.InvalidField(itemPath, "url is required for custom link type")
	}
	urlString := strings.TrimSpace(*item.Url)
	if len(urlString) > menuURLMaxLength {
		return domainerr.InvalidField(itemPath, fmt.Sprintf("url must be at most %d characters", menuURLMaxLength))
	}
	if strings.HasPrefix(urlString, "/") {
		return nil
	}
	if _, err := url.ParseRequestURI(urlString); err != nil {
		return domainerr.InvalidField(itemPath, "url must be a valid URL or relative path")
	}
	return nil
}

// ==================== Helper Methods ====================

func toProtoMenu(m *model.Menu) (*managev1.Menu, error) {
	menu := &managev1.Menu{
		Id:           m.ID,
		SourceLocale: m.SourceLocale,
		Name:         m.Name,
		CreatedAt:    timestamppb.New(m.CreatedAt),
		UpdatedAt:    timestamppb.New(m.UpdatedAt),
	}

	// Parse items from JSON
	if len(m.Items) > 0 {
		var items []model.MenuItem
		if err := json.Unmarshal(m.Items, &items); err != nil {
			return nil, fmt.Errorf("failed to parse menu items: %w", err)
		}
		menu.Items = modelItemsToProto(items)
	} else {
		menu.Items = []*managev1.MenuItem{}
	}

	return menu, nil
}

func modelItemsToProto(items []model.MenuItem) []*managev1.MenuItem {
	result := make([]*managev1.MenuItem, len(items))
	for i, item := range items {
		result[i] = modelItemToProto(&item)
	}
	return result
}

func modelItemToProto(item *model.MenuItem) *managev1.MenuItem {
	localizationMode, fixedLocale := CanonicalizeItemLocalization(item.LocalizationMode, item.FixedLocale)
	protoItem := &managev1.MenuItem{
		Id:               item.ID,
		Label:            item.Label,
		LinkType:         linkTypeToProto(item.LinkType),
		LocalizationMode: localizationModeToProto(localizationMode),
	}

	if item.URL != nil {
		protoItem.Url = item.URL
	}
	if item.TargetID != nil {
		protoItem.TargetId = item.TargetID
	}
	if item.TargetSlug != nil {
		protoItem.TargetSlug = item.TargetSlug
	}
	if item.OpenInNewTab != nil {
		protoItem.OpenInNewTab = item.OpenInNewTab
	}
	if item.Visibility != nil {
		protoItem.Visibility = &managev1.MenuVisibility{
			Mode:  visibilityModeToProto(item.Visibility.Mode),
			Roles: item.Visibility.Roles,
		}
	}
	if fixedLocale != nil {
		protoItem.FixedLocale = fixedLocale
	}

	// Recursive children
	if len(item.Children) > 0 {
		protoItem.Children = modelItemsToProto(item.Children)
	}

	return protoItem
}

func linkTypeToProto(linkType string) managev1.MenuLinkType {
	switch linkType {
	case "custom":
		return managev1.MenuLinkType_MENU_LINK_TYPE_CUSTOM
	case "page":
		return managev1.MenuLinkType_MENU_LINK_TYPE_PAGE
	case "category":
		return managev1.MenuLinkType_MENU_LINK_TYPE_CATEGORY
	case "tag":
		return managev1.MenuLinkType_MENU_LINK_TYPE_TAG
	case "series":
		return managev1.MenuLinkType_MENU_LINK_TYPE_SERIES
	default:
		return managev1.MenuLinkType_MENU_LINK_TYPE_UNSPECIFIED
	}
}

func visibilityModeToProto(mode string) managev1.MenuVisibilityMode {
	switch mode {
	case "all":
		return managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_ALL
	case "authenticated":
		return managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_AUTHENTICATED
	case "guest":
		return managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_GUEST
	case "roles":
		return managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_ROLES
	default:
		return managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_ALL
	}
}

func localizationModeToProto(mode *string) managev1.MenuItemLocalizationMode {
	if mode == nil {
		return managev1.MenuItemLocalizationMode_MENU_ITEM_LOCALIZATION_MODE_UNSPECIFIED
	}

	switch *mode {
	case model.MenuItemLocalizationModeFixedLocale:
		return managev1.MenuItemLocalizationMode_MENU_ITEM_LOCALIZATION_MODE_FIXED_LOCALE
	case model.MenuItemLocalizationModeTranslated:
		return managev1.MenuItemLocalizationMode_MENU_ITEM_LOCALIZATION_MODE_TRANSLATED
	default:
		return managev1.MenuItemLocalizationMode_MENU_ITEM_LOCALIZATION_MODE_UNSPECIFIED
	}
}

func protoItemsToJSON(items []*managev1.MenuItem) (json.RawMessage, error) {
	modelItems := make([]model.MenuItem, len(items))
	for i, item := range items {
		modelItems[i] = protoItemToModel(item)
	}
	return json.Marshal(modelItems)
}

func protoItemToModel(item *managev1.MenuItem) model.MenuItem {
	localizationMode, fixedLocale := CanonicalizeItemLocalization(
		protoLocalizationModeToStringPtr(item.LocalizationMode),
		item.FixedLocale,
	)
	modelItem := model.MenuItem{
		ID:               item.Id,
		Label:            item.Label,
		LinkType:         protoLinkTypeToString(item.LinkType),
		LocalizationMode: localizationMode,
		FixedLocale:      fixedLocale,
	}

	if item.Url != nil {
		modelItem.URL = item.Url
	}
	if item.TargetId != nil {
		modelItem.TargetID = item.TargetId
	}
	if item.TargetSlug != nil {
		modelItem.TargetSlug = item.TargetSlug
	}
	if item.OpenInNewTab != nil {
		modelItem.OpenInNewTab = item.OpenInNewTab
	}
	if item.Visibility != nil {
		// validateMenuItems has already rejected unknown roles.
		roles, _ := normalizeUserRoleList("visibility.roles", item.Visibility.Roles)
		modelItem.Visibility = &model.MenuVisibility{
			Mode:  protoVisibilityModeToString(item.Visibility.Mode),
			Roles: roles,
		}
	}

	// Recursive children
	if len(item.Children) > 0 {
		modelItem.Children = make([]model.MenuItem, len(item.Children))
		for i, child := range item.Children {
			modelItem.Children[i] = protoItemToModel(child)
		}
	}

	return modelItem
}

func protoLinkTypeToString(linkType managev1.MenuLinkType) string {
	switch linkType {
	case managev1.MenuLinkType_MENU_LINK_TYPE_CUSTOM:
		return "custom"
	case managev1.MenuLinkType_MENU_LINK_TYPE_PAGE:
		return "page"
	case managev1.MenuLinkType_MENU_LINK_TYPE_CATEGORY:
		return "category"
	case managev1.MenuLinkType_MENU_LINK_TYPE_TAG:
		return "tag"
	case managev1.MenuLinkType_MENU_LINK_TYPE_SERIES:
		return "series"
	default:
		return "custom"
	}
}

func protoVisibilityModeToString(mode managev1.MenuVisibilityMode) string {
	switch mode {
	case managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_ALL:
		return "all"
	case managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_AUTHENTICATED:
		return "authenticated"
	case managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_GUEST:
		return "guest"
	case managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_ROLES:
		return "roles"
	default:
		return "all"
	}
}

func protoLocalizationModeToStringPtr(
	mode managev1.MenuItemLocalizationMode,
) *string {
	switch mode {
	case managev1.MenuItemLocalizationMode_MENU_ITEM_LOCALIZATION_MODE_FIXED_LOCALE:
		value := model.MenuItemLocalizationModeFixedLocale
		return &value
	case managev1.MenuItemLocalizationMode_MENU_ITEM_LOCALIZATION_MODE_TRANSLATED:
		value := model.MenuItemLocalizationModeTranslated
		return &value
	default:
		return nil
	}
}
