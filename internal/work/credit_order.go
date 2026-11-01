package work

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// OrderItem is one item of a Work's flattened credit order: a group, or a
// credit in GroupID's group (nil when ungrouped).
type OrderItem struct {
	Kind    CreditItemKind
	ID      string
	GroupID *string
}

type workCreditOrderEntry struct {
	kind    CreditItemKind
	id      string
	groupID *string
}

func loadWorkCreditRows(ctx context.Context, store Store, workID string) ([]CreditGroup, []Credit, []workCreditOrderEntry, error) {
	groups, credits, err := store.CreditRows(ctx, workID)
	if err != nil {
		return nil, nil, nil, err
	}

	order, err := buildWorkCreditOrder(groups, credits)
	if err != nil {
		return nil, nil, nil, err
	}
	return groups, credits, order, nil
}

// buildWorkCreditOrder merges the group and credit rows when their sort orders
// describe a valid canonical flattened list.
func buildWorkCreditOrder(groups []CreditGroup, credits []Credit) ([]workCreditOrderEntry, error) {
	merged := make([]workCreditOrderEntry, 0, len(groups)+len(credits))
	groupSortOrders := make(map[string]int, len(groups))
	creditSortOrders := make(map[string]int, len(credits))
	for _, group := range groups {
		groupSortOrders[group.ID] = group.SortOrder
		merged = append(merged, workCreditOrderEntry{
			kind: CreditItemGroup,
			id:   group.ID,
		})
	}
	for _, credit := range credits {
		creditSortOrders[credit.ID] = credit.SortOrder
		merged = append(merged, workCreditOrderEntry{
			kind:    CreditItemCredit,
			id:      credit.ID,
			groupID: cloneGroupID(credit.GroupID),
		})
	}
	sort.Slice(merged, func(i, j int) bool {
		left := orderEntrySortOrder(merged[i], groupSortOrders, creditSortOrders)
		right := orderEntrySortOrder(merged[j], groupSortOrders, creditSortOrders)
		if left != right {
			return left < right
		}
		if merged[i].kind != merged[j].kind {
			return merged[i].kind < merged[j].kind
		}
		return merged[i].id < merged[j].id
	})
	if !validPersistedWorkCreditOrder(merged, groups, credits, groupSortOrders, creditSortOrders) {
		return nil, errors.New("persisted Work credit order is invalid")
	}
	return merged, nil
}

func orderEntrySortOrder(entry workCreditOrderEntry, groupSortOrders, creditSortOrders map[string]int) int {
	if entry.kind == CreditItemGroup {
		return groupSortOrders[entry.id]
	}
	return creditSortOrders[entry.id]
}

func validPersistedWorkCreditOrder(
	order []workCreditOrderEntry,
	groups []CreditGroup,
	credits []Credit,
	groupSortOrders map[string]int,
	creditSortOrders map[string]int,
) bool {
	if len(order) != len(groups)+len(credits) {
		return false
	}
	groupIDs := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		groupIDs[group.ID] = struct{}{}
	}
	creditIDs := make(map[string]struct{}, len(credits))
	for _, credit := range credits {
		creditIDs[credit.ID] = struct{}{}
	}

	for index, entry := range order {
		sortOrder := orderEntrySortOrder(entry, groupSortOrders, creditSortOrders)
		if sortOrder != index+1 {
			return false
		}
	}

	activeGroup := ""
	seenGroups := make(map[string]struct{}, len(groups))
	seenCredits := make(map[string]struct{}, len(credits))
	for _, entry := range order {
		switch entry.kind {
		case CreditItemGroup:
			if _, ok := groupIDs[entry.id]; !ok {
				return false
			}
			if _, duplicate := seenGroups[entry.id]; duplicate {
				return false
			}
			seenGroups[entry.id] = struct{}{}
			activeGroup = entry.id
		case CreditItemCredit:
			if _, ok := creditIDs[entry.id]; !ok {
				return false
			}
			if _, duplicate := seenCredits[entry.id]; duplicate {
				return false
			}
			seenCredits[entry.id] = struct{}{}
			if entry.groupID == nil {
				activeGroup = ""
				continue
			}
			if _, ok := groupIDs[*entry.groupID]; !ok || activeGroup != *entry.groupID {
				return false
			}
		default:
			return false
		}
	}
	return len(seenGroups) == len(groups) && len(seenCredits) == len(credits)
}

func moveWorkCreditGroupOrder(order []workCreditOrderEntry, groupID string, after, before *OrderItem) ([]workCreditOrderEntry, bool) {
	start := -1
	for index, entry := range order {
		if entry.kind == CreditItemGroup && entry.id == groupID {
			start = index
			break
		}
	}
	if start < 0 {
		return order, false
	}
	if workCreditGroupAnchorInSection(order, before, groupID) || workCreditGroupAnchorInSection(order, after, groupID) {
		return order, false
	}
	end := start + 1
	for end < len(order) && order[end].kind == CreditItemCredit &&
		order[end].groupID != nil && *order[end].groupID == groupID {
		end++
	}
	block := append([]workCreditOrderEntry(nil), order[start:end]...)
	remaining := make([]workCreditOrderEntry, 0, len(order)-len(block))
	remaining = append(remaining, order[:start]...)
	remaining = append(remaining, order[end:]...)

	position, hasPosition := workCreditGroupAnchorPosition(remaining, before, true)
	if !hasPosition {
		position, hasPosition = workCreditGroupAnchorPosition(remaining, after, false)
	}
	if !hasPosition {
		position = len(remaining)
	}
	result := make([]workCreditOrderEntry, 0, len(order))
	result = append(result, remaining[:position]...)
	result = append(result, block...)
	result = append(result, remaining[position:]...)
	return result, !sameWorkCreditOrder(order, result)
}

func workCreditGroupAnchorInSection(order []workCreditOrderEntry, anchor *OrderItem, groupID string) bool {
	if anchor == nil || anchor.ID == "" {
		return false
	}
	index := findWorkCreditOrderEntry(order, anchor.Kind, anchor.ID)
	if index < 0 {
		return false
	}
	entry := order[index]
	if entry.kind == CreditItemGroup {
		return entry.id == groupID
	}
	return entry.kind == CreditItemCredit &&
		entry.groupID != nil && *entry.groupID == groupID
}

func workCreditGroupAnchorPosition(order []workCreditOrderEntry, anchor *OrderItem, before bool) (int, bool) {
	if anchor == nil || anchor.ID == "" {
		return 0, false
	}
	index := findWorkCreditOrderEntry(order, anchor.Kind, anchor.ID)
	if index < 0 {
		return 0, false
	}
	entry := order[index]
	if entry.kind == CreditItemGroup {
		if before {
			return index, true
		}
		return workCreditGroupSectionEnd(order, index, entry.id), true
	}
	if entry.kind != CreditItemCredit {
		return 0, false
	}
	if entry.groupID != nil {
		groupIndex := findWorkCreditOrderEntry(order, CreditItemGroup, *entry.groupID)
		if groupIndex < 0 {
			return 0, false
		}
		if before {
			return groupIndex, true
		}
		return workCreditGroupSectionEnd(order, groupIndex, *entry.groupID), true
	}
	if before {
		return index, true
	}
	return index + 1, true
}

func workCreditGroupSectionEnd(order []workCreditOrderEntry, groupIndex int, groupID string) int {
	index := groupIndex + 1
	for index < len(order) && order[index].kind == CreditItemCredit &&
		order[index].groupID != nil && *order[index].groupID == groupID {
		index++
	}
	return index
}

func moveWorkCreditOrder(order []workCreditOrderEntry, creditID string, targetGroupID *string, after, before *OrderItem) ([]workCreditOrderEntry, bool) {
	start := findWorkCreditOrderEntry(order, CreditItemCredit, creditID)
	if start < 0 {
		return order, false
	}
	entry := order[start]
	entry.groupID = cloneGroupID(targetGroupID)
	remaining := make([]workCreditOrderEntry, 0, len(order)-1)
	remaining = append(remaining, order[:start]...)
	remaining = append(remaining, order[start+1:]...)

	position, hasPosition := workCreditCreditAnchorPosition(remaining, before, targetGroupID)
	if !hasPosition {
		position, hasPosition = workCreditCreditAnchorPosition(remaining, after, targetGroupID)
		if hasPosition {
			position++
		}
	}
	if !hasPosition {
		position = workCreditGroupSectionEndForTarget(remaining, targetGroupID)
	}
	result := make([]workCreditOrderEntry, 0, len(order))
	result = append(result, remaining[:position]...)
	result = append(result, entry)
	result = append(result, remaining[position:]...)
	return result, !sameWorkCreditOrder(order, result)
}

func workCreditCreditAnchorPosition(order []workCreditOrderEntry, anchor *OrderItem, targetGroupID *string) (int, bool) {
	if anchor == nil || anchor.Kind != CreditItemCredit || anchor.ID == "" {
		return 0, false
	}
	index := findWorkCreditOrderEntry(order, anchor.Kind, anchor.ID)
	if index < 0 || !sameOptionalString(order[index].groupID, targetGroupID) {
		return 0, false
	}
	return index, true
}

func workCreditGroupSectionEndForTarget(order []workCreditOrderEntry, groupID *string) int {
	if groupID == nil {
		return len(order)
	}
	groupIndex := findWorkCreditOrderEntry(order, CreditItemGroup, *groupID)
	if groupIndex < 0 {
		return len(order)
	}
	return workCreditGroupSectionEnd(order, groupIndex, *groupID)
}

func findWorkCreditOrderEntry(order []workCreditOrderEntry, kind CreditItemKind, id string) int {
	for index, entry := range order {
		if entry.kind == kind && entry.id == id {
			return index
		}
	}
	return -1
}

// validateWorkCreditMoveAnchors rejects anchors that resolve to another item
// kind, Work, or credit group. An ID that no longer exists is a deleted anchor,
// so the move helpers can deterministically fall back to the surviving anchor
// or the target section's end.
func validateWorkCreditMoveAnchors(
	ctx context.Context,
	store Store,
	order []workCreditOrderEntry,
	movingKind CreditItemKind,
	movingID string,
	targetGroupID *string,
	after, before *OrderItem,
) error {
	for _, anchor := range []*OrderItem{after, before} {
		if anchor == nil {
			continue
		}
		if strings.TrimSpace(anchor.ID) == "" {
			return domainerr.InvalidField("anchor.id", "must identify an item")
		}
		if anchor.Kind != CreditItemGroup &&
			anchor.Kind != CreditItemCredit {
			return domainerr.InvalidField("anchor.kind", "must identify a group or credit")
		}
		if anchor.Kind == movingKind && anchor.ID == movingID {
			return domainerr.InvalidField("anchor", "cannot reference the moved item")
		}

		index := findWorkCreditOrderEntry(order, anchor.Kind, anchor.ID)
		if index >= 0 {
			if movingKind == CreditItemCredit {
				entry := order[index]
				if anchor.Kind != CreditItemCredit ||
					!sameOptionalString(entry.groupID, targetGroupID) {
					return domainerr.InvalidField("anchor", "must be a credit in the target group")
				}
			}
			continue
		}

		parsedID, err := uuid.Parse(anchor.ID)
		if err != nil {
			return domainerr.InvalidField("anchor.id", "must be a valid item ID")
		}
		exists, err := store.CreditItemExists(ctx, parsedID.String())
		if err != nil {
			return err
		}
		if exists {
			return domainerr.InvalidField("anchor", "must reference a compatible item in this Work")
		}
	}
	return nil
}

func sameWorkCreditOrder(left, right []workCreditOrderEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].kind != right[index].kind || left[index].id != right[index].id ||
			!sameOptionalString(left[index].groupID, right[index].groupID) {
			return false
		}
	}
	return true
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func insertWorkCreditAtGroupEnd(order []workCreditOrderEntry, entry workCreditOrderEntry) []workCreditOrderEntry {
	position := workCreditGroupSectionEndForTarget(order, entry.groupID)
	result := make([]workCreditOrderEntry, 0, len(order)+1)
	result = append(result, order[:position]...)
	result = append(result, entry)
	result = append(result, order[position:]...)
	return result
}

func removeWorkCreditOrderEntry(order []workCreditOrderEntry, kind CreditItemKind, id string) []workCreditOrderEntry {
	index := findWorkCreditOrderEntry(order, kind, id)
	if index < 0 {
		return order
	}
	result := make([]workCreditOrderEntry, 0, len(order)-1)
	result = append(result, order[:index]...)
	result = append(result, order[index+1:]...)
	return result
}

func workCreditOrderItems(order []workCreditOrderEntry) []OrderItem {
	result := make([]OrderItem, 0, len(order))
	for _, entry := range order {
		result = append(result, OrderItem{Kind: entry.kind, ID: entry.id, GroupID: cloneGroupID(entry.groupID)})
	}
	return result
}

func persistWorkCreditOrder(ctx context.Context, store Store, workID string, order []workCreditOrderEntry) error {
	for index, entry := range order {
		sortOrder := index + 1
		switch entry.kind {
		case CreditItemGroup:
			if err := store.SetGroupSortOrder(ctx, workID, entry.id, sortOrder); err != nil {
				return err
			}
		case CreditItemCredit:
			if err := store.SetCreditSortOrder(ctx, workID, entry.id, entry.groupID, sortOrder); err != nil {
				return err
			}
		default:
			return errInvalidCreditOrder
		}
	}
	return nil
}

// errInvalidCreditOrder answers an order entry of no known kind.
var errInvalidCreditOrder = errors.New("unsupported data")

func cloneGroupID(groupID *string) *string {
	if groupID == nil {
		return nil
	}
	value := *groupID
	return &value
}
