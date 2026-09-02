package aidocument

import (
	"errors"
	"fmt"
)

// DocumentAfterOperations applies an already validated operation batch to an
// isolated copy of one loaded document. It is intentionally persistence-free:
// owning-domain adapters compile the returned typed tree into their generated
// storage contract and perform the authoritative revision CAS themselves.
func DocumentAfterOperations(document Document, operations []Operation) (Document, error) {
	result := document
	result.Nodes = canonicalNodes(document.Nodes)
	for index, operation := range operations {
		if err := applyDocumentOperation(&result, operation); err != nil {
			return Document{}, fmt.Errorf("apply operation %d: %w", index, err)
		}
	}
	result.Nodes = canonicalNodes(result.Nodes)
	return result, nil
}

func applyDocumentOperation(document *Document, operation Operation) error {
	switch operation.Kind {
	case OperationSetField:
		return mutateDocumentField(document, operation.SetField.Target, func(current *Value) error {
			*current = cloneValue(operation.SetField.Value)
			return nil
		}, true)
	case OperationUnsetField:
		return mutateDocumentField(document, operation.UnsetField.Target, nil, false)
	case OperationAttachFile:
		return mutateDocumentFile(document, operation.AttachFile.Target, operation.AttachFile.File, true)
	case OperationDetachFile:
		return mutateDocumentFile(document, operation.DetachFile.Target, "", false)
	case OperationInsertBlock:
		op := operation.InsertBlock
		document.Nodes = append(document.Nodes, Node{ID: op.Block, Kind: op.Kind, Parent: op.Parent})
		placeBlockAfter(document.Nodes, op.Block, op.Parent, op.After)
		return nil
	case OperationDeleteBlock:
		deleted := map[BlockID]bool{operation.DeleteBlock.Block: true}
		for changed := true; changed; {
			changed = false
			for _, node := range document.Nodes {
				if deleted[node.Parent] && !deleted[node.ID] {
					deleted[node.ID] = true
					changed = true
				}
			}
		}
		kept := document.Nodes[:0]
		for _, node := range document.Nodes {
			if !deleted[node.ID] {
				kept = append(kept, node)
			}
		}
		document.Nodes = kept
		renumberBlockOrders(document.Nodes)
		return nil
	case OperationMoveBlock:
		op := operation.MoveBlock
		placeBlockAfter(document.Nodes, op.Block, op.Parent, op.After)
		return nil
	case OperationReplaceBlockKind:
		node := mutableNode(document, operation.ReplaceBlockKind.Block)
		if node == nil {
			return errors.New("block does not exist")
		}
		node.Kind = operation.ReplaceBlockKind.Kind
		node.Shared, node.Localized, node.Files, node.Relations = nil, nil, nil, nil
		return nil
	case OperationInsertRelationItem:
		op := operation.InsertRelationItem
		relation, err := mutableRelation(document, op.Block, op.Relation, true)
		if err != nil {
			return err
		}
		relation.Items = append(relation.Items, RelationItem{ID: op.Item, Kind: op.Kind})
		placeRelationItemAfter(relation, op.Item, op.After)
		return nil
	case OperationDeleteRelationItem:
		op := operation.DeleteRelationItem
		relation, err := mutableRelation(document, op.Block, op.Relation, false)
		if err != nil {
			return err
		}
		kept := relation.Items[:0]
		for _, item := range relation.Items {
			if item.ID != op.Item {
				kept = append(kept, item)
			}
		}
		relation.Items = kept
		renumberRelationOrders(relation)
		return nil
	case OperationMoveRelationItem:
		op := operation.MoveRelationItem
		source, err := mutableRelation(document, op.Block, op.Relation, false)
		if err != nil {
			return err
		}
		var moved RelationItem
		found := false
		kept := source.Items[:0]
		for _, item := range source.Items {
			if item.ID == op.Item {
				moved, found = item, true
				continue
			}
			kept = append(kept, item)
		}
		if !found {
			return errors.New("relation item does not exist")
		}
		source.Items = kept
		renumberRelationOrders(source)
		target, err := mutableRelation(document, op.TargetBlock, op.Target, true)
		if err != nil {
			return err
		}
		target.Items = append(target.Items, moved)
		placeRelationItemAfter(target, moved.ID, op.After)
		return nil
	case OperationCreateTranslation:
		document.LocaleExists = true
		return nil
	case OperationDeleteTranslation:
		document.LocaleExists = false
		for index := range document.Nodes {
			document.Nodes[index].Localized = nil
			for relationIndex := range document.Nodes[index].Relations {
				for itemIndex := range document.Nodes[index].Relations[relationIndex].Items {
					document.Nodes[index].Relations[relationIndex].Items[itemIndex].Localized = nil
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported operation kind %q", operation.Kind)
	}
}

type valueMutation func(*Value) error

func mutateDocumentField(document *Document, target FieldTarget, mutate valueMutation, set bool) error {
	node := mutableNode(document, target.Block)
	if node == nil {
		return errors.New("block does not exist")
	}
	var shared, localized *[]FieldValue
	if target.relationItem() {
		item := mutableRelationItem(node, target.Relation, target.Item)
		if item == nil {
			return errors.New("relation item does not exist")
		}
		shared, localized = &item.Shared, &item.Localized
	} else {
		shared, localized = &node.Shared, &node.Localized
	}
	values, err := fieldValueOwner(document.Catalog, node.Kind, target, shared, localized)
	if err != nil {
		return err
	}
	return mutateFieldValues(values, target.Field, target.Path, mutate, set)
}

func fieldValueOwner(catalog Catalog, blockKind BlockKind, target FieldTarget, shared, localized *[]FieldValue) (*[]FieldValue, error) {
	ownership := FieldOwnership("")
	if target.relationItem() {
		for _, rule := range catalog.RelationFields {
			if rule.BlockKind == blockKind && rule.Relation == target.Relation && rule.Field == target.Field {
				ownership = rule.Ownership
				break
			}
		}
	} else {
		for _, rule := range catalog.Fields {
			if rule.BlockKind == blockKind && rule.Field == target.Field {
				ownership = rule.Ownership
				break
			}
		}
	}
	if ownership == "" {
		return nil, errors.New("field is not in the catalog")
	}
	if ownership == FieldOwnershipLocale {
		return localized, nil
	}
	return shared, nil
}

func mutateFieldValues(values *[]FieldValue, field FieldID, path []FieldPathSegment, mutate valueMutation, set bool) error {
	for index := range *values {
		if (*values)[index].ID != field {
			continue
		}
		if len(path) == 0 {
			if !set {
				*values = append((*values)[:index], (*values)[index+1:]...)
				return nil
			}
			return mutate(&(*values)[index].Value)
		}
		return mutateValuePath(&(*values)[index].Value, path, mutate, set)
	}
	if !set || len(path) != 0 {
		return nil
	}
	value := Value{}
	if err := mutate(&value); err != nil {
		return err
	}
	*values = append(*values, FieldValue{ID: field, Value: value})
	return nil
}

func mutateValuePath(value *Value, path []FieldPathSegment, mutate valueMutation, set bool) error {
	segment := path[0]
	if segment.Field != "" {
		if value.Kind != ValueKindObject {
			return errors.New("field path traverses a non-object value")
		}
		for index := range value.Object {
			if value.Object[index].ID != segment.Field {
				continue
			}
			if len(path) == 1 {
				if !set {
					value.Object = append(value.Object[:index], value.Object[index+1:]...)
					return nil
				}
				return mutate(&value.Object[index].Value)
			}
			return mutateValuePath(&value.Object[index].Value, path[1:], mutate, set)
		}
		if set && len(path) == 1 {
			created := Value{}
			if err := mutate(&created); err != nil {
				return err
			}
			value.Object = append(value.Object, ObjectField{ID: segment.Field, Value: created})
		}
		return nil
	}
	if value.Kind != ValueKindList {
		return errors.New("item path traverses a non-list value")
	}
	for index := range value.List {
		if value.List[index].ID != segment.Item {
			continue
		}
		if len(path) == 1 {
			if !set {
				value.List = append(value.List[:index], value.List[index+1:]...)
				return nil
			}
			return mutate(&value.List[index].Value)
		}
		return mutateValuePath(&value.List[index].Value, path[1:], mutate, set)
	}
	return nil
}

func mutateDocumentFile(document *Document, target FieldTarget, file FileReference, attach bool) error {
	node := mutableNode(document, target.Block)
	if node == nil {
		return errors.New("block does not exist")
	}
	files := &node.Files
	if target.relationItem() {
		item := mutableRelationItem(node, target.Relation, target.Item)
		if item == nil {
			return errors.New("relation item does not exist")
		}
		files = &item.Files
	}
	for index := range *files {
		binding := &(*files)[index]
		if binding.Field != target.Field || nestedFieldKey(binding.Field, binding.Path) != nestedFieldKey(target.Field, target.Path) {
			continue
		}
		if attach {
			binding.File = file
		} else {
			*files = append((*files)[:index], (*files)[index+1:]...)
		}
		return nil
	}
	if attach {
		*files = append(*files, FileBinding{Field: target.Field, Path: append([]FieldPathSegment(nil), target.Path...), File: file})
	}
	return nil
}

func mutableNode(document *Document, id BlockID) *Node {
	for index := range document.Nodes {
		if document.Nodes[index].ID == id {
			return &document.Nodes[index]
		}
	}
	return nil
}

func mutableRelation(document *Document, block BlockID, relationID RelationID, create bool) (*Relation, error) {
	node := mutableNode(document, block)
	if node == nil {
		return nil, errors.New("block does not exist")
	}
	for index := range node.Relations {
		if node.Relations[index].ID == relationID {
			return &node.Relations[index], nil
		}
	}
	if !create {
		return nil, errors.New("relation does not exist")
	}
	node.Relations = append(node.Relations, Relation{ID: relationID})
	return &node.Relations[len(node.Relations)-1], nil
}

func mutableRelationItem(node *Node, relationID RelationID, itemID RelationItemID) *RelationItem {
	for relationIndex := range node.Relations {
		if node.Relations[relationIndex].ID != relationID {
			continue
		}
		for itemIndex := range node.Relations[relationIndex].Items {
			if node.Relations[relationIndex].Items[itemIndex].ID == itemID {
				return &node.Relations[relationIndex].Items[itemIndex]
			}
		}
	}
	return nil
}

func placeBlockAfter(nodes []Node, block, parent, after BlockID) {
	for index := range nodes {
		if nodes[index].ID == block {
			nodes[index].Parent = parent
			nodes[index].Order = 0
		}
	}
	siblings := make([]*Node, 0)
	for index := range nodes {
		if nodes[index].Parent == parent && nodes[index].ID != block {
			siblings = append(siblings, &nodes[index])
		}
	}
	ordered := make([]BlockID, 0, len(siblings)+1)
	inserted := false
	if after == "" {
		ordered = append(ordered, block)
		inserted = true
	}
	for _, sibling := range siblings {
		ordered = append(ordered, sibling.ID)
		if sibling.ID == after {
			ordered = append(ordered, block)
			inserted = true
		}
	}
	if !inserted {
		ordered = append(ordered, block)
	}
	for order, id := range ordered {
		for index := range nodes {
			if nodes[index].ID == id {
				nodes[index].Order = order
			}
		}
	}
}

func renumberBlockOrders(nodes []Node) {
	parents := make(map[BlockID]int)
	for index := range nodes {
		nodes[index].Order = parents[nodes[index].Parent]
		parents[nodes[index].Parent]++
	}
}

func placeRelationItemAfter(relation *Relation, item, after RelationItemID) {
	items := append([]RelationItem(nil), relation.Items...)
	relation.Items = relation.Items[:0]
	var moved RelationItem
	for _, candidate := range items {
		if candidate.ID == item {
			moved = candidate
		}
	}
	if after == "" {
		relation.Items = append(relation.Items, moved)
	}
	for _, candidate := range items {
		if candidate.ID == item {
			continue
		}
		relation.Items = append(relation.Items, candidate)
		if candidate.ID == after {
			relation.Items = append(relation.Items, moved)
		}
	}
	renumberRelationOrders(relation)
}

func renumberRelationOrders(relation *Relation) {
	for index := range relation.Items {
		relation.Items[index].Order = index
	}
}
