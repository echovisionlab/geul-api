package work

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type workCreditOrderEntry struct {
	kind    managev1.WorkCreditItemKind
	id      string
	groupID *string
}

func loadWorkCreditRows(ctx context.Context, db *gorm.DB, workID string) ([]model.WorkCreditGroup, []model.WorkCredit, []workCreditOrderEntry, error) {
	var groups []model.WorkCreditGroup
	if err := db.WithContext(ctx).
		Where("work_id = ?", workID).
		Order("sort_order ASC").
		Order("id ASC").
		Find(&groups).Error; err != nil {
		return nil, nil, nil, err
	}

	var credits []model.WorkCredit
	if err := db.WithContext(ctx).
		Where("work_id = ?", workID).
		Order("sort_order ASC").
		Order("id ASC").
		Find(&credits).Error; err != nil {
		return nil, nil, nil, err
	}

	order, err := buildWorkCreditOrder(groups, credits)
	if err != nil {
		return nil, nil, nil, errs.Internal(err)
	}
	return groups, credits, order, nil
}

// buildWorkCreditOrder merges the group and credit rows when their sort orders
// describe a valid canonical flattened list.
func buildWorkCreditOrder(groups []model.WorkCreditGroup, credits []model.WorkCredit) ([]workCreditOrderEntry, error) {
	merged := make([]workCreditOrderEntry, 0, len(groups)+len(credits))
	groupSortOrders := make(map[string]int, len(groups))
	creditSortOrders := make(map[string]int, len(credits))
	for _, group := range groups {
		groupSortOrders[group.ID] = group.SortOrder
		merged = append(merged, workCreditOrderEntry{
			kind: managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP,
			id:   group.ID,
		})
	}
	for _, credit := range credits {
		creditSortOrders[credit.ID] = credit.SortOrder
		merged = append(merged, workCreditOrderEntry{
			kind:    managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT,
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
	if entry.kind == managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP {
		return groupSortOrders[entry.id]
	}
	return creditSortOrders[entry.id]
}

func validPersistedWorkCreditOrder(
	order []workCreditOrderEntry,
	groups []model.WorkCreditGroup,
	credits []model.WorkCredit,
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
		case managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP:
			if _, ok := groupIDs[entry.id]; !ok {
				return false
			}
			if _, duplicate := seenGroups[entry.id]; duplicate {
				return false
			}
			seenGroups[entry.id] = struct{}{}
			activeGroup = entry.id
		case managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT:
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

func moveWorkCreditGroupOrder(order []workCreditOrderEntry, groupID string, after, before *managev1.WorkCreditOrderItem) ([]workCreditOrderEntry, bool) {
	start := -1
	for index, entry := range order {
		if entry.kind == managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP && entry.id == groupID {
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
	for end < len(order) && order[end].kind == managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT &&
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

func workCreditGroupAnchorInSection(order []workCreditOrderEntry, anchor *managev1.WorkCreditOrderItem, groupID string) bool {
	if anchor == nil || anchor.Id == "" {
		return false
	}
	index := findWorkCreditOrderEntry(order, anchor.Kind, anchor.Id)
	if index < 0 {
		return false
	}
	entry := order[index]
	if entry.kind == managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP {
		return entry.id == groupID
	}
	return entry.kind == managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT &&
		entry.groupID != nil && *entry.groupID == groupID
}

func workCreditGroupAnchorPosition(order []workCreditOrderEntry, anchor *managev1.WorkCreditOrderItem, before bool) (int, bool) {
	if anchor == nil || anchor.Id == "" {
		return 0, false
	}
	index := findWorkCreditOrderEntry(order, anchor.Kind, anchor.Id)
	if index < 0 {
		return 0, false
	}
	entry := order[index]
	if entry.kind == managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP {
		if before {
			return index, true
		}
		return workCreditGroupSectionEnd(order, index, entry.id), true
	}
	if entry.kind != managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT {
		return 0, false
	}
	if entry.groupID != nil {
		groupIndex := findWorkCreditOrderEntry(order, managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP, *entry.groupID)
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
	for index < len(order) && order[index].kind == managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT &&
		order[index].groupID != nil && *order[index].groupID == groupID {
		index++
	}
	return index
}

func moveWorkCreditOrder(order []workCreditOrderEntry, creditID string, targetGroupID *string, after, before *managev1.WorkCreditOrderItem) ([]workCreditOrderEntry, bool) {
	start := findWorkCreditOrderEntry(order, managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, creditID)
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

func workCreditCreditAnchorPosition(order []workCreditOrderEntry, anchor *managev1.WorkCreditOrderItem, targetGroupID *string) (int, bool) {
	if anchor == nil || anchor.Kind != managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT || anchor.Id == "" {
		return 0, false
	}
	index := findWorkCreditOrderEntry(order, anchor.Kind, anchor.Id)
	if index < 0 || !sameOptionalString(order[index].groupID, targetGroupID) {
		return 0, false
	}
	return index, true
}

func workCreditGroupSectionEndForTarget(order []workCreditOrderEntry, groupID *string) int {
	if groupID == nil {
		return len(order)
	}
	groupIndex := findWorkCreditOrderEntry(order, managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP, *groupID)
	if groupIndex < 0 {
		return len(order)
	}
	return workCreditGroupSectionEnd(order, groupIndex, *groupID)
}

func findWorkCreditOrderEntry(order []workCreditOrderEntry, kind managev1.WorkCreditItemKind, id string) int {
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
	tx *gorm.DB,
	workID string,
	order []workCreditOrderEntry,
	movingKind managev1.WorkCreditItemKind,
	movingID string,
	targetGroupID *string,
	after, before *managev1.WorkCreditOrderItem,
) error {
	for _, anchor := range []*managev1.WorkCreditOrderItem{after, before} {
		if anchor == nil {
			continue
		}
		if strings.TrimSpace(anchor.Id) == "" {
			return errs.InvalidArgument("anchor.id", "must identify an item")
		}
		if anchor.Kind != managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP &&
			anchor.Kind != managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT {
			return errs.InvalidArgument("anchor.kind", "must identify a group or credit")
		}
		if anchor.Kind == movingKind && anchor.Id == movingID {
			return errs.InvalidArgument("anchor", "cannot reference the moved item")
		}

		index := findWorkCreditOrderEntry(order, anchor.Kind, anchor.Id)
		if index >= 0 {
			if movingKind == managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT {
				entry := order[index]
				if anchor.Kind != managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT ||
					!sameOptionalString(entry.groupID, targetGroupID) {
					return errs.InvalidArgument("anchor", "must be a credit in the target group")
				}
			}
			continue
		}

		parsedID, err := uuid.Parse(anchor.Id)
		if err != nil {
			return errs.InvalidArgument("anchor.id", "must be a valid item ID")
		}
		var groupCount, creditCount int64
		if err := tx.WithContext(ctx).Model(&model.WorkCreditGroup{}).Where("id = ?", parsedID.String()).Count(&groupCount).Error; err != nil {
			return errs.Internal(err)
		}
		if err := tx.WithContext(ctx).Model(&model.WorkCredit{}).Where("id = ?", parsedID.String()).Count(&creditCount).Error; err != nil {
			return errs.Internal(err)
		}
		if groupCount > 0 || creditCount > 0 {
			return errs.InvalidArgument("anchor", "must reference a compatible item in this Work")
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

func removeWorkCreditOrderEntry(order []workCreditOrderEntry, kind managev1.WorkCreditItemKind, id string) []workCreditOrderEntry {
	index := findWorkCreditOrderEntry(order, kind, id)
	if index < 0 {
		return order
	}
	result := make([]workCreditOrderEntry, 0, len(order)-1)
	result = append(result, order[:index]...)
	result = append(result, order[index+1:]...)
	return result
}

func protoWorkCreditOrder(order []workCreditOrderEntry) []*managev1.WorkCreditOrderItem {
	result := make([]*managev1.WorkCreditOrderItem, 0, len(order))
	for _, entry := range order {
		item := &managev1.WorkCreditOrderItem{Kind: entry.kind, Id: entry.id}
		if entry.kind == managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT {
			groupID := ""
			if entry.groupID != nil {
				groupID = *entry.groupID
			}
			item.GroupId = &groupID
		}
		result = append(result, item)
	}
	return result
}

func persistWorkCreditOrder(ctx context.Context, tx *gorm.DB, workID string, order []workCreditOrderEntry) error {
	for index, entry := range order {
		sortOrder := index + 1
		switch entry.kind {
		case managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP:
			result := tx.WithContext(ctx).
				Model(&model.WorkCreditGroup{}).
				Where("id = ? AND work_id = ?", entry.id, workID).
				Update("sort_order", sortOrder)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return gorm.ErrRecordNotFound
			}
		case managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT:
			var groupID any
			if entry.groupID != nil {
				groupID = *entry.groupID
			}
			result := tx.WithContext(ctx).
				Model(&model.WorkCredit{}).
				Where("id = ? AND work_id = ?", entry.id, workID).
				Updates(map[string]any{"group_id": groupID, "sort_order": sortOrder})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return gorm.ErrRecordNotFound
			}
		default:
			return gorm.ErrInvalidData
		}
	}
	return nil
}

func cloneGroupID(groupID *string) *string {
	if groupID == nil {
		return nil
	}
	value := *groupID
	return &value
}
