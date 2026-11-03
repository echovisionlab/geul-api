package menu

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConnectHandler serves the Menu management RPCs.
type ConnectHandler struct {
	menus *Service
}

// NewConnectHandler returns the Connect adapter of menus.
func NewConnectHandler(menus *Service) *ConnectHandler {
	return &ConnectHandler{menus: menus}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func respondMenu(menu Menu, err error) (*connect.Response[managev1.Menu], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(menuMessage(menu)), nil
}

func (h *ConnectHandler) GetMenuById(ctx context.Context, req *connect.Request[managev1.GetMenuByIdRequest]) (*connect.Response[managev1.Menu], error) {
	return respondMenu(h.menus.GetMenu(ctx, GetMenuQuery{ID: req.Msg.Id}))
}

func (h *ConnectHandler) ListMenus(ctx context.Context, req *connect.Request[managev1.ListMenusRequest]) (*connect.Response[managev1.ListMenusResponse], error) {
	page, err := h.menus.ListMenus(ctx, ListMenusQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	menus := make([]*managev1.Menu, 0, len(page.Menus))
	for _, menu := range page.Menus {
		menus = append(menus, menuMessage(menu))
	}
	return connect.NewResponse(&managev1.ListMenusResponse{
		Menus: menus,
		Pagination: &commonv1.PaginationResponse{
			Total: page.Total, Limit: page.Limit, Offset: page.Offset, HasMore: page.HasMore,
		},
	}), nil
}

func (h *ConnectHandler) CreateMenu(ctx context.Context, req *connect.Request[managev1.CreateMenuRequest]) (*connect.Response[managev1.Menu], error) {
	return respondMenu(h.menus.CreateMenu(ctx, CreateMenuCommand{Name: req.Msg.Name, Items: requestItems(req.Msg.Items)}))
}

func (h *ConnectHandler) UpdateMenu(ctx context.Context, req *connect.Request[managev1.UpdateMenuRequest]) (*connect.Response[managev1.Menu], error) {
	command := UpdateMenuCommand{ID: req.Msg.Id, Name: req.Msg.Name}
	if req.Msg.Items != nil {
		items := requestItems(req.Msg.Items.Items)
		command.Items = &items
	}
	return respondMenu(h.menus.UpdateMenu(ctx, command))
}

func (h *ConnectHandler) DeleteMenu(ctx context.Context, req *connect.Request[managev1.DeleteMenuRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	err := h.menus.DeleteMenu(ctx, DeleteMenuCommand{ID: req.Msg.Id})
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

// menuMessage is the API form of menu.
func menuMessage(menu Menu) *managev1.Menu {
	return &managev1.Menu{
		Id:           menu.ID,
		SourceLocale: menu.SourceLocale,
		Name:         menu.Name,
		Items:        itemMessages(menu.Items),
		CreatedAt:    timestamppb.New(menu.CreatedAt),
		UpdatedAt:    timestamppb.New(menu.UpdatedAt),
	}
}

func itemMessages(items []Item) []*managev1.MenuItem {
	result := make([]*managev1.MenuItem, len(items))
	for i := range items {
		result[i] = itemMessage(&items[i])
	}
	return result
}

func itemMessage(item *Item) *managev1.MenuItem {
	localizationMode, fixedLocale := CanonicalizeItemLocalization(item.LocalizationMode, item.FixedLocale)
	message := &managev1.MenuItem{
		Id:               item.ID,
		Label:            item.Label,
		LinkType:         linkTypeToProto(item.LinkType),
		LocalizationMode: localizationModeToProto(localizationMode),
		Url:              item.URL,
		TargetId:         item.TargetID,
		TargetSlug:       item.TargetSlug,
		OpenInNewTab:     item.OpenInNewTab,
		FixedLocale:      fixedLocale,
	}
	if item.Visibility != nil {
		message.Visibility = &managev1.MenuVisibility{
			Mode:  visibilityModeToProto(item.Visibility.Mode),
			Roles: item.Visibility.Roles,
		}
	}
	if len(item.Children) > 0 {
		message.Children = itemMessages(item.Children)
	}
	return message
}

func linkTypeToProto(linkType LinkType) managev1.MenuLinkType {
	switch linkType {
	case LinkCustom:
		return managev1.MenuLinkType_MENU_LINK_TYPE_CUSTOM
	case LinkPage:
		return managev1.MenuLinkType_MENU_LINK_TYPE_PAGE
	case LinkCategory:
		return managev1.MenuLinkType_MENU_LINK_TYPE_CATEGORY
	case LinkTag:
		return managev1.MenuLinkType_MENU_LINK_TYPE_TAG
	case LinkSeries:
		return managev1.MenuLinkType_MENU_LINK_TYPE_SERIES
	default:
		return managev1.MenuLinkType_MENU_LINK_TYPE_UNSPECIFIED
	}
}

func visibilityModeToProto(mode VisibilityMode) managev1.MenuVisibilityMode {
	switch mode {
	case VisibilityAuthenticated:
		return managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_AUTHENTICATED
	case VisibilityGuest:
		return managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_GUEST
	case VisibilityRoles:
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
	case LocalizationFixedLocale:
		return managev1.MenuItemLocalizationMode_MENU_ITEM_LOCALIZATION_MODE_FIXED_LOCALE
	case LocalizationTranslated:
		return managev1.MenuItemLocalizationMode_MENU_ITEM_LOCALIZATION_MODE_TRANSLATED
	default:
		return managev1.MenuItemLocalizationMode_MENU_ITEM_LOCALIZATION_MODE_UNSPECIFIED
	}
}

// requestItems reads a requested item tree. An unspecified enum is empty
// and a value outside the API's set keeps its number, so validation names
// both as the API did.
func requestItems(items []*managev1.MenuItem) []Item {
	result := make([]Item, len(items))
	for i, item := range items {
		if item != nil {
			result[i] = requestItem(item)
		}
	}
	return result
}

func requestItem(item *managev1.MenuItem) Item {
	result := Item{
		ID: item.Id, Label: item.Label, LinkType: linkTypeFromProto(item.LinkType),
		URL: item.Url, TargetID: item.TargetId, TargetSlug: item.TargetSlug, OpenInNewTab: item.OpenInNewTab,
		LocalizationMode: localizationModeFromProto(item.LocalizationMode), FixedLocale: item.FixedLocale,
	}
	if item.Visibility != nil {
		result.Visibility = &Visibility{Mode: visibilityModeFromProto(item.Visibility.Mode), Roles: item.Visibility.Roles}
	}
	if len(item.Children) > 0 {
		result.Children = requestItems(item.Children)
	}
	return result
}

func linkTypeFromProto(linkType managev1.MenuLinkType) LinkType {
	switch linkType {
	case managev1.MenuLinkType_MENU_LINK_TYPE_UNSPECIFIED:
		return ""
	case managev1.MenuLinkType_MENU_LINK_TYPE_CUSTOM:
		return LinkCustom
	case managev1.MenuLinkType_MENU_LINK_TYPE_PAGE:
		return LinkPage
	case managev1.MenuLinkType_MENU_LINK_TYPE_CATEGORY:
		return LinkCategory
	case managev1.MenuLinkType_MENU_LINK_TYPE_TAG:
		return LinkTag
	case managev1.MenuLinkType_MENU_LINK_TYPE_SERIES:
		return LinkSeries
	default:
		return LinkType(linkType.String())
	}
}

func visibilityModeFromProto(mode managev1.MenuVisibilityMode) VisibilityMode {
	switch mode {
	case managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_UNSPECIFIED:
		return ""
	case managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_ALL:
		return VisibilityAll
	case managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_AUTHENTICATED:
		return VisibilityAuthenticated
	case managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_GUEST:
		return VisibilityGuest
	case managev1.MenuVisibilityMode_MENU_VISIBILITY_MODE_ROLES:
		return VisibilityRoles
	default:
		return VisibilityMode(mode.String())
	}
}

func localizationModeFromProto(mode managev1.MenuItemLocalizationMode) *string {
	var value string
	switch mode {
	case managev1.MenuItemLocalizationMode_MENU_ITEM_LOCALIZATION_MODE_FIXED_LOCALE:
		value = LocalizationFixedLocale
	case managev1.MenuItemLocalizationMode_MENU_ITEM_LOCALIZATION_MODE_TRANSLATED:
		value = LocalizationTranslated
	default:
		return nil
	}
	return &value
}

// InternalConnectHandler serves the Menu collaboration RPCs editor-collab
// calls.
type InternalConnectHandler struct {
	menus *Service
}

// NewInternalConnectHandler returns the internal Connect adapter of menus.
func NewInternalConnectHandler(menus *Service) *InternalConnectHandler {
	return &InternalConnectHandler{menus: menus}
}

func (h *InternalConnectHandler) LoadDocument(ctx context.Context, req *connect.Request[intrav1.LoadMenuDocumentRequest]) (*connect.Response[intrav1.LoadMenuDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	snapshot, err := h.menus.LoadMenuDocument(ctx, LoadMenuDocumentQuery{MenuID: req.Msg.MenuId, Locale: req.Msg.Locale})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&intrav1.LoadMenuDocumentResponse{
		SourceLocale:     snapshot.SourceLocale,
		Locale:           snapshot.Locale,
		LocaleExists:     snapshot.LocaleExists,
		Name:             snapshot.Name,
		Items:            collaborationItemMessages(snapshot.Items),
		SourceLabels:     cloneMenuLabels(snapshot.sourceLabels),
		RequestedLabels:  cloneMenuLabels(snapshot.Labels),
		DocumentRevision: snapshot.DocumentRevision,
		TargetRevision:   cloneStringPointer(snapshot.TargetRevision),
	}), nil
}

func (h *InternalConnectHandler) SaveDocument(ctx context.Context, req *connect.Request[intrav1.SaveMenuDocumentRequest]) (*connect.Response[intrav1.SaveMenuDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	saved, err := h.menus.SaveMenuDocument(ctx, SaveMenuDocumentCommand{
		MenuID: req.Msg.MenuId, Locale: req.Msg.Locale,
		ExpectedDocumentRevision: req.Msg.ExpectedDocumentRevision, ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
		Name: req.Msg.Name, Items: collaborationItems(req.Msg.Items), RequestedLabels: req.Msg.RequestedLabels,
		ContributorMemberIDs: req.Msg.ContributorMemberIds,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	// A save that passed validation has the canonical locale it requested.
	return connect.NewResponse(&intrav1.SaveMenuDocumentResponse{
		Locale: req.Msg.Locale, DocumentRevision: saved.DocumentRevision, TargetRevision: saved.TargetRevision,
	}), nil
}

func collaborationItemMessages(items []AIDocumentItem) []*intrav1.MenuCollaborationItem {
	result := make([]*intrav1.MenuCollaborationItem, len(items))
	for index, item := range items {
		result[index] = &intrav1.MenuCollaborationItem{
			Id: item.ID, LinkType: item.LinkType,
			Url: cloneStringPointer(item.URL), TargetId: cloneStringPointer(item.TargetID),
			TargetSlug: cloneStringPointer(item.TargetSlug), OpenInNewTab: cloneBoolPointer(item.OpenInNewTab),
			VisibilityMode:   cloneStringPointer(item.VisibilityMode),
			VisibilityRoles:  append([]string(nil), item.VisibilityRoles...),
			LocalizationMode: cloneStringPointer(item.LocalizationMode), FixedLocale: cloneStringPointer(item.FixedLocale),
			Children: collaborationItemMessages(item.Children),
		}
	}
	return result
}

func collaborationItems(items []*intrav1.MenuCollaborationItem) []AIDocumentItem {
	result := make([]AIDocumentItem, len(items))
	for index, item := range items {
		if item == nil {
			continue
		}
		result[index] = AIDocumentItem{
			ID: item.Id, LinkType: item.LinkType,
			URL: cloneStringPointer(item.Url), TargetID: cloneStringPointer(item.TargetId),
			TargetSlug: cloneStringPointer(item.TargetSlug), OpenInNewTab: cloneBoolPointer(item.OpenInNewTab),
			VisibilityMode:   cloneStringPointer(item.VisibilityMode),
			VisibilityRoles:  append([]string(nil), item.VisibilityRoles...),
			LocalizationMode: cloneStringPointer(item.LocalizationMode), FixedLocale: cloneStringPointer(item.FixedLocale),
			Children: collaborationItems(item.Children),
		}
	}
	return result
}
