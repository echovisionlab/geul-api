package menu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"reflect"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// Menu validation constants
const (
	menuNameMaxLength      = 100
	menuItemLabelMaxLength = 255
	menuMaxNestingDepth    = 5
	menuURLMaxLength       = 2048
)

// A requested item tree uses the stored Item shape with two request-only
// readings: an empty LinkType or Visibility.Mode is unspecified, and a value
// outside the stored set is unsupported. validateMenuItems rejects what a
// Menu cannot store and storedItems canonicalizes the rest.

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
func validateMenuItems(items []Item, depth int) error {
	if depth > menuMaxNestingDepth {
		return domainerr.InvalidField("items", fmt.Sprintf("menu nesting depth exceeds maximum of %d levels", menuMaxNestingDepth))
	}

	for i := range items {
		if err := validateMenuItem(&items[i], i, depth); err != nil {
			return err
		}
	}
	return nil
}

// validateMenuItem validates a single menu item
func validateMenuItem(item *Item, index, depth int) error {
	itemPath := fmt.Sprintf("item[%d]", index)
	if depth > 0 {
		itemPath = fmt.Sprintf("child at depth %d, %s", depth, itemPath)
	}

	if strings.TrimSpace(item.ID) == "" {
		return domainerr.InvalidField(itemPath, "id cannot be empty")
	}
	label := strings.TrimSpace(item.Label)
	if label == "" {
		return domainerr.InvalidField(itemPath, "label cannot be empty")
	}
	if len(label) > menuItemLabelMaxLength {
		return domainerr.InvalidField(itemPath, fmt.Sprintf("label must be at most %d characters", menuItemLabelMaxLength))
	}

	if item.LinkType == "" {
		return domainerr.InvalidField(itemPath, "linkType must be specified")
	}
	if !knownLinkType(item.LinkType) {
		return domainerr.InvalidField(itemPath, "invalid linkType")
	}
	if err := validateMenuItemLink(item, itemPath); err != nil {
		return err
	}
	if err := validateMenuItemVisibility(item.Visibility, itemPath); err != nil {
		return err
	}
	if err := validateMenuItemLocalization(item, itemPath); err != nil {
		return err
	}

	if len(item.Children) > 0 {
		if err := validateMenuItems(item.Children, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func knownLinkType(linkType LinkType) bool {
	switch linkType {
	case LinkCustom, LinkPage, LinkCategory, LinkTag, LinkSeries:
		return true
	default:
		return false
	}
}

func validateMenuItemLink(item *Item, itemPath string) error {
	if item.LinkType != LinkCustom {
		hasTargetID := item.TargetID != nil && strings.TrimSpace(*item.TargetID) != ""
		hasTargetSlug := item.TargetSlug != nil && strings.TrimSpace(*item.TargetSlug) != ""
		if !hasTargetID && !hasTargetSlug {
			// The message names the API enum value, as the request spelled it.
			return domainerr.InvalidField(itemPath, fmt.Sprintf("targetId or targetSlug is required for MENU_LINK_TYPE_%s link type", strings.ToUpper(string(item.LinkType))))
		}
		return nil
	}
	if item.URL == nil || strings.TrimSpace(*item.URL) == "" {
		return domainerr.InvalidField(itemPath, "url is required for custom link type")
	}
	urlString := strings.TrimSpace(*item.URL)
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

func validateMenuItemVisibility(visibility *Visibility, itemPath string) error {
	if visibility == nil {
		return nil
	}
	switch visibility.Mode {
	case "", VisibilityAll, VisibilityAuthenticated, VisibilityGuest, VisibilityRoles:
	default:
		return domainerr.InvalidField(itemPath, "invalid visibility mode")
	}
	roles, err := normalizeUserRoleList(itemPath+".visibility.roles", visibility.Roles)
	if err != nil {
		return err
	}
	if visibility.Mode == VisibilityRoles && len(roles) == 0 {
		return domainerr.InvalidField(itemPath, "at least one role is required when visibility mode is 'roles'")
	}
	return nil
}

func validateMenuItemLocalization(item *Item, itemPath string) error {
	fixedLocaleTrimmed := ""
	if item.FixedLocale != nil {
		fixedLocaleTrimmed = strings.TrimSpace(*item.FixedLocale)
		if fixedLocaleTrimmed != "" && NormalizeItemFixedLocale(item.FixedLocale) == nil {
			return domainerr.InvalidField(itemPath, "fixedLocale must be a supported locale")
		}
	}
	mode := NormalizeItemLocalizationMode(item.LocalizationMode, item.FixedLocale)
	if mode == LocalizationFixedLocale && fixedLocaleTrimmed == "" {
		return domainerr.InvalidField(itemPath, "fixedLocale is required when localization mode is fixed locale")
	}
	return nil
}

// storedItems canonicalizes a validated requested tree into the tree a Menu
// stores: an explicit, valid localization, normalized roles and a stored
// visibility mode.
func storedItems(items []Item) []Item {
	stored := make([]Item, len(items))
	for i := range items {
		stored[i] = storedItem(items[i])
	}
	return stored
}

func storedItem(item Item) Item {
	localizationMode, fixedLocale := CanonicalizeItemLocalization(item.LocalizationMode, item.FixedLocale)
	stored := Item{
		ID: item.ID, Label: item.Label, LinkType: item.LinkType,
		URL: item.URL, TargetID: item.TargetID, TargetSlug: item.TargetSlug, OpenInNewTab: item.OpenInNewTab,
		LocalizationMode: localizationMode, FixedLocale: fixedLocale,
	}
	if !knownLinkType(stored.LinkType) {
		stored.LinkType = LinkCustom
	}
	if item.Visibility != nil {
		// validateMenuItems has already rejected unknown roles.
		roles, _ := normalizeUserRoleList("visibility.roles", item.Visibility.Roles)
		stored.Visibility = &Visibility{Mode: storedVisibilityMode(item.Visibility.Mode), Roles: roles}
	}
	if len(item.Children) > 0 {
		stored.Children = storedItems(item.Children)
	}
	return stored
}

func storedVisibilityMode(mode VisibilityMode) VisibilityMode {
	switch mode {
	case VisibilityAuthenticated, VisibilityGuest, VisibilityRoles:
		return mode
	default:
		return VisibilityAll
	}
}

// requestedItems reads a stored tree back as a request would carry it, the
// form validateMenuItems checks: a stored link type outside the known set
// is unspecified, and the localization is canonical.
func requestedItems(items []Item) []Item {
	requested := make([]Item, len(items))
	for i := range items {
		item := items[i]
		item.LocalizationMode, item.FixedLocale = CanonicalizeItemLocalization(item.LocalizationMode, item.FixedLocale)
		if !knownLinkType(item.LinkType) {
			item.LinkType = ""
		}
		if item.Visibility != nil {
			item.Visibility = &Visibility{Mode: storedVisibilityMode(item.Visibility.Mode), Roles: item.Visibility.Roles}
		}
		if len(item.Children) > 0 {
			item.Children = requestedItems(item.Children)
		} else {
			item.Children = nil
		}
		requested[i] = item
	}
	return requested
}

// encodeRequestedItems canonicalizes and encodes a validated requested tree.
func encodeRequestedItems(items []Item) ([]byte, error) {
	body, err := json.Marshal(storedItems(items))
	if err != nil {
		return nil, domainerr.New(domainerr.Invalid, "", fmt.Sprintf("failed to convert menu items: %s", err.Error()))
	}
	return body, nil
}

// decodeSourceItems decodes a stored item tree; an empty document has none.
func decodeSourceItems(body []byte) ([]Item, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var items []Item
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// menuOf decodes the item tree of root.
func menuOf(root Root) (Menu, error) {
	menu := Menu{
		ID: root.ID, SourceLocale: root.SourceLocale, Name: root.Name,
		CreatedAt: root.CreatedAt, UpdatedAt: root.UpdatedAt, Items: []Item{},
	}
	if len(root.ItemsJSON) > 0 {
		var items []Item
		if err := json.Unmarshal(root.ItemsJSON, &items); err != nil {
			return Menu{}, fmt.Errorf("failed to parse menu items: %w", err)
		}
		if items != nil {
			menu.Items = items
		}
	}
	return menu, nil
}

func collectMenuTargetReferences(items []Item) []TargetReference {
	refs := make([]TargetReference, 0)
	for i := range items {
		item := &items[i]
		if ref, ok := menuItemTarget(item); ok {
			refs = append(refs, ref)
		}
		refs = append(refs, collectMenuTargetReferences(item.Children)...)
	}
	return refs
}

func menuItemTarget(item *Item) (TargetReference, bool) {
	switch item.LinkType {
	case LinkPage, LinkCategory, LinkTag, LinkSeries:
	default:
		return TargetReference{}, false
	}
	ref := TargetReference{LinkType: item.LinkType}
	if item.TargetID != nil {
		ref.ID = strings.TrimSpace(*item.TargetID)
	}
	if ref.ID == "" && item.TargetSlug != nil {
		ref.Slug = strings.TrimSpace(*item.TargetSlug)
	}
	return ref, ref.ID != "" || ref.Slug != ""
}

// semanticMenuItemsEqual treats undecodable items as changed. It runs after
// menuUpdateAction, whose topology comparison already logged a decode failure
// of the same stored and requested items.
func semanticMenuItemsEqual(stored, requested []byte) bool {
	var storedItems []Item
	if len(stored) > 0 {
		if err := json.Unmarshal(stored, &storedItems); err != nil {
			return false
		}
	}
	var requestedItems []Item
	if len(requested) > 0 {
		if err := json.Unmarshal(requested, &requestedItems); err != nil {
			return false
		}
	}
	return reflect.DeepEqual(storedItems, requestedItems)
}

func menuItemTopologyEqual(ctx context.Context, menuID string, stored, requested []byte) bool {
	var storedItems []Item
	if len(stored) != 0 {
		if err := json.Unmarshal(stored, &storedItems); err != nil {
			slog.ErrorContext(ctx, "Stored Menu items are not valid JSON; requiring Manage permission", "domain", "menu", "operation", "compare_stored_topology", "outcome", "failed", "menu_id", menuID, failurecause.Attr(err))
			return false
		}
	}
	var requestedItems []Item
	if len(requested) != 0 {
		if err := json.Unmarshal(requested, &requestedItems); err != nil {
			slog.WarnContext(ctx, "Encoded Menu request items are not valid JSON; requiring Manage permission", "domain", "menu", "operation", "compare_requested_topology", "outcome", "failed", "menu_id", menuID, failurecause.Attr(err))
			return false
		}
	}
	return reflectMenuItemTopology(storedItems, "") == reflectMenuItemTopology(requestedItems, "")
}

func reflectMenuItemTopology(items []Item, parentID string) string {
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
