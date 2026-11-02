package aidocument

import (
	"errors"
	"fmt"
	"slices"
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
		result.Conflict = revisionConflict(ConflictDocumentRevision, document, request)
		return result
	}
	if !equalRevision(document.TargetRevision, request.ExpectedTargetRevision) {
		result.Conflict = revisionConflict(ConflictTargetRevision, document, request)
		return result
	}

	validator := operationValidator{
		role:         document.Role(),
		localeExists: document.LocaleExists,
		batchSize:    len(request.Operations),
		catalog:      newCatalogIndex(document.Catalog),
		graph:        newOperationGraph(document.Nodes),
	}
	for index, operation := range request.Operations {
		validator.index = index
		if issue := validator.validate(operation); issue != nil {
			result.Issues = append(result.Issues, *issue)
			continue
		}
		validator.accept(operation)
	}
	return result
}

func revisionConflict(code ConflictCode, document Document, request ApplyRequest) *Conflict {
	return &Conflict{
		Code:                    code,
		CurrentDocumentRevision: document.DocumentRevision,
		CurrentTargetRevision:   cloneRevision(document.TargetRevision),
		AffectedHandles:         AffectedHandles(request.Operations, request.Locale),
	}
}

func newOperationGraph(nodes []Node) map[BlockID]graphBlock {
	graph := make(map[BlockID]graphBlock, len(nodes))
	for _, node := range nodes {
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
	return graph
}

// operationValidator checks the operation at index against the catalog and
// against the block graph and locale state that the batch's earlier accepted
// operations have left behind.
type operationValidator struct {
	index        int
	role         LocaleRole
	localeExists bool
	batchSize    int
	catalog      catalogIndex
	graph        map[BlockID]graphBlock
}

func (v *operationValidator) accept(operation Operation) {
	applyGraphEffect(operation, v.graph)
	switch operation.Kind {
	case OperationCreateTranslation:
		v.localeExists = true
	case OperationDeleteTranslation:
		v.localeExists = false
	}
}

func (v *operationValidator) issue(code IssueCode, handle, message string) *OperationIssue {
	return &OperationIssue{Operation: v.index, Code: code, Handle: handle, Message: message}
}

func (v *operationValidator) validate(operation Operation) *OperationIssue {
	if err := operation.validateShape(); err != nil {
		return v.issue(IssueInvalidOperation, "", err.Error())
	}
	switch operation.Kind {
	case OperationSetField:
		return v.validateSetField(*operation.SetField)
	case OperationUnsetField:
		return v.validateUnsetField(operation.UnsetField.Target)
	case OperationInsertBlock:
		return v.validateInsertBlock(*operation.InsertBlock)
	case OperationDeleteBlock:
		return v.validateDeleteBlock(operation.DeleteBlock.Block)
	case OperationMoveBlock:
		return v.validateMoveBlock(*operation.MoveBlock)
	case OperationReplaceBlockKind:
		return v.validateReplaceBlockKind(*operation.ReplaceBlockKind)
	case OperationInsertRelationItem:
		return v.validateInsertRelationItem(*operation.InsertRelationItem)
	case OperationDeleteRelationItem:
		return v.validateDeleteRelationItem(*operation.DeleteRelationItem)
	case OperationMoveRelationItem:
		return v.validateMoveRelationItem(*operation.MoveRelationItem)
	case OperationAttachFile:
		return v.validateAttachFile(*operation.AttachFile)
	case OperationDetachFile:
		return v.validateDetachFile(operation.DetachFile.Target)
	default:
		// validateShape admits only known kinds, which leaves the translation lifecycle.
		return v.validateTranslationLifecycle(operation.Kind)
	}
}

func (v *operationValidator) requireSource(handle string) *OperationIssue {
	if v.role == LocaleRoleSource {
		return nil
	}
	return v.issue(IssueSourceAuthorityRequired, handle, "only the current source locale may alter the shared block graph, shared fields, or file relations")
}

func (v *operationValidator) resolveField(target FieldTarget) (resolvedFieldRule, *OperationIssue) {
	if err := validateCanonicalFieldTarget(target); err != nil {
		return resolvedFieldRule{}, v.issue(IssueInvalidOperation, fieldHandle(target), err.Error())
	}
	rule, ruleIssue := v.fieldRule(target)
	if ruleIssue != nil {
		return resolvedFieldRule{}, ruleIssue
	}
	resolved, err := resolveNestedFieldRule(rule, target.Path)
	if err != nil {
		return resolvedFieldRule{}, v.issue(IssueUnknownField, fieldHandle(target), err.Error())
	}
	return resolved, nil
}

// fieldRule finds the catalog rule of a block field or relation-item field
// through the current graph, so fields of blocks and items inserted earlier in
// the batch resolve too.
func (v *operationValidator) fieldRule(target FieldTarget) (FieldRule, *OperationIssue) {
	block, ok := v.graph[target.Block]
	if !ok {
		return FieldRule{}, v.issue(IssueUnknownBlock, blockHandle(target.Block), "field target block does not exist")
	}
	if !target.relationItem() {
		rule, ok := v.catalog.field(block.kind, target.Field)
		if !ok {
			return FieldRule{}, v.issue(IssueUnknownField, fieldHandle(target), "field is not defined for the target block kind")
		}
		return rule, nil
	}
	items, ok := block.relations[target.Relation]
	if !ok {
		return FieldRule{}, v.issue(IssueUnknownRelation, relationHandle(target.Block, target.Relation), "relation does not exist")
	}
	itemKind, ok := items[target.Item]
	if !ok {
		return FieldRule{}, v.issue(IssueUnknownRelationItem, relationItemHandle(target.Block, target.Relation, target.Item), "relation item does not exist")
	}
	rule, ok := v.catalog.relationField(block.kind, target.Relation, itemKind, target.Field)
	if !ok {
		return FieldRule{}, v.issue(IssueUnknownField, fieldHandle(target), "field is not defined for the relation item kind")
	}
	return rule, nil
}

func (v *operationValidator) validateSetField(op SetField) *OperationIssue {
	rule, fieldIssue := v.resolveField(op.Target)
	if fieldIssue != nil {
		return fieldIssue
	}
	handle := fieldHandle(op.Target)
	if rule.file {
		return v.issue(IssueUnknownField, handle, "file fields require a typed file operation")
	}
	if op.Value.Kind != rule.valueKind {
		return v.issue(IssueValueKindMismatch, handle, "field value kind does not match the domain catalog")
	}
	if err := op.Value.validate(); err != nil {
		return v.issue(IssueValueKindMismatch, handle, err.Error())
	}
	if rule.schema != nil {
		if err := validateValueForSchema(op.Value, *rule.schema, 0); err != nil {
			return v.issue(IssueValueKindMismatch, handle, err.Error())
		}
	}
	if v.role == LocaleRoleNonSource && (rule.ownership != FieldOwnershipLocale || !rule.translatable) {
		return v.issue(IssueTargetFieldForbidden, handle, "non-source locales may modify only locale-owned translatable values")
	}
	return nil
}

func (v *operationValidator) validateUnsetField(target FieldTarget) *OperationIssue {
	rule, fieldIssue := v.resolveField(target)
	if fieldIssue != nil {
		return fieldIssue
	}
	if v.role == LocaleRoleNonSource {
		return v.issue(IssueTargetFieldForbidden, fieldHandle(target), "non-source locales cannot unset fields; set an explicit empty value or delete the translation")
	}
	if rule.file {
		return v.issue(IssueTargetFieldForbidden, fieldHandle(target), "file fields require a typed file operation")
	}
	return nil
}

func (v *operationValidator) validateInsertBlock(op InsertBlock) *OperationIssue {
	handle := blockHandle(op.Block)
	if sourceIssue := v.requireSource(handle); sourceIssue != nil {
		return sourceIssue
	}
	if err := validateStableID("block ID", string(op.Block), maxHandleLength); err != nil {
		return v.issue(IssueInvalidOperation, handle, err.Error())
	}
	if _, exists := v.graph[op.Block]; exists {
		return v.issue(IssueDuplicateBlock, handle, "block ID already exists")
	}
	if !v.catalog.hasBlockKind(op.Kind) {
		return v.issue(IssueUnknownBlockKind, handle, "block kind is not in the domain catalog")
	}
	return v.validatePlacement(op.Block, op.Parent, op.After)
}

func (v *operationValidator) validateDeleteBlock(block BlockID) *OperationIssue {
	if sourceIssue := v.requireSource(blockHandle(block)); sourceIssue != nil {
		return sourceIssue
	}
	if _, ok := v.graph[block]; !ok {
		return v.issue(IssueUnknownBlock, blockHandle(block), "block does not exist")
	}
	return nil
}

func (v *operationValidator) validateMoveBlock(op MoveBlock) *OperationIssue {
	handle := blockHandle(op.Block)
	if sourceIssue := v.requireSource(handle); sourceIssue != nil {
		return sourceIssue
	}
	if _, ok := v.graph[op.Block]; !ok {
		return v.issue(IssueUnknownBlock, handle, "block does not exist")
	}
	if op.Parent == op.Block || v.isDescendant(op.Parent, op.Block) {
		return v.issue(IssueBlockCycle, handle, "move would create a block cycle")
	}
	return v.validatePlacement(op.Block, op.Parent, op.After)
}

func (v *operationValidator) validateReplaceBlockKind(op ReplaceBlockKind) *OperationIssue {
	handle := blockHandle(op.Block)
	if sourceIssue := v.requireSource(handle); sourceIssue != nil {
		return sourceIssue
	}
	if _, ok := v.graph[op.Block]; !ok {
		return v.issue(IssueUnknownBlock, handle, "block does not exist")
	}
	if !v.catalog.hasBlockKind(op.Kind) {
		return v.issue(IssueUnknownBlockKind, handle, "block kind is not in the domain catalog")
	}
	return nil
}

func (v *operationValidator) validateInsertRelationItem(op InsertRelationItem) *OperationIssue {
	handle := relationItemHandle(op.Block, op.Relation, op.Item)
	if sourceIssue := v.requireSource(handle); sourceIssue != nil {
		return sourceIssue
	}
	if err := validateRelationOperationIDs(op.Block, op.Relation, op.Item); err != nil {
		return v.issue(IssueInvalidOperation, handle, err.Error())
	}
	if err := validateStableID("relation item kind", string(op.Kind), maxKindLength); err != nil {
		return v.issue(IssueInvalidOperation, handle, err.Error())
	}
	if err := validateAfterRelationItem(op.After); err != nil {
		return v.issue(IssueInvalidOperation, handle, err.Error())
	}
	block, ok := v.graph[op.Block]
	if !ok {
		return v.issue(IssueUnknownBlock, blockHandle(op.Block), "relation owner block does not exist")
	}
	allowed, ok := v.catalog.relationItemKinds(block.kind, op.Relation)
	if !ok {
		return v.issue(IssueUnknownRelation, relationHandle(op.Block, op.Relation), "relation is not defined for the block kind")
	}
	if _, ok := allowed[op.Kind]; !ok {
		return v.issue(IssueInvalidRelationItemMove, handle, "relation item kind is not allowed")
	}
	if v.relationItemExists(op.Item) {
		return v.issue(IssueDuplicateRelationItem, handle, "relation item ID already exists")
	}
	return v.validateRelationPlacement(op.Block, op.Relation, op.Item, op.After)
}

func (v *operationValidator) validateDeleteRelationItem(op DeleteRelationItem) *OperationIssue {
	handle := relationItemHandle(op.Block, op.Relation, op.Item)
	if sourceIssue := v.requireSource(handle); sourceIssue != nil {
		return sourceIssue
	}
	if err := validateRelationOperationIDs(op.Block, op.Relation, op.Item); err != nil {
		return v.issue(IssueInvalidOperation, handle, err.Error())
	}
	return v.requireRelationItem(op.Block, op.Relation, op.Item)
}

func (v *operationValidator) validateMoveRelationItem(op MoveRelationItem) *OperationIssue {
	handle := relationItemHandle(op.Block, op.Relation, op.Item)
	targetHandle := relationItemHandle(op.TargetBlock, op.Target, op.Item)
	if sourceIssue := v.requireSource(handle); sourceIssue != nil {
		return sourceIssue
	}
	if err := validateRelationOperationIDs(op.Block, op.Relation, op.Item); err != nil {
		return v.issue(IssueInvalidOperation, handle, err.Error())
	}
	if err := validateRelationOperationIDs(op.TargetBlock, op.Target, op.Item); err != nil {
		return v.issue(IssueInvalidOperation, targetHandle, err.Error())
	}
	if err := validateAfterRelationItem(op.After); err != nil {
		return v.issue(IssueInvalidOperation, targetHandle, err.Error())
	}
	if itemIssue := v.requireRelationItem(op.Block, op.Relation, op.Item); itemIssue != nil {
		return itemIssue
	}
	kind := v.graph[op.Block].relations[op.Relation][op.Item]
	target, ok := v.graph[op.TargetBlock]
	if !ok {
		return v.issue(IssueUnknownBlock, blockHandle(op.TargetBlock), "target relation owner block does not exist")
	}
	allowed, ok := v.catalog.relationItemKinds(target.kind, op.Target)
	if !ok {
		return v.issue(IssueUnknownRelation, relationHandle(op.TargetBlock, op.Target), "target relation is not defined for the block kind")
	}
	if _, ok := allowed[kind]; !ok {
		return v.issue(IssueInvalidRelationItemMove, handle, "target relation does not allow the item kind")
	}
	return v.validateRelationPlacement(op.TargetBlock, op.Target, op.Item, op.After)
}

func (v *operationValidator) validateAttachFile(op AttachFile) *OperationIssue {
	if slotIssue := v.requireFileSlot(op.Target); slotIssue != nil {
		return slotIssue
	}
	if err := validateFileReference(op.File); err != nil {
		return v.issue(IssueInvalidFileReference, fieldHandle(op.Target), err.Error())
	}
	return nil
}

func (v *operationValidator) validateDetachFile(target FieldTarget) *OperationIssue {
	return v.requireFileSlot(target)
}

func (v *operationValidator) requireFileSlot(target FieldTarget) *OperationIssue {
	if sourceIssue := v.requireSource(fieldHandle(target)); sourceIssue != nil {
		return sourceIssue
	}
	rule, fieldIssue := v.resolveField(target)
	if fieldIssue != nil {
		return fieldIssue
	}
	if !rule.file {
		return v.issue(IssueUnknownField, fieldHandle(target), "field is not a file slot")
	}
	return nil
}

func (v *operationValidator) validateTranslationLifecycle(kind OperationKind) *OperationIssue {
	const handle = "translation"
	create := kind == OperationCreateTranslation
	if v.role == LocaleRoleSource {
		if create {
			return v.issue(IssueTranslationIsSource, handle, "the current source locale cannot be created as a translation")
		}
		return v.issue(IssueTranslationIsSource, handle, "the current source locale cannot be deleted as a translation")
	}
	if v.batchSize != 1 {
		if create {
			return v.issue(IssueLocaleOperationNotExclusive, handle, "translation create must be an exclusive aggregate operation")
		}
		return v.issue(IssueLocaleOperationNotExclusive, handle, "translation delete must be an exclusive aggregate operation")
	}
	if create && v.localeExists {
		return v.issue(IssueTranslationAlreadyExists, handle, "translation locale already exists")
	}
	if !create && !v.localeExists {
		return v.issue(IssueTranslationMissing, handle, "translation locale does not exist")
	}
	return nil
}

func resolveNestedFieldRule(rule FieldRule, path []FieldPathSegment) (resolvedFieldRule, error) {
	if rule.Schema == nil {
		if len(path) != 0 {
			return resolvedFieldRule{}, errors.New("field does not define nested catalog shape")
		}
		return resolvedFieldRule{valueKind: rule.ValueKind, ownership: rule.Ownership, translatable: rule.Translatable, file: rule.File}, nil
	}
	current := rule.Schema
	for _, segment := range path {
		if err := segment.validate(); err != nil {
			return resolvedFieldRule{}, err
		}
		var err error
		if segment.Field != "" {
			current, err = objectFieldSchema(current, segment.Field)
		} else {
			current, err = listItemSchema(current, segment.Item)
		}
		if err != nil {
			return resolvedFieldRule{}, err
		}
	}
	return resolvedFieldRule{
		valueKind: current.Kind, ownership: current.Ownership,
		translatable: current.Translatable, file: current.File, schema: current,
	}, nil
}

func objectFieldSchema(schema *FieldSchema, field FieldID) (*FieldSchema, error) {
	if schema.File || schema.Kind != ValueKindObject {
		return nil, fmt.Errorf("path field %q does not target an object", field)
	}
	for index := range schema.Fields {
		if schema.Fields[index].Field == field {
			return &schema.Fields[index].Schema, nil
		}
	}
	return nil, fmt.Errorf("path field %q is not in the catalog", field)
}

func listItemSchema(schema *FieldSchema, item RelationItemID) (*FieldSchema, error) {
	if schema.File || schema.Kind != ValueKindList || schema.Item == nil {
		return nil, fmt.Errorf("path item %q does not target a list", item)
	}
	switch schema.Identity.Kind {
	case ListIdentityPositional:
		return nil, errors.New("positional lists can only be replaced as a whole typed value")
	case ListIdentityFixed:
		if !slices.Contains(schema.Identity.Handles, item) {
			return nil, fmt.Errorf("fixed list item %q is not in the catalog", item)
		}
	}
	return schema.Item, nil
}

func (v *operationValidator) validatePlacement(moving, parent, after BlockID) *OperationIssue {
	if parent != "" {
		if _, ok := v.graph[parent]; !ok {
			return v.issue(IssueInvalidBlockRelation, blockHandle(parent), "parent block does not exist")
		}
	}
	if after == "" {
		return nil
	}
	sibling, ok := v.graph[after]
	if !ok {
		return v.issue(IssueInvalidBlockRelation, blockHandle(after), "after block does not exist")
	}
	if after == moving {
		return v.issue(IssueInvalidBlockRelation, blockHandle(after), "block cannot be placed after itself")
	}
	if sibling.parent != parent {
		return v.issue(IssueInvalidBlockRelation, blockHandle(after), "after block is not a child of the requested parent")
	}
	return nil
}

func (v *operationValidator) requireRelationItem(blockID BlockID, relation RelationID, item RelationItemID) *OperationIssue {
	block, ok := v.graph[blockID]
	if !ok {
		return v.issue(IssueUnknownBlock, blockHandle(blockID), "relation owner block does not exist")
	}
	items, ok := block.relations[relation]
	if !ok {
		return v.issue(IssueUnknownRelation, relationHandle(blockID, relation), "relation does not exist")
	}
	if _, ok := items[item]; !ok {
		return v.issue(IssueUnknownRelationItem, relationItemHandle(blockID, relation, item), "relation item does not exist")
	}
	return nil
}

// relationItemExists reports whether any relation of any block already holds
// item, because relation item IDs are unique across the whole document.
func (v *operationValidator) relationItemExists(item RelationItemID) bool {
	for _, block := range v.graph {
		for _, items := range block.relations {
			if _, ok := items[item]; ok {
				return true
			}
		}
	}
	return false
}

func validateRelationOperationIDs(block BlockID, relation RelationID, item RelationItemID) error {
	if err := validateStableID("block ID", string(block), maxHandleLength); err != nil {
		return err
	}
	if err := validateStableID("relation ID", string(relation), maxCatalogIDLength); err != nil {
		return err
	}
	return validateStableID("relation item ID", string(item), maxHandleLength)
}

func validateAfterRelationItem(after RelationItemID) error {
	if after == "" {
		return nil
	}
	return validateStableID("after relation item ID", string(after), maxHandleLength)
}

func (v *operationValidator) validateRelationPlacement(blockID BlockID, relation RelationID, moving, after RelationItemID) *OperationIssue {
	block, ok := v.graph[blockID]
	if !ok {
		return v.issue(IssueUnknownBlock, blockHandle(blockID), "relation owner block does not exist")
	}
	if after == "" {
		return nil
	}
	if after == moving {
		return v.issue(IssueInvalidRelationItemMove, relationItemHandle(blockID, relation, moving), "relation item cannot be placed after itself")
	}
	if _, ok := block.relations[relation][after]; !ok {
		return v.issue(IssueInvalidRelationItemMove, relationItemHandle(blockID, relation, after), "after relation item does not exist in the target relation")
	}
	return nil
}

func (v *operationValidator) isDescendant(candidate, ancestor BlockID) bool {
	for candidate != "" {
		if candidate == ancestor {
			return true
		}
		block, ok := v.graph[candidate]
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
