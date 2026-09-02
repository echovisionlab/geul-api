package menu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"reflect"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
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

// MenuService implements the MenuService Connect handler
type MenuService struct {
	managev1connect.UnimplementedMenuServiceHandler
	db           *gorm.DB
	spiceDB      *auth.SpiceDBClient
	permissions  menuPermissionChecker
	auditWriter  domainaudit.Appender
	siteSettings SiteSettingsReferences
	targets      TargetReferences
}

// NewMenuService creates a new MenuService
func NewMenuService(
	db *gorm.DB,
	siteSettings SiteSettingsReferences,
	targets TargetReferences,
	spiceDB *auth.SpiceDBClient,
) *MenuService {
	if db == nil {
		panic("db is required")
	}
	if siteSettings == nil {
		panic("site settings references are required")
	}
	if targets == nil {
		panic("menu target references are required")
	}
	dependencycheck.MustNotNil(spiceDB, "spiceDB")
	return &MenuService{
		db: db, spiceDB: spiceDB, permissions: spiceDB,
		siteSettings: siteSettings, targets: targets,
	}
}

// NewAuditedMenuService requires every authoritative Menu mutation to append
// its Domain Audit record in the same transaction.
func NewAuditedMenuService(
	db *gorm.DB,
	auditWriter domainaudit.Appender,
	siteSettings SiteSettingsReferences,
	targets TargetReferences,
	spiceDB *auth.SpiceDBClient,
) *MenuService {
	if auditWriter == nil {
		panic("menu audit writer is required")
	}
	service := NewMenuService(db, siteSettings, targets, spiceDB)
	service.auditWriter = auditWriter
	return service
}

func lockMenuForUpdate(ctx context.Context, tx *gorm.DB, menuID string) (*model.Menu, error) {
	var menu model.Menu
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&menu, "id = ?", menuID).Error; err != nil {
		return nil, err
	}
	return &menu, nil
}

// GetMenu retrieves a menu by name
// GetMenuById retrieves a menu by ID
func (s *MenuService) GetMenuById(
	ctx context.Context,
	req *connect.Request[managev1.GetMenuByIdRequest],
) (*connect.Response[managev1.Menu], error) {
	// Validate ID is provided
	id := strings.TrimSpace(req.Msg.Id)
	if id == "" {
		return nil, errs.Required("id")
	}

	var menu model.Menu
	if err := s.db.WithContext(ctx).First(&menu, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("menu", id)
		}
		return nil, errs.Internal(fmt.Errorf("failed to get menu: %w", err))
	}
	if err := s.requireExistingMenuPermission(ctx, id, policyv1.Menu.View); err != nil {
		return nil, err
	}

	protoMenu, err := s.toProtoMenu(&menu)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to convert menu: %w", err))
	}

	return connect.NewResponse(protoMenu), nil
}

// ListMenus returns all menus
func (s *MenuService) ListMenus(
	ctx context.Context,
	req *connect.Request[managev1.ListMenusRequest],
) (*connect.Response[managev1.ListMenusResponse], error) {
	can, err := policyv1.Menu.List()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := requireMenuGlobalCan(ctx, s.permissions, can); err != nil {
		return nil, err
	}
	var menus []model.Menu
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Menu{})

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply pagination
	limit := int32(100)
	offset := int32(0)
	if req.Msg.Pagination != nil {
		if req.Msg.Pagination.Limit > 0 {
			limit = req.Msg.Pagination.Limit
		}
		offset = req.Msg.Pagination.Offset
	}

	if err := query.
		Order("name ASC").
		Limit(int(limit)).
		Offset(int(offset)).
		Find(&menus).Error; err != nil {
		return nil, errs.Internal(err)
	}

	protoMenus := make([]*managev1.Menu, 0, len(menus))
	for i := range menus {
		protoMenu, err := s.toProtoMenu(&menus[i])
		if err != nil {
			// Log the error but continue with other menus
			// This prevents one corrupted menu from breaking the whole list
			slog.Error("failed to parse menu items JSON",
				"menuId", menus[i].ID,
				"menuName", menus[i].Name,
				"error", err)
			continue
		}
		protoMenus = append(protoMenus, protoMenu)
	}

	return connect.NewResponse(&managev1.ListMenusResponse{
		Menus: protoMenus,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// CreateMenu creates a new menu
func (s *MenuService) CreateMenu(
	ctx context.Context,
	req *connect.Request[managev1.CreateMenuRequest],
) (*connect.Response[managev1.Menu], error) {
	can, err := policyv1.Menu.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	// Validate menu name
	if err := s.validateMenuName(req.Msg.Name); err != nil {
		return nil, err
	}

	// Check name uniqueness
	var existingCount int64
	if err := s.db.WithContext(ctx).Model(&model.Menu{}).Where("name = ?", req.Msg.Name).Count(&existingCount).Error; err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to check menu name uniqueness: %w", err))
	}
	if existingCount > 0 {
		return nil, errs.AlreadyExists("menu", "name", req.Msg.Name)
	}

	// Validate menu items
	if err := s.validateMenuItems(req.Msg.Items, 0); err != nil {
		return nil, err
	}

	// Convert proto items to JSON
	items, err := s.protoItemsToJSON(req.Msg.Items)
	if err != nil {
		return nil, errs.InvalidArgumentMsg(fmt.Sprintf("failed to convert menu items: %s", err.Error()))
	}

	now := time.Now().UTC()
	menu := &model.Menu{
		Name: strings.TrimSpace(req.Msg.Name), Items: items,
		SourceLocale: translation.DefaultLocale,
		CreatedAt:    now, UpdatedAt: now,
	}

	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		targetErr := s.targets.ValidateAndLock(ctx, tx, collectMenuTargetReferences(req.Msg.Items))
		if err := requireFreshMenuGlobalCan(ctx, tx, s.permissions, can); err != nil {
			return err
		}
		if targetErr != nil {
			return targetErr
		}
		documentID, err := createMenuContentDocument(ctx, tx, now)
		if err != nil {
			return err
		}
		menu.ContentDocumentID = documentID.String()
		if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(menu).Error; err != nil {
			return err
		}
		if err := initializeCurrentSourceLabelValues(
			ctx, tx, menu.ID, menu.Items, menu.UpdatedAt,
		); err != nil {
			return err
		}
		if err := domainaudit.AppendOptionalRequest(
			ctx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditMenuCreated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMenuCreatedAuditRecord(metadata, menu.ID)
			},
		); err != nil {
			return err
		}
		apply, err := policyv1.Menu.TouchPolicy(menu.ID)
		if err != nil {
			return err
		}
		compensate, err := policyv1.Menu.DeletePolicy(menu.ID)
		if err != nil {
			return err
		}
		return write(
			[]policyv1.RelationshipMutation{apply},
			[]policyv1.RelationshipMutation{compensate},
		)
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(fmt.Errorf("failed to create menu: %w", err))
	}
	protoMenu, err := s.toProtoMenu(menu)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to convert menu: %w", err))
	}
	return connect.NewResponse(protoMenu), nil
}

// UpdateMenu updates an existing menu
func (s *MenuService) UpdateMenu(
	ctx context.Context,
	req *connect.Request[managev1.UpdateMenuRequest],
) (*connect.Response[managev1.Menu], error) {
	// Validate ID
	id := strings.TrimSpace(req.Msg.Id)
	if id == "" {
		return nil, errs.Required("id")
	}
	update, err := s.prepareMenuUpdate(req.Msg, time.Now())
	if err != nil {
		return nil, err
	}
	menu, _, err := s.applyMenuUpdate(ctx, id, req.Msg, update)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("menu", id)
		}
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(fmt.Errorf("failed to update menu: %w", err))
	}

	protoMenu, err := s.toProtoMenu(menu)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to convert menu: %w", err))
	}
	return connect.NewResponse(protoMenu), nil
}

// DeleteMenu deletes a menu
func (s *MenuService) DeleteMenu(
	ctx context.Context,
	req *connect.Request[managev1.DeleteMenuRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	menuID := strings.TrimSpace(req.Msg.Id)
	if menuID == "" {
		return nil, errs.Required("id")
	}
	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		// Site Settings mutations always lock the singleton first. Deleting a
		// selected Menu follows that order before taking the Menu lock too.
		if err := s.siteSettings.ClearMenuReferences(ctx, tx, menuID); err != nil {
			return err
		}
		menu, err := lockMenuForUpdate(ctx, tx, menuID)
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("menu", menuID)
			}
			return errs.Internal(err)
		}
		if err := requireFreshMenuPermission(ctx, tx, s.permissions, menuID, policyv1.Menu.Delete); err != nil {
			return err
		}
		document, err := loadMenuContentDocumentStateFromRoot(ctx, tx, *menu, true)
		if err != nil {
			return err
		}

		if err := tx.Delete(menu).Error; err != nil {
			return errs.Internal(err)
		}
		if err := deleteMenuContentDocument(ctx, tx, document.ID); err != nil {
			return err
		}
		if err := domainaudit.AppendOptionalRequest(
			ctx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditMenuDeleted,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMenuDeletedAuditRecord(metadata, menuID)
			},
		); err != nil {
			return err
		}
		apply, err := policyv1.Menu.DeletePolicy(menuID)
		if err != nil {
			return err
		}
		compensate, err := policyv1.Menu.TouchPolicy(menuID)
		if err != nil {
			return err
		}
		return write(
			[]policyv1.RelationshipMutation{apply},
			[]policyv1.RelationshipMutation{compensate},
		)
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(fmt.Errorf("failed to delete menu: %w", err))
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

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
func (s *MenuService) validateMenuName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errs.InvalidArgument("name", "cannot be empty")
	}
	if len(name) > menuNameMaxLength {
		return errs.InvalidArgument("name", fmt.Sprintf("must be at most %d characters", menuNameMaxLength))
	}
	return nil
}

// validateMenuItems validates menu items recursively
func (s *MenuService) validateMenuItems(items []*managev1.MenuItem, depth int) error {
	if depth > menuMaxNestingDepth {
		return errs.InvalidArgument("items", fmt.Sprintf("menu nesting depth exceeds maximum of %d levels", menuMaxNestingDepth))
	}

	for i, item := range items {
		if err := s.validateMenuItem(item, i, depth); err != nil {
			return err
		}
	}
	return nil
}

// validateMenuItem validates a single menu item
func (s *MenuService) validateMenuItem(item *managev1.MenuItem, index, depth int) error {
	itemPath := fmt.Sprintf("item[%d]", index)
	if depth > 0 {
		itemPath = fmt.Sprintf("child at depth %d, %s", depth, itemPath)
	}

	// Validate ID
	id := strings.TrimSpace(item.Id)
	if id == "" {
		return errs.InvalidArgument(itemPath, "id cannot be empty")
	}

	// Validate label
	label := strings.TrimSpace(item.Label)
	if label == "" {
		return errs.InvalidArgument(itemPath, "label cannot be empty")
	}
	if len(label) > menuItemLabelMaxLength {
		return errs.InvalidArgument(itemPath, fmt.Sprintf("label must be at most %d characters", menuItemLabelMaxLength))
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
		return errs.InvalidArgument(itemPath, "linkType must be specified")
	}
	if !validLinkTypes[item.LinkType] {
		return errs.InvalidArgument(itemPath, "invalid linkType")
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
			return errs.InvalidArgument(itemPath, "invalid visibility mode")
		}
		roles, err := normalizeUserRoleList(itemPath+".visibility.roles", item.Visibility.Roles)
		if err != nil {
			return err
		}
		item.Visibility.Roles = roles
		// If mode is "roles", at least one role should be specified
		if item.Visibility.Mode == managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_ROLES && len(item.Visibility.Roles) == 0 {
			return errs.InvalidArgument(itemPath, "at least one role is required when visibility mode is 'roles'")
		}
	}

	fixedLocaleTrimmed := ""
	if item.FixedLocale != nil {
		fixedLocaleTrimmed = strings.TrimSpace(*item.FixedLocale)
		if fixedLocaleTrimmed != "" && NormalizeItemFixedLocale(item.FixedLocale) == nil {
			return errs.InvalidArgument(itemPath, "fixedLocale must be a supported locale")
		}
	}

	mode := NormalizeItemLocalizationMode(
		s.protoLocalizationModeToStringPtr(item.LocalizationMode),
		item.FixedLocale,
	)
	if mode == model.MenuItemLocalizationModeFixedLocale && fixedLocaleTrimmed == "" {
		return errs.InvalidArgument(itemPath, "fixedLocale is required when localization mode is fixed locale")
	}

	// Recursively validate children
	if len(item.Children) > 0 {
		if err := s.validateMenuItems(item.Children, depth+1); err != nil {
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
			return errs.InvalidArgument(itemPath, fmt.Sprintf("targetId or targetSlug is required for %s link type", item.LinkType.String()))
		}
		return nil
	}
	if item.Url == nil || strings.TrimSpace(*item.Url) == "" {
		return errs.InvalidArgument(itemPath, "url is required for custom link type")
	}
	urlString := strings.TrimSpace(*item.Url)
	if len(urlString) > menuURLMaxLength {
		return errs.InvalidArgument(itemPath, fmt.Sprintf("url must be at most %d characters", menuURLMaxLength))
	}
	if strings.HasPrefix(urlString, "/") {
		return nil
	}
	if _, err := url.ParseRequestURI(urlString); err != nil {
		return errs.InvalidArgument(itemPath, "url must be a valid URL or relative path")
	}
	return nil
}

// ==================== Helper Methods ====================

func (s *MenuService) toProtoMenu(m *model.Menu) (*managev1.Menu, error) {
	menu := &managev1.Menu{
		Id:        m.ID,
		Name:      m.Name,
		CreatedAt: timestamppb.New(m.CreatedAt),
		UpdatedAt: timestamppb.New(m.UpdatedAt),
	}

	// Parse items from JSON
	if len(m.Items) > 0 {
		var items []model.MenuItem
		if err := json.Unmarshal(m.Items, &items); err != nil {
			return nil, fmt.Errorf("failed to parse menu items: %w", err)
		}
		menu.Items = s.modelItemsToProto(items)
	} else {
		menu.Items = []*managev1.MenuItem{}
	}

	return menu, nil
}

func (s *MenuService) modelItemsToProto(items []model.MenuItem) []*managev1.MenuItem {
	result := make([]*managev1.MenuItem, len(items))
	for i, item := range items {
		result[i] = s.modelItemToProto(&item)
	}
	return result
}

func (s *MenuService) modelItemToProto(item *model.MenuItem) *managev1.MenuItem {
	localizationMode, fixedLocale := CanonicalizeItemLocalization(item.LocalizationMode, item.FixedLocale)
	protoItem := &managev1.MenuItem{
		Id:               item.ID,
		Label:            item.Label,
		LinkType:         s.linkTypeToProto(item.LinkType),
		LocalizationMode: s.localizationModeToProto(localizationMode),
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
			Mode:  s.visibilityModeToProto(item.Visibility.Mode),
			Roles: item.Visibility.Roles,
		}
	}
	if fixedLocale != nil {
		protoItem.FixedLocale = fixedLocale
	}

	// Recursive children
	if len(item.Children) > 0 {
		protoItem.Children = s.modelItemsToProto(item.Children)
	}

	return protoItem
}

func (s *MenuService) linkTypeToProto(linkType string) managev1.MenuLinkType {
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

func (s *MenuService) visibilityModeToProto(mode string) managev1.MenuVisibilityMode {
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

func (s *MenuService) localizationModeToProto(mode *string) managev1.MenuItemLocalizationMode {
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

func (s *MenuService) protoItemsToJSON(items []*managev1.MenuItem) (json.RawMessage, error) {
	modelItems := make([]model.MenuItem, len(items))
	for i, item := range items {
		modelItems[i] = s.protoItemToModel(item)
	}
	return json.Marshal(modelItems)
}

func (s *MenuService) protoItemToModel(item *managev1.MenuItem) model.MenuItem {
	localizationMode, fixedLocale := CanonicalizeItemLocalization(
		s.protoLocalizationModeToStringPtr(item.LocalizationMode),
		item.FixedLocale,
	)
	modelItem := model.MenuItem{
		ID:               item.Id,
		Label:            item.Label,
		LinkType:         s.protoLinkTypeToString(item.LinkType),
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
		roles, _ := normalizeUserRoleList("visibility.roles", item.Visibility.Roles)
		modelItem.Visibility = &model.MenuVisibility{
			Mode:  s.protoVisibilityModeToString(item.Visibility.Mode),
			Roles: roles,
		}
	}

	// Recursive children
	if len(item.Children) > 0 {
		modelItem.Children = make([]model.MenuItem, len(item.Children))
		for i, child := range item.Children {
			modelItem.Children[i] = s.protoItemToModel(child)
		}
	}

	return modelItem
}

func (s *MenuService) protoLinkTypeToString(linkType managev1.MenuLinkType) string {
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

func (s *MenuService) protoVisibilityModeToString(mode managev1.MenuVisibilityMode) string {
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

func (s *MenuService) protoLocalizationModeToStringPtr(
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
