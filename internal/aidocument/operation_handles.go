package aidocument

import (
	"sort"
	"strings"
)

func blockHandle(block BlockID) string { return "block:" + string(block) }

func relationHandle(block BlockID, relation RelationID) string {
	return "relation:" + string(block) + "/" + string(relation)
}

func relationItemHandle(block BlockID, relation RelationID, item RelationItemID) string {
	return "relation-item:" + string(block) + "/" + string(relation) + "/" + string(item)
}

func fieldHandle(target FieldTarget) string {
	path := ""
	for _, segment := range target.Path {
		if segment.Field != "" {
			path += "/field:" + string(segment.Field)
		} else {
			path += "/item:" + string(segment.Item)
		}
	}
	if target.relationItem() {
		return "field:" + string(target.Block) + "/" + string(target.Relation) + "/" + string(target.Item) + "/" + string(target.Field) + path
	}
	return "field:" + string(target.Block) + "/" + string(target.Field) + path
}

// OperationHandles returns the sorted, unique targets of one operation.
// The operation payload must match its kind.
func OperationHandles(operation Operation, locale Locale) []string {
	return canonicalHandles(operationHandles(operation, locale))
}

// AffectedHandles collects the sorted, unique targets of a batch whose
// operation shapes have been validated.
func AffectedHandles(operations []Operation, locale Locale) []string {
	var handles []string
	for _, operation := range operations {
		handles = append(handles, operationHandles(operation, locale)...)
	}
	return canonicalHandles(handles)
}

func operationHandles(operation Operation, locale Locale) []string {
	var handles []string
	switch operation.Kind {
	case OperationSetField:
		handles = []string{fieldHandle(operation.SetField.Target)}
	case OperationUnsetField:
		handles = []string{fieldHandle(operation.UnsetField.Target)}
	case OperationInsertBlock:
		handles = []string{blockHandle(operation.InsertBlock.Block), blockHandle(operation.InsertBlock.Parent)}
	case OperationDeleteBlock:
		handles = []string{blockHandle(operation.DeleteBlock.Block)}
	case OperationMoveBlock:
		handles = []string{blockHandle(operation.MoveBlock.Block), blockHandle(operation.MoveBlock.Parent)}
	case OperationReplaceBlockKind:
		handles = []string{blockHandle(operation.ReplaceBlockKind.Block)}
	case OperationInsertRelationItem:
		op := operation.InsertRelationItem
		handles = []string{relationHandle(op.Block, op.Relation), relationItemHandle(op.Block, op.Relation, op.Item)}
	case OperationDeleteRelationItem:
		op := operation.DeleteRelationItem
		handles = []string{relationHandle(op.Block, op.Relation), relationItemHandle(op.Block, op.Relation, op.Item)}
	case OperationMoveRelationItem:
		op := operation.MoveRelationItem
		handles = []string{relationItemHandle(op.Block, op.Relation, op.Item), relationHandle(op.Block, op.Relation), relationHandle(op.TargetBlock, op.Target)}
	case OperationAttachFile:
		handles = []string{fieldHandle(operation.AttachFile.Target)}
	case OperationDetachFile:
		handles = []string{fieldHandle(operation.DetachFile.Target)}
	case OperationCreateTranslation, OperationDeleteTranslation:
		handles = []string{"translation:" + string(locale)}
	}
	return handles
}

func canonicalHandles(handles []string) []string {
	set := make(map[string]struct{}, len(handles))
	for _, handle := range handles {
		if strings.TrimSpace(handle) != "" && handle != "block:" {
			set[handle] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for handle := range set {
		result = append(result, handle)
	}
	sort.Strings(result)
	return result
}
