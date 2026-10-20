package aidocument

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type IssueCode string

const (
	IssueInvalidOperation            IssueCode = "invalid_operation"
	IssueUnknownBlock                IssueCode = "unknown_block"
	IssueDuplicateBlock              IssueCode = "duplicate_block"
	IssueUnknownBlockKind            IssueCode = "unknown_block_kind"
	IssueUnknownField                IssueCode = "unknown_field"
	IssueUnknownRelation             IssueCode = "unknown_relation"
	IssueUnknownRelationItem         IssueCode = "unknown_relation_item"
	IssueDuplicateRelationItem       IssueCode = "duplicate_relation_item"
	IssueInvalidRelationItemMove     IssueCode = "invalid_relation_item_move"
	IssueValueKindMismatch           IssueCode = "value_kind_mismatch"
	IssueSourceAuthorityRequired     IssueCode = "source_authority_required"
	IssueTargetFieldForbidden        IssueCode = "target_field_forbidden"
	IssueInvalidBlockRelation        IssueCode = "invalid_block_relation"
	IssueBlockCycle                  IssueCode = "block_cycle"
	IssueInvalidFileReference        IssueCode = "invalid_file_reference"
	IssueTranslationIsSource         IssueCode = "translation_is_source"
	IssueTranslationAlreadyExists    IssueCode = "translation_already_exists"
	IssueTranslationMissing          IssueCode = "translation_missing"
	IssueLocaleOperationNotExclusive IssueCode = "locale_operation_not_exclusive"
)

type OperationIssue struct {
	Operation int       `json:"operation"`
	Code      IssueCode `json:"code"`
	Handle    string    `json:"handle,omitempty"`
	Message   string    `json:"message"`
}

type ConflictCode string

const (
	ConflictDocumentRevision ConflictCode = "document_revision_conflict"
	ConflictTargetRevision   ConflictCode = "target_revision_conflict"
)

type Conflict struct {
	Code                    ConflictCode `json:"code"`
	CurrentDocumentRevision Revision     `json:"currentDocumentRevision"`
	CurrentTargetRevision   *Revision    `json:"currentTargetRevision,omitempty"`
	AffectedHandles         []string     `json:"affectedHandles"`
}

type ConflictError struct{ Conflict Conflict }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s: current document revision is %q", e.Conflict.Code, e.Conflict.CurrentDocumentRevision)
}

type ValidationResult struct {
	Normalized []Operation      `json:"normalized,omitempty"`
	Issues     []OperationIssue `json:"issues,omitempty"`
	Conflict   *Conflict        `json:"conflict,omitempty"`
}

func (r ValidationResult) Valid() bool { return len(r.Issues) == 0 && r.Conflict == nil }

type ValidationError struct{ Result ValidationResult }

func (e *ValidationError) Error() string {
	if e.Result.Conflict != nil {
		return (&ConflictError{Conflict: *e.Result.Conflict}).Error()
	}
	return fmt.Sprintf("document operation validation failed with %d issue(s)", len(e.Result.Issues))
}

type graphBlock struct {
	kind      BlockKind
	parent    BlockID
	relations map[RelationID]map[RelationItemID]RelationItemKind
}

type resolvedFieldRule struct {
	valueKind    ValueKind
	ownership    FieldOwnership
	translatable bool
	file         bool
	schema       *FieldSchema
}

func ValidateOperations(document Document, request ApplyRequest) ValidationResult {
	result := ValidationResult{Normalized: append([]Operation(nil), request.Operations...)}
	if err := request.validateEnvelope(); err != nil {
		result.Issues = append(result.Issues, OperationIssue{Operation: -1, Code: IssueInvalidOperation, Message: err.Error()})
		return result
	}
	if err := document.validate(); err != nil {
		result.Issues = append(result.Issues, OperationIssue{Operation: -1, Code: IssueInvalidOperation, Message: "domain document is invalid: " + err.Error()})
		return result
	}
	if document.Identity != request.Identity() || document.Locale != request.Locale {
		result.Issues = append(result.Issues, OperationIssue{Operation: -1, Code: IssueInvalidOperation, Message: "loaded document identity or locale does not match apply request"})
		return result
	}
	if document.DocumentRevision != request.ExpectedDocumentRevision {
		result.Conflict = &Conflict{
			Code:                    ConflictDocumentRevision,
			CurrentDocumentRevision: document.DocumentRevision,
			CurrentTargetRevision:   cloneRevision(document.TargetRevision),
			AffectedHandles:         affectedHandles(request),
		}
		return result
	}
	if !equalRevision(document.TargetRevision, request.ExpectedTargetRevision) {
		result.Conflict = &Conflict{
			Code:                    ConflictTargetRevision,
			CurrentDocumentRevision: document.DocumentRevision,
			CurrentTargetRevision:   cloneRevision(document.TargetRevision),
			AffectedHandles:         affectedHandles(request),
		}
		return result
	}

	kinds := make(map[BlockKind]struct{}, len(document.Catalog.BlockKinds))
	for _, kind := range document.Catalog.BlockKinds {
		kinds[kind] = struct{}{}
	}
	rules := make(map[string]FieldRule, len(document.Catalog.Fields))
	for _, rule := range document.Catalog.Fields {
		rules[fieldRuleKey(rule.BlockKind, rule.Field)] = rule
	}
	relationRules := make(map[string]map[RelationItemKind]struct{}, len(document.Catalog.Relations))
	for _, rule := range document.Catalog.Relations {
		allowed := make(map[RelationItemKind]struct{}, len(rule.ItemKinds))
		for _, kind := range rule.ItemKinds {
			allowed[kind] = struct{}{}
		}
		relationRules[relationRuleKey(rule.BlockKind, rule.Relation)] = allowed
	}
	relationFieldRules := make(map[string]RelationFieldRule, len(document.Catalog.RelationFields))
	for _, rule := range document.Catalog.RelationFields {
		relationFieldRules[relationFieldRuleKey(rule.BlockKind, rule.Relation, rule.ItemKind, rule.Field)] = rule
	}
	graph := make(map[BlockID]graphBlock, len(document.Nodes))
	for _, node := range document.Nodes {
		block := graphBlock{kind: node.Kind, parent: node.Parent, relations: make(map[RelationID]map[RelationItemID]RelationItemKind)}
		for _, relation := range node.Relations {
			items := make(map[RelationItemID]RelationItemKind, len(relation.Items))
			for _, item := range relation.Items {
				items[item.ID] = item.Kind
			}
			block.relations[relation.ID] = items
		}
		graph[node.ID] = block
	}

	role := document.Role()
	for index, operation := range request.Operations {
		issue := validateOperation(index, operation, role, document.LocaleExists, len(request.Operations), kinds, rules, relationRules, relationFieldRules, graph)
		if issue != nil {
			result.Issues = append(result.Issues, *issue)
			continue
		}
		applyGraphEffect(operation, graph)
		if operation.Kind == OperationCreateTranslation {
			document.LocaleExists = true
		}
		if operation.Kind == OperationDeleteTranslation {
			document.LocaleExists = false
		}
	}
	return result
}

func validateOperation(
	index int,
	operation Operation,
	role LocaleRole,
	localeExists bool,
	batchSize int,
	kinds map[BlockKind]struct{},
	rules map[string]FieldRule,
	relationRules map[string]map[RelationItemKind]struct{},
	relationFieldRules map[string]RelationFieldRule,
	graph map[BlockID]graphBlock,
) *OperationIssue {
	issue := func(code IssueCode, handle, message string) *OperationIssue {
		return &OperationIssue{Operation: index, Code: code, Handle: handle, Message: message}
	}
	if err := operation.validateShape(); err != nil {
		return issue(IssueInvalidOperation, "", err.Error())
	}
	requireSource := func(handle string) *OperationIssue {
		if role == LocaleRoleSource {
			return nil
		}
		return issue(IssueSourceAuthorityRequired, handle, "only the current source locale may alter the shared block graph, shared fields, or file relations")
	}
	resolveField := func(target FieldTarget) (resolvedFieldRule, *OperationIssue) {
		if err := validateCanonicalFieldTarget(target); err != nil {
			return resolvedFieldRule{}, issue(IssueInvalidOperation, fieldHandle(target), err.Error())
		}
		block, ok := graph[target.Block]
		if !ok {
			return resolvedFieldRule{}, issue(IssueUnknownBlock, blockHandle(target.Block), "field target block does not exist")
		}
		if !target.relationItem() {
			rule, ok := rules[fieldRuleKey(block.kind, target.Field)]
			if !ok {
				return resolvedFieldRule{}, issue(IssueUnknownField, fieldHandle(target), "field is not defined for the target block kind")
			}
			resolved, err := resolveNestedFieldRule(rule.ValueKind, rule.Ownership, rule.Translatable, rule.File, rule.Schema, target.Path)
			if err != nil {
				return resolvedFieldRule{}, issue(IssueUnknownField, fieldHandle(target), err.Error())
			}
			return resolved, nil
		}
		if target.Relation == "" || target.Item == "" {
			return resolvedFieldRule{}, issue(IssueInvalidOperation, fieldHandle(target), "relation-item field target requires both relation and item IDs")
		}
		items, ok := block.relations[target.Relation]
		if !ok {
			return resolvedFieldRule{}, issue(IssueUnknownRelation, relationHandle(target.Block, target.Relation), "relation does not exist")
		}
		itemKind, ok := items[target.Item]
		if !ok {
			return resolvedFieldRule{}, issue(IssueUnknownRelationItem, relationItemHandle(target.Block, target.Relation, target.Item), "relation item does not exist")
		}
		rule, ok := relationFieldRules[relationFieldRuleKey(block.kind, target.Relation, itemKind, target.Field)]
		if !ok {
			return resolvedFieldRule{}, issue(IssueUnknownField, fieldHandle(target), "field is not defined for the relation item kind")
		}
		resolved, err := resolveNestedFieldRule(rule.ValueKind, rule.Ownership, rule.Translatable, rule.File, rule.Schema, target.Path)
		if err != nil {
			return resolvedFieldRule{}, issue(IssueUnknownField, fieldHandle(target), err.Error())
		}
		return resolved, nil
	}

	switch operation.Kind {
	case OperationSetField:
		rule, fieldIssue := resolveField(operation.SetField.Target)
		if fieldIssue != nil {
			return fieldIssue
		}
		if rule.file {
			return issue(IssueUnknownField, fieldHandle(operation.SetField.Target), "file fields require a typed file operation")
		}
		if operation.SetField.Value.Kind != rule.valueKind {
			return issue(IssueValueKindMismatch, fieldHandle(operation.SetField.Target), "field value kind does not match the domain catalog")
		}
		if err := operation.SetField.Value.validate(); err != nil {
			return issue(IssueValueKindMismatch, fieldHandle(operation.SetField.Target), err.Error())
		}
		if rule.schema != nil {
			if err := validateValueForSchema(operation.SetField.Value, *rule.schema, 0); err != nil {
				return issue(IssueValueKindMismatch, fieldHandle(operation.SetField.Target), err.Error())
			}
		}
		if role == LocaleRoleNonSource && (rule.ownership != FieldOwnershipLocale || !rule.translatable) {
			return issue(IssueTargetFieldForbidden, fieldHandle(operation.SetField.Target), "non-source locales may modify only locale-owned translatable values")
		}
		return nil
	case OperationUnsetField:
		rule, fieldIssue := resolveField(operation.UnsetField.Target)
		if fieldIssue != nil {
			return fieldIssue
		}
		if role == LocaleRoleNonSource {
			return issue(IssueTargetFieldForbidden, fieldHandle(operation.UnsetField.Target), "non-source locales cannot unset fields; set an explicit empty value or delete the translation")
		}
		if rule.file {
			return issue(IssueTargetFieldForbidden, fieldHandle(operation.UnsetField.Target), "file fields require a typed file operation")
		}
		return nil
	case OperationInsertBlock:
		if sourceIssue := requireSource(blockHandle(operation.InsertBlock.Block)); sourceIssue != nil {
			return sourceIssue
		}
		if err := validateStableID("block ID", string(operation.InsertBlock.Block), 160); err != nil {
			return issue(IssueInvalidOperation, blockHandle(operation.InsertBlock.Block), err.Error())
		}
		if _, exists := graph[operation.InsertBlock.Block]; exists {
			return issue(IssueDuplicateBlock, blockHandle(operation.InsertBlock.Block), "block ID already exists")
		}
		if _, ok := kinds[operation.InsertBlock.Kind]; !ok {
			return issue(IssueUnknownBlockKind, blockHandle(operation.InsertBlock.Block), "block kind is not in the domain catalog")
		}
		return validatePlacement(index, operation.InsertBlock.Block, operation.InsertBlock.Parent, operation.InsertBlock.After, graph)
	case OperationDeleteBlock:
		if sourceIssue := requireSource(blockHandle(operation.DeleteBlock.Block)); sourceIssue != nil {
			return sourceIssue
		}
		if _, ok := graph[operation.DeleteBlock.Block]; !ok {
			return issue(IssueUnknownBlock, blockHandle(operation.DeleteBlock.Block), "block does not exist")
		}
		return nil
	case OperationMoveBlock:
		if sourceIssue := requireSource(blockHandle(operation.MoveBlock.Block)); sourceIssue != nil {
			return sourceIssue
		}
		if _, ok := graph[operation.MoveBlock.Block]; !ok {
			return issue(IssueUnknownBlock, blockHandle(operation.MoveBlock.Block), "block does not exist")
		}
		if operation.MoveBlock.Parent == operation.MoveBlock.Block || isDescendant(operation.MoveBlock.Parent, operation.MoveBlock.Block, graph) {
			return issue(IssueBlockCycle, blockHandle(operation.MoveBlock.Block), "move would create a block cycle")
		}
		return validatePlacement(index, operation.MoveBlock.Block, operation.MoveBlock.Parent, operation.MoveBlock.After, graph)
	case OperationReplaceBlockKind:
		if sourceIssue := requireSource(blockHandle(operation.ReplaceBlockKind.Block)); sourceIssue != nil {
			return sourceIssue
		}
		if _, ok := graph[operation.ReplaceBlockKind.Block]; !ok {
			return issue(IssueUnknownBlock, blockHandle(operation.ReplaceBlockKind.Block), "block does not exist")
		}
		if _, ok := kinds[operation.ReplaceBlockKind.Kind]; !ok {
			return issue(IssueUnknownBlockKind, blockHandle(operation.ReplaceBlockKind.Block), "block kind is not in the domain catalog")
		}
		return nil
	case OperationInsertRelationItem:
		op := operation.InsertRelationItem
		if sourceIssue := requireSource(relationItemHandle(op.Block, op.Relation, op.Item)); sourceIssue != nil {
			return sourceIssue
		}
		if err := validateRelationOperationIDs(op.Block, op.Relation, op.Item); err != nil {
			return issue(IssueInvalidOperation, relationItemHandle(op.Block, op.Relation, op.Item), err.Error())
		}
		if err := validateStableID("relation item kind", string(op.Kind), 80); err != nil {
			return issue(IssueInvalidOperation, relationItemHandle(op.Block, op.Relation, op.Item), err.Error())
		}
		if op.After != "" {
			if err := validateStableID("after relation item ID", string(op.After), 160); err != nil {
				return issue(IssueInvalidOperation, relationItemHandle(op.Block, op.Relation, op.Item), err.Error())
			}
		}
		block, ok := graph[op.Block]
		if !ok {
			return issue(IssueUnknownBlock, blockHandle(op.Block), "relation owner block does not exist")
		}
		allowed, ok := relationRules[relationRuleKey(block.kind, op.Relation)]
		if !ok {
			return issue(IssueUnknownRelation, relationHandle(op.Block, op.Relation), "relation is not defined for the block kind")
		}
		if _, ok := allowed[op.Kind]; !ok {
			return issue(IssueInvalidRelationItemMove, relationItemHandle(op.Block, op.Relation, op.Item), "relation item kind is not allowed")
		}
		for _, graphBlock := range graph {
			for _, items := range graphBlock.relations {
				if _, duplicate := items[op.Item]; duplicate {
					return issue(IssueDuplicateRelationItem, relationItemHandle(op.Block, op.Relation, op.Item), "relation item ID already exists")
				}
			}
		}
		return validateRelationPlacement(index, op.Block, op.Relation, op.Item, op.After, graph)
	case OperationDeleteRelationItem:
		op := operation.DeleteRelationItem
		if sourceIssue := requireSource(relationItemHandle(op.Block, op.Relation, op.Item)); sourceIssue != nil {
			return sourceIssue
		}
		if err := validateRelationOperationIDs(op.Block, op.Relation, op.Item); err != nil {
			return issue(IssueInvalidOperation, relationItemHandle(op.Block, op.Relation, op.Item), err.Error())
		}
		return requireRelationItem(index, op.Block, op.Relation, op.Item, graph)
	case OperationMoveRelationItem:
		op := operation.MoveRelationItem
		if sourceIssue := requireSource(relationItemHandle(op.Block, op.Relation, op.Item)); sourceIssue != nil {
			return sourceIssue
		}
		if err := validateRelationOperationIDs(op.Block, op.Relation, op.Item); err != nil {
			return issue(IssueInvalidOperation, relationItemHandle(op.Block, op.Relation, op.Item), err.Error())
		}
		if err := validateRelationOperationIDs(op.TargetBlock, op.Target, op.Item); err != nil {
			return issue(IssueInvalidOperation, relationItemHandle(op.TargetBlock, op.Target, op.Item), err.Error())
		}
		if op.After != "" {
			if err := validateStableID("after relation item ID", string(op.After), 160); err != nil {
				return issue(IssueInvalidOperation, relationItemHandle(op.TargetBlock, op.Target, op.Item), err.Error())
			}
		}
		if itemIssue := requireRelationItem(index, op.Block, op.Relation, op.Item, graph); itemIssue != nil {
			return itemIssue
		}
		source := graph[op.Block]
		kind := source.relations[op.Relation][op.Item]
		target, ok := graph[op.TargetBlock]
		if !ok {
			return issue(IssueUnknownBlock, blockHandle(op.TargetBlock), "target relation owner block does not exist")
		}
		allowed, ok := relationRules[relationRuleKey(target.kind, op.Target)]
		if !ok {
			return issue(IssueUnknownRelation, relationHandle(op.TargetBlock, op.Target), "target relation is not defined for the block kind")
		}
		if _, ok := allowed[kind]; !ok {
			return issue(IssueInvalidRelationItemMove, relationItemHandle(op.Block, op.Relation, op.Item), "target relation does not allow the item kind")
		}
		return validateRelationPlacement(index, op.TargetBlock, op.Target, op.Item, op.After, graph)
	case OperationAttachFile:
		if sourceIssue := requireSource(fieldHandle(operation.AttachFile.Target)); sourceIssue != nil {
			return sourceIssue
		}
		rule, fieldIssue := resolveField(operation.AttachFile.Target)
		if fieldIssue != nil {
			return fieldIssue
		}
		if !rule.file {
			return issue(IssueUnknownField, fieldHandle(operation.AttachFile.Target), "field is not a file slot")
		}
		if err := validateFileReference(operation.AttachFile.File); err != nil {
			return issue(IssueInvalidFileReference, fieldHandle(operation.AttachFile.Target), err.Error())
		}
		return nil
	case OperationDetachFile:
		if sourceIssue := requireSource(fieldHandle(operation.DetachFile.Target)); sourceIssue != nil {
			return sourceIssue
		}
		rule, fieldIssue := resolveField(operation.DetachFile.Target)
		if fieldIssue != nil {
			return fieldIssue
		}
		if !rule.file {
			return issue(IssueUnknownField, fieldHandle(operation.DetachFile.Target), "field is not a file slot")
		}
		return nil
	case OperationCreateTranslation:
		if role == LocaleRoleSource {
			return issue(IssueTranslationIsSource, "translation", "the current source locale cannot be created as a translation")
		}
		if batchSize != 1 {
			return issue(IssueLocaleOperationNotExclusive, "translation", "translation create must be an exclusive aggregate operation")
		}
		if localeExists {
			return issue(IssueTranslationAlreadyExists, "translation", "translation locale already exists")
		}
		return nil
	case OperationDeleteTranslation:
		if role == LocaleRoleSource {
			return issue(IssueTranslationIsSource, "translation", "the current source locale cannot be deleted as a translation")
		}
		if batchSize != 1 {
			return issue(IssueLocaleOperationNotExclusive, "translation", "translation delete must be an exclusive aggregate operation")
		}
		if !localeExists {
			return issue(IssueTranslationMissing, "translation", "translation locale does not exist")
		}
		return nil
	default:
		return issue(IssueInvalidOperation, "", "unsupported operation kind")
	}
}

func resolveNestedFieldRule(
	kind ValueKind,
	ownership FieldOwnership,
	translatable bool,
	file bool,
	schema *FieldSchema,
	path []FieldPathSegment,
) (resolvedFieldRule, error) {
	if schema == nil {
		if len(path) != 0 {
			return resolvedFieldRule{}, errors.New("field does not define nested catalog shape")
		}
		return resolvedFieldRule{valueKind: kind, ownership: ownership, translatable: translatable, file: file}, nil
	}
	current := schema
	for _, segment := range path {
		if err := segment.validate(); err != nil {
			return resolvedFieldRule{}, err
		}
		if segment.Field != "" {
			if current.File || current.Kind != ValueKindObject {
				return resolvedFieldRule{}, fmt.Errorf("path field %q does not target an object", segment.Field)
			}
			var next *FieldSchema
			for index := range current.Fields {
				if current.Fields[index].Field == segment.Field {
					next = &current.Fields[index].Schema
					break
				}
			}
			if next == nil {
				return resolvedFieldRule{}, fmt.Errorf("path field %q is not in the catalog", segment.Field)
			}
			current = next
			continue
		}
		if current.File || current.Kind != ValueKindList || current.Item == nil {
			return resolvedFieldRule{}, fmt.Errorf("path item %q does not target a list", segment.Item)
		}
		if current.Identity.Kind == ListIdentityPositional {
			return resolvedFieldRule{}, errors.New("positional lists can only be replaced as a whole typed value")
		}
		if current.Identity.Kind == ListIdentityFixed {
			found := false
			for _, handle := range current.Identity.Handles {
				if handle == segment.Item {
					found = true
					break
				}
			}
			if !found {
				return resolvedFieldRule{}, fmt.Errorf("fixed list item %q is not in the catalog", segment.Item)
			}
		}
		current = current.Item
	}
	return resolvedFieldRule{
		valueKind: current.Kind, ownership: current.Ownership,
		translatable: current.Translatable, file: current.File, schema: current,
	}, nil
}

func validatePlacement(index int, moving, parent, after BlockID, graph map[BlockID]graphBlock) *OperationIssue {
	issue := func(handle, message string) *OperationIssue {
		return &OperationIssue{Operation: index, Code: IssueInvalidBlockRelation, Handle: handle, Message: message}
	}
	if parent != "" {
		if _, ok := graph[parent]; !ok {
			return issue(blockHandle(parent), "parent block does not exist")
		}
	}
	if after != "" {
		sibling, ok := graph[after]
		if !ok {
			return issue(blockHandle(after), "after block does not exist")
		}
		if after == moving {
			return issue(blockHandle(after), "block cannot be placed after itself")
		}
		if sibling.parent != parent {
			return issue(blockHandle(after), "after block is not a child of the requested parent")
		}
	}
	return nil
}

func requireRelationItem(index int, blockID BlockID, relation RelationID, item RelationItemID, graph map[BlockID]graphBlock) *OperationIssue {
	block, ok := graph[blockID]
	if !ok {
		return &OperationIssue{Operation: index, Code: IssueUnknownBlock, Handle: blockHandle(blockID), Message: "relation owner block does not exist"}
	}
	items, ok := block.relations[relation]
	if !ok {
		return &OperationIssue{Operation: index, Code: IssueUnknownRelation, Handle: relationHandle(blockID, relation), Message: "relation does not exist"}
	}
	if _, ok := items[item]; !ok {
		return &OperationIssue{Operation: index, Code: IssueUnknownRelationItem, Handle: relationItemHandle(blockID, relation, item), Message: "relation item does not exist"}
	}
	return nil
}

func validateRelationOperationIDs(block BlockID, relation RelationID, item RelationItemID) error {
	if err := validateStableID("block ID", string(block), 160); err != nil {
		return err
	}
	if err := validateStableID("relation ID", string(relation), 120); err != nil {
		return err
	}
	return validateStableID("relation item ID", string(item), 160)
}

func validateRelationPlacement(index int, blockID BlockID, relation RelationID, moving, after RelationItemID, graph map[BlockID]graphBlock) *OperationIssue {
	block, ok := graph[blockID]
	if !ok {
		return &OperationIssue{Operation: index, Code: IssueUnknownBlock, Handle: blockHandle(blockID), Message: "relation owner block does not exist"}
	}
	items := block.relations[relation]
	if after == "" {
		return nil
	}
	if after == moving {
		return &OperationIssue{Operation: index, Code: IssueInvalidRelationItemMove, Handle: relationItemHandle(blockID, relation, moving), Message: "relation item cannot be placed after itself"}
	}
	if _, ok := items[after]; !ok {
		return &OperationIssue{Operation: index, Code: IssueInvalidRelationItemMove, Handle: relationItemHandle(blockID, relation, after), Message: "after relation item does not exist in the target relation"}
	}
	return nil
}

func isDescendant(candidate, ancestor BlockID, graph map[BlockID]graphBlock) bool {
	for candidate != "" {
		if candidate == ancestor {
			return true
		}
		block, ok := graph[candidate]
		if !ok {
			return false
		}
		candidate = block.parent
	}
	return false
}

func applyGraphEffect(operation Operation, graph map[BlockID]graphBlock) {
	switch operation.Kind {
	case OperationInsertBlock:
		graph[operation.InsertBlock.Block] = graphBlock{kind: operation.InsertBlock.Kind, parent: operation.InsertBlock.Parent, relations: make(map[RelationID]map[RelationItemID]RelationItemKind)}
	case OperationDeleteBlock:
		remove := map[BlockID]struct{}{operation.DeleteBlock.Block: {}}
		changed := true
		for changed {
			changed = false
			for id, block := range graph {
				if _, parentRemoved := remove[block.parent]; parentRemoved {
					if _, already := remove[id]; !already {
						remove[id] = struct{}{}
						changed = true
					}
				}
			}
		}
		for id := range remove {
			delete(graph, id)
		}
	case OperationMoveBlock:
		block := graph[operation.MoveBlock.Block]
		block.parent = operation.MoveBlock.Parent
		graph[operation.MoveBlock.Block] = block
	case OperationReplaceBlockKind:
		block := graph[operation.ReplaceBlockKind.Block]
		block.kind = operation.ReplaceBlockKind.Kind
		graph[operation.ReplaceBlockKind.Block] = block
	case OperationInsertRelationItem:
		op := operation.InsertRelationItem
		block := graph[op.Block]
		if block.relations[op.Relation] == nil {
			block.relations[op.Relation] = make(map[RelationItemID]RelationItemKind)
		}
		block.relations[op.Relation][op.Item] = op.Kind
		graph[op.Block] = block
	case OperationDeleteRelationItem:
		op := operation.DeleteRelationItem
		block := graph[op.Block]
		delete(block.relations[op.Relation], op.Item)
		graph[op.Block] = block
	case OperationMoveRelationItem:
		op := operation.MoveRelationItem
		source := graph[op.Block]
		kind := source.relations[op.Relation][op.Item]
		delete(source.relations[op.Relation], op.Item)
		graph[op.Block] = source
		target := graph[op.TargetBlock]
		if target.relations[op.Target] == nil {
			target.relations[op.Target] = make(map[RelationItemID]RelationItemKind)
		}
		target.relations[op.Target][op.Item] = kind
		graph[op.TargetBlock] = target
	}
}

func fieldRuleKey(kind BlockKind, field FieldID) string {
	return string(kind) + "\x00" + string(field)
}

func relationRuleKey(kind BlockKind, relation RelationID) string {
	return string(kind) + "\x00" + string(relation)
}

func relationFieldRuleKey(kind BlockKind, relation RelationID, itemKind RelationItemKind, field FieldID) string {
	return string(kind) + "\x00" + string(relation) + "\x00" + string(itemKind) + "\x00" + string(field)
}

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

func affectedHandles(request ApplyRequest) []string {
	set := make(map[string]struct{}, len(request.Operations))
	for _, operation := range request.Operations {
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
			handles = []string{"translation:" + string(request.Locale)}
		}
		for _, handle := range handles {
			if strings.TrimSpace(handle) != "" && handle != "block:" {
				set[handle] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(set))
	for handle := range set {
		result = append(result, handle)
	}
	sort.Strings(result)
	return result
}
