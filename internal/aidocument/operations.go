package aidocument

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

type OperationKind string

const (
	OperationSetField           OperationKind = "set_field"
	OperationUnsetField         OperationKind = "unset_field"
	OperationInsertBlock        OperationKind = "insert_block"
	OperationDeleteBlock        OperationKind = "delete_block"
	OperationMoveBlock          OperationKind = "move_block"
	OperationReplaceBlockKind   OperationKind = "replace_block_kind"
	OperationInsertRelationItem OperationKind = "insert_relation_item"
	OperationDeleteRelationItem OperationKind = "delete_relation_item"
	OperationMoveRelationItem   OperationKind = "move_relation_item"
	OperationAttachFile         OperationKind = "attach_file"
	OperationDetachFile         OperationKind = "detach_file"
	OperationCreateTranslation  OperationKind = "create_translation"
	OperationDeleteTranslation  OperationKind = "delete_translation"
)

type FieldTarget struct {
	Block    BlockID
	Relation RelationID
	Item     RelationItemID
	Field    FieldID
	Path     []FieldPathSegment
}

type FieldPathSegment struct {
	Field FieldID
	Item  RelationItemID
}

func ObjectPath(field FieldID) FieldPathSegment     { return FieldPathSegment{Field: field} }
func ListPath(item RelationItemID) FieldPathSegment { return FieldPathSegment{Item: item} }

func (s FieldPathSegment) validate() error {
	if (s.Field == "") == (s.Item == "") {
		return errors.New("field path segment must contain exactly one field or list item handle")
	}
	if s.Field != "" {
		return validateStableID("nested field ID", string(s.Field), maxCatalogIDLength)
	}
	return validateStableID("list item handle", string(s.Item), maxHandleLength)
}

func (s FieldPathSegment) MarshalJSON() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(wireFieldPathSegment(s))
}

func (s *FieldPathSegment) UnmarshalJSON(data []byte) error {
	var wire wireFieldPathSegment
	if err := decodeStrict(data, &wire); err != nil {
		return fmt.Errorf("field path segment: %w", err)
	}
	*s = FieldPathSegment(wire)
	return s.validate()
}

type wireFieldPathSegment struct {
	Field FieldID        `json:"field,omitempty"`
	Item  RelationItemID `json:"item_id,omitempty"`
}

func (t FieldTarget) relationItem() bool { return t.Relation != "" || t.Item != "" }

type SetField struct {
	Target FieldTarget
	Value  Value
}

type UnsetField struct{ Target FieldTarget }

type InsertBlock struct {
	Block  BlockID
	Kind   BlockKind
	Parent BlockID
	After  BlockID
}

type DeleteBlock struct{ Block BlockID }

type MoveBlock struct {
	Block  BlockID
	Parent BlockID
	After  BlockID
}

type ReplaceBlockKind struct {
	Block BlockID
	Kind  BlockKind
}

type InsertRelationItem struct {
	Block    BlockID
	Relation RelationID
	Item     RelationItemID
	Kind     RelationItemKind
	After    RelationItemID
}

type DeleteRelationItem struct {
	Block    BlockID
	Relation RelationID
	Item     RelationItemID
}

type MoveRelationItem struct {
	Block       BlockID
	Relation    RelationID
	Item        RelationItemID
	TargetBlock BlockID
	Target      RelationID
	After       RelationItemID
}

type AttachFile struct {
	Target FieldTarget
	File   FileReference
}

type DetachFile struct{ Target FieldTarget }
type CreateTranslation struct{}
type DeleteTranslation struct{}

// Operation is a closed tagged union. Exactly the payload matching Kind must
// be present; generic JSON paths and untyped patches are not representable.
type Operation struct {
	Kind               OperationKind
	SetField           *SetField
	UnsetField         *UnsetField
	InsertBlock        *InsertBlock
	DeleteBlock        *DeleteBlock
	MoveBlock          *MoveBlock
	ReplaceBlockKind   *ReplaceBlockKind
	InsertRelationItem *InsertRelationItem
	DeleteRelationItem *DeleteRelationItem
	MoveRelationItem   *MoveRelationItem
	AttachFile         *AttachFile
	DetachFile         *DetachFile
	CreateTranslation  *CreateTranslation
	DeleteTranslation  *DeleteTranslation
}

func SetFieldOperation(block BlockID, field FieldID, value Value) Operation {
	return Operation{Kind: OperationSetField, SetField: &SetField{Target: FieldTarget{Block: block, Field: field}, Value: value}}
}

func UnsetFieldOperation(block BlockID, field FieldID) Operation {
	return Operation{Kind: OperationUnsetField, UnsetField: &UnsetField{Target: FieldTarget{Block: block, Field: field}}}
}

func SetNestedFieldOperation(block BlockID, field FieldID, path []FieldPathSegment, value Value) Operation {
	return Operation{Kind: OperationSetField, SetField: &SetField{Target: FieldTarget{Block: block, Field: field, Path: append([]FieldPathSegment(nil), path...)}, Value: value}}
}

func UnsetNestedFieldOperation(block BlockID, field FieldID, path []FieldPathSegment) Operation {
	return Operation{Kind: OperationUnsetField, UnsetField: &UnsetField{Target: FieldTarget{Block: block, Field: field, Path: append([]FieldPathSegment(nil), path...)}}}
}

func SetRelationFieldOperation(block BlockID, relation RelationID, item RelationItemID, field FieldID, value Value) Operation {
	return Operation{Kind: OperationSetField, SetField: &SetField{Target: FieldTarget{Block: block, Relation: relation, Item: item, Field: field}, Value: value}}
}

func UnsetRelationFieldOperation(block BlockID, relation RelationID, item RelationItemID, field FieldID) Operation {
	return Operation{Kind: OperationUnsetField, UnsetField: &UnsetField{Target: FieldTarget{Block: block, Relation: relation, Item: item, Field: field}}}
}

func InsertBlockOperation(block BlockID, kind BlockKind, parent, after BlockID) Operation {
	return Operation{Kind: OperationInsertBlock, InsertBlock: &InsertBlock{Block: block, Kind: kind, Parent: parent, After: after}}
}

func DeleteBlockOperation(block BlockID) Operation {
	return Operation{Kind: OperationDeleteBlock, DeleteBlock: &DeleteBlock{Block: block}}
}

func MoveBlockOperation(block, parent, after BlockID) Operation {
	return Operation{Kind: OperationMoveBlock, MoveBlock: &MoveBlock{Block: block, Parent: parent, After: after}}
}

func ReplaceBlockKindOperation(block BlockID, kind BlockKind) Operation {
	return Operation{Kind: OperationReplaceBlockKind, ReplaceBlockKind: &ReplaceBlockKind{Block: block, Kind: kind}}
}

func InsertRelationItemOperation(block BlockID, relation RelationID, item RelationItemID, kind RelationItemKind, after RelationItemID) Operation {
	return Operation{Kind: OperationInsertRelationItem, InsertRelationItem: &InsertRelationItem{Block: block, Relation: relation, Item: item, Kind: kind, After: after}}
}

func DeleteRelationItemOperation(block BlockID, relation RelationID, item RelationItemID) Operation {
	return Operation{Kind: OperationDeleteRelationItem, DeleteRelationItem: &DeleteRelationItem{Block: block, Relation: relation, Item: item}}
}

func MoveRelationItemOperation(block BlockID, relation RelationID, item RelationItemID, targetBlock BlockID, target RelationID, after RelationItemID) Operation {
	return Operation{Kind: OperationMoveRelationItem, MoveRelationItem: &MoveRelationItem{Block: block, Relation: relation, Item: item, TargetBlock: targetBlock, Target: target, After: after}}
}

func AttachFileOperation(block BlockID, field FieldID, file FileReference) Operation {
	return Operation{Kind: OperationAttachFile, AttachFile: &AttachFile{Target: FieldTarget{Block: block, Field: field}, File: file}}
}

func DetachFileOperation(block BlockID, field FieldID) Operation {
	return Operation{Kind: OperationDetachFile, DetachFile: &DetachFile{Target: FieldTarget{Block: block, Field: field}}}
}

func AttachRelationFileOperation(block BlockID, relation RelationID, item RelationItemID, field FieldID, file FileReference) Operation {
	return Operation{Kind: OperationAttachFile, AttachFile: &AttachFile{Target: FieldTarget{Block: block, Relation: relation, Item: item, Field: field}, File: file}}
}

func DetachRelationFileOperation(block BlockID, relation RelationID, item RelationItemID, field FieldID) Operation {
	return Operation{Kind: OperationDetachFile, DetachFile: &DetachFile{Target: FieldTarget{Block: block, Relation: relation, Item: item, Field: field}}}
}

func CreateTranslationOperation() Operation {
	return Operation{Kind: OperationCreateTranslation, CreateTranslation: &CreateTranslation{}}
}

func DeleteTranslationOperation() Operation {
	return Operation{Kind: OperationDeleteTranslation, DeleteTranslation: &DeleteTranslation{}}
}

func (o Operation) payloadCount() int {
	count := 0
	for _, present := range []bool{
		o.SetField != nil, o.UnsetField != nil, o.InsertBlock != nil, o.DeleteBlock != nil,
		o.MoveBlock != nil, o.ReplaceBlockKind != nil, o.AttachFile != nil, o.DetachFile != nil,
		o.InsertRelationItem != nil, o.DeleteRelationItem != nil, o.MoveRelationItem != nil,
		o.CreateTranslation != nil, o.DeleteTranslation != nil,
	} {
		if present {
			count++
		}
	}
	return count
}

func (o Operation) validateShape() error {
	if o.payloadCount() != 1 {
		return errors.New("operation must contain exactly one typed payload")
	}
	valid := (o.Kind == OperationSetField && o.SetField != nil) ||
		(o.Kind == OperationUnsetField && o.UnsetField != nil) ||
		(o.Kind == OperationInsertBlock && o.InsertBlock != nil) ||
		(o.Kind == OperationDeleteBlock && o.DeleteBlock != nil) ||
		(o.Kind == OperationMoveBlock && o.MoveBlock != nil) ||
		(o.Kind == OperationReplaceBlockKind && o.ReplaceBlockKind != nil) ||
		(o.Kind == OperationInsertRelationItem && o.InsertRelationItem != nil) ||
		(o.Kind == OperationDeleteRelationItem && o.DeleteRelationItem != nil) ||
		(o.Kind == OperationMoveRelationItem && o.MoveRelationItem != nil) ||
		(o.Kind == OperationAttachFile && o.AttachFile != nil) ||
		(o.Kind == OperationDetachFile && o.DetachFile != nil) ||
		(o.Kind == OperationCreateTranslation && o.CreateTranslation != nil) ||
		(o.Kind == OperationDeleteTranslation && o.DeleteTranslation != nil)
	if !valid {
		return fmt.Errorf("operation kind %q does not match its payload", o.Kind)
	}
	return nil
}

// wireOperation is the JSON shape of Operation: op names the operation and
// decides which of the other fields it carries. A field operation addresses its
// field with block_id, field and, for a relation item, relation_id and item_id.
type wireOperation struct {
	Op               OperationKind      `json:"op"`
	Block            *BlockID           `json:"block_id,omitempty"`
	Relation         *RelationID        `json:"relation_id,omitempty"`
	Item             *RelationItemID    `json:"item_id,omitempty"`
	Field            *FieldID           `json:"field,omitempty"`
	Path             []FieldPathSegment `json:"path,omitempty"`
	Value            *Value             `json:"value,omitempty"`
	File             *FileReference     `json:"file_id,omitempty"`
	Kind             *string            `json:"kind,omitempty"`
	Parent           *BlockID           `json:"parent_block_id,omitempty"`
	AfterBlock       *BlockID           `json:"after_block_id,omitempty"`
	AfterItem        *RelationItemID    `json:"after_item_id,omitempty"`
	TargetBlock      *BlockID           `json:"target_block_id,omitempty"`
	TargetRelationID *RelationID        `json:"target_relation_id,omitempty"`
}

func wireFieldOperation(op OperationKind, target FieldTarget) wireOperation {
	wire := wireOperation{Op: op, Block: &target.Block, Field: &target.Field, Path: target.Path}
	if target.Relation != "" {
		wire.Relation = &target.Relation
	}
	if target.Item != "" {
		wire.Item = &target.Item
	}
	return wire
}

// optionalHandle leaves out the empty handle that means "none".
func optionalHandle[T ~string](value T) *T {
	if value == "" {
		return nil
	}
	return &value
}

func kindString[T ~string](kind T) *string {
	value := string(kind)
	return &value
}

func (o Operation) MarshalJSON() ([]byte, error) {
	if err := o.validateShape(); err != nil {
		return nil, err
	}
	wire := wireOperation{Op: o.Kind}
	switch o.Kind {
	case OperationSetField:
		wire = wireFieldOperation(o.Kind, o.SetField.Target)
		wire.Value = &o.SetField.Value
	case OperationUnsetField:
		wire = wireFieldOperation(o.Kind, o.UnsetField.Target)
	case OperationInsertBlock:
		op := o.InsertBlock
		wire.Block, wire.Kind, wire.Parent, wire.AfterBlock = &op.Block, kindString(op.Kind), optionalHandle(op.Parent), optionalHandle(op.After)
	case OperationDeleteBlock:
		wire.Block = &o.DeleteBlock.Block
	case OperationMoveBlock:
		op := o.MoveBlock
		wire.Block, wire.Parent, wire.AfterBlock = &op.Block, optionalHandle(op.Parent), optionalHandle(op.After)
	case OperationReplaceBlockKind:
		op := o.ReplaceBlockKind
		wire.Block, wire.Kind = &op.Block, kindString(op.Kind)
	case OperationInsertRelationItem:
		op := o.InsertRelationItem
		wire.Block, wire.Relation, wire.Item, wire.Kind, wire.AfterItem = &op.Block, &op.Relation, &op.Item, kindString(op.Kind), optionalHandle(op.After)
	case OperationDeleteRelationItem:
		op := o.DeleteRelationItem
		wire.Block, wire.Relation, wire.Item = &op.Block, &op.Relation, &op.Item
	case OperationMoveRelationItem:
		op := o.MoveRelationItem
		wire.Block, wire.Relation, wire.Item = &op.Block, &op.Relation, &op.Item
		wire.TargetBlock, wire.TargetRelationID, wire.AfterItem = &op.TargetBlock, &op.Target, optionalHandle(op.After)
	case OperationAttachFile:
		wire = wireFieldOperation(o.Kind, o.AttachFile.Target)
		wire.File = &o.AttachFile.File
	case OperationDetachFile:
		wire = wireFieldOperation(o.Kind, o.DetachFile.Target)
	case OperationCreateTranslation, OperationDeleteTranslation:
	default:
		return nil, fmt.Errorf("unsupported operation %q", o.Kind)
	}
	return json.Marshal(wire)
}

func (o *Operation) UnmarshalJSON(data []byte) error {
	*o = Operation{}
	var wire wireOperation
	if err := decodeStrict(data, &wire); err != nil {
		return fmt.Errorf("operation: %w", err)
	}
	decoded, err := wire.operation()
	if err != nil {
		return err
	}
	*o = decoded
	return o.validateShape()
}

// require reports a missing field, and any field the operation does not take.
func (w wireOperation) require(fields ...string) error {
	present := map[string]bool{
		"block_id": w.Block != nil, "relation_id": w.Relation != nil, "item_id": w.Item != nil, "field": w.Field != nil,
		"path": w.Path != nil, "value": w.Value != nil, "file_id": w.File != nil, "kind": w.Kind != nil,
		"parent_block_id": w.Parent != nil, "after_block_id": w.AfterBlock != nil, "after_item_id": w.AfterItem != nil,
		"target_block_id": w.TargetBlock != nil, "target_relation_id": w.TargetRelationID != nil,
	}
	optional := wireOperationOptionalFields[w.Op]
	for _, field := range fields {
		if !present[field] {
			return fmt.Errorf("operation %q needs %s", w.Op, field)
		}
	}
	for field, set := range present {
		if set && !slices.Contains(fields, field) && !slices.Contains(optional, field) {
			return fmt.Errorf("operation %q does not take %s", w.Op, field)
		}
	}
	return nil
}

var (
	wireFieldAddressOptional    = []string{"relation_id", "item_id", "path"}
	wireOperationOptionalFields = map[OperationKind][]string{
		OperationSetField:           wireFieldAddressOptional,
		OperationUnsetField:         wireFieldAddressOptional,
		OperationAttachFile:         wireFieldAddressOptional,
		OperationDetachFile:         wireFieldAddressOptional,
		OperationInsertBlock:        {"parent_block_id", "after_block_id"},
		OperationMoveBlock:          {"parent_block_id", "after_block_id"},
		OperationInsertRelationItem: {"after_item_id"},
		OperationMoveRelationItem:   {"after_item_id"},
	}
)

func handleOrEmpty[T ~string](value *T) T {
	if value == nil {
		return ""
	}
	return *value
}

func (w wireOperation) fieldTarget() FieldTarget {
	return FieldTarget{
		Block: handleOrEmpty(w.Block), Relation: handleOrEmpty(w.Relation), Item: handleOrEmpty(w.Item),
		Field: handleOrEmpty(w.Field), Path: w.Path,
	}
}

func (w wireOperation) operation() (Operation, error) {
	var required []string
	switch w.Op {
	case OperationSetField:
		required = []string{"block_id", "field", "value"}
	case OperationAttachFile:
		required = []string{"block_id", "field", "file_id"}
	case OperationUnsetField, OperationDetachFile:
		required = []string{"block_id", "field"}
	case OperationInsertBlock, OperationReplaceBlockKind:
		required = []string{"block_id", "kind"}
	case OperationDeleteBlock, OperationMoveBlock:
		required = []string{"block_id"}
	case OperationInsertRelationItem:
		required = []string{"block_id", "relation_id", "item_id", "kind"}
	case OperationDeleteRelationItem:
		required = []string{"block_id", "relation_id", "item_id"}
	case OperationMoveRelationItem:
		required = []string{"block_id", "relation_id", "item_id", "target_block_id", "target_relation_id"}
	case OperationCreateTranslation, OperationDeleteTranslation:
	default:
		return Operation{}, fmt.Errorf("unsupported operation %q", w.Op)
	}
	if err := w.require(required...); err != nil {
		return Operation{}, err
	}
	if w.Path != nil && len(w.Path) == 0 {
		return Operation{}, errors.New("field path must not be empty; omit it for a whole-field operation")
	}
	block := handleOrEmpty(w.Block)
	switch w.Op {
	case OperationSetField:
		return Operation{Kind: w.Op, SetField: &SetField{Target: w.fieldTarget(), Value: *w.Value}}, nil
	case OperationUnsetField:
		return Operation{Kind: w.Op, UnsetField: &UnsetField{Target: w.fieldTarget()}}, nil
	case OperationAttachFile:
		return Operation{Kind: w.Op, AttachFile: &AttachFile{Target: w.fieldTarget(), File: *w.File}}, nil
	case OperationDetachFile:
		return Operation{Kind: w.Op, DetachFile: &DetachFile{Target: w.fieldTarget()}}, nil
	case OperationInsertBlock:
		return InsertBlockOperation(block, BlockKind(*w.Kind), handleOrEmpty(w.Parent), handleOrEmpty(w.AfterBlock)), nil
	case OperationDeleteBlock:
		return DeleteBlockOperation(block), nil
	case OperationMoveBlock:
		return MoveBlockOperation(block, handleOrEmpty(w.Parent), handleOrEmpty(w.AfterBlock)), nil
	case OperationReplaceBlockKind:
		return ReplaceBlockKindOperation(block, BlockKind(*w.Kind)), nil
	case OperationInsertRelationItem:
		return InsertRelationItemOperation(block, *w.Relation, *w.Item, RelationItemKind(*w.Kind), handleOrEmpty(w.AfterItem)), nil
	case OperationDeleteRelationItem:
		return DeleteRelationItemOperation(block, *w.Relation, *w.Item), nil
	case OperationMoveRelationItem:
		return MoveRelationItemOperation(block, *w.Relation, *w.Item, *w.TargetBlock, *w.TargetRelationID, handleOrEmpty(w.AfterItem)), nil
	case OperationCreateTranslation:
		return CreateTranslationOperation(), nil
	default:
		return DeleteTranslationOperation(), nil
	}
}

type ApplyRequest struct {
	Protocol                 string            `json:"-"`
	Profile                  Domain            `json:"document_type"`
	Document                 DocumentReference `json:"document_id"`
	Locale                   Locale            `json:"locale"`
	ExpectedDocumentRevision Revision          `json:"expected_document_revision"`
	ExpectedTargetRevision   *Revision         `json:"expected_target_revision,omitempty"`
	Operations               []Operation       `json:"operations"`
}

func (r ApplyRequest) Identity() DocumentIdentity {
	return DocumentIdentity{Domain: r.Profile, Reference: r.Document}
}

func (r ApplyRequest) validateEnvelope() error {
	if r.Protocol != ProtocolVersion {
		return fmt.Errorf("unsupported protocol %q", r.Protocol)
	}
	if err := r.Identity().validate(); err != nil {
		return err
	}
	if err := validateLocale(r.Locale); err != nil {
		return err
	}
	if err := validateOpaque("expected document revision", string(r.ExpectedDocumentRevision), 256); err != nil {
		return err
	}
	if r.ExpectedTargetRevision != nil {
		if err := validateOpaque("expected target revision", string(*r.ExpectedTargetRevision), 256); err != nil {
			return err
		}
	}
	if len(r.Operations) == 0 || len(r.Operations) > 100 {
		return errors.New("apply requires 1 to 100 operations")
	}
	for index, operation := range r.Operations {
		if err := operation.validateShape(); err != nil {
			return fmt.Errorf("operation %d: %w", index, err)
		}
	}
	return nil
}

func validateCanonicalOperations(operations []Operation) error {
	for index, operation := range operations {
		if err := validateCanonicalOperation(operation); err != nil {
			return fmt.Errorf("operation %d: %w", index, err)
		}
	}
	return nil
}

func validateCanonicalOperation(operation Operation) error {
	switch operation.Kind {
	case OperationSetField:
		if err := validateCanonicalFieldTarget(operation.SetField.Target); err != nil {
			return err
		}
		return operation.SetField.Value.validate()
	case OperationUnsetField:
		return validateCanonicalFieldTarget(operation.UnsetField.Target)
	case OperationInsertBlock:
		op := operation.InsertBlock
		if err := validateStableID("block ID", string(op.Block), maxHandleLength); err != nil {
			return err
		}
		if err := validateStableID("block kind", string(op.Kind), maxKindLength); err != nil {
			return err
		}
		return validateCanonicalPlacement(op.Parent, op.After)
	case OperationDeleteBlock:
		return validateStableID("block ID", string(operation.DeleteBlock.Block), maxHandleLength)
	case OperationMoveBlock:
		op := operation.MoveBlock
		if err := validateStableID("block ID", string(op.Block), maxHandleLength); err != nil {
			return err
		}
		return validateCanonicalPlacement(op.Parent, op.After)
	case OperationReplaceBlockKind:
		if err := validateStableID("block ID", string(operation.ReplaceBlockKind.Block), maxHandleLength); err != nil {
			return err
		}
		return validateStableID("block kind", string(operation.ReplaceBlockKind.Kind), maxKindLength)
	case OperationInsertRelationItem:
		op := operation.InsertRelationItem
		if err := validateRelationOperationIDs(op.Block, op.Relation, op.Item); err != nil {
			return err
		}
		if err := validateStableID("relation item kind", string(op.Kind), maxKindLength); err != nil {
			return err
		}
		return validateAfterRelationItem(op.After)
	case OperationDeleteRelationItem:
		op := operation.DeleteRelationItem
		return validateRelationOperationIDs(op.Block, op.Relation, op.Item)
	case OperationMoveRelationItem:
		return validateCanonicalMoveRelationItem(*operation.MoveRelationItem)
	case OperationAttachFile:
		if err := validateCanonicalFieldTarget(operation.AttachFile.Target); err != nil {
			return err
		}
		return validateFileReference(operation.AttachFile.File)
	case OperationDetachFile:
		return validateCanonicalFieldTarget(operation.DetachFile.Target)
	case OperationCreateTranslation, OperationDeleteTranslation:
		// Locale is carried once by the batch envelope.
		return nil
	default:
		return fmt.Errorf("unsupported operation kind %q", operation.Kind)
	}
}

func validateCanonicalPlacement(parent, after BlockID) error {
	if parent != "" {
		if err := validateStableID("parent block ID", string(parent), maxHandleLength); err != nil {
			return err
		}
	}
	if after == "" {
		return nil
	}
	return validateStableID("after block ID", string(after), maxHandleLength)
}

func validateCanonicalMoveRelationItem(op MoveRelationItem) error {
	if err := validateRelationOperationIDs(op.Block, op.Relation, op.Item); err != nil {
		return err
	}
	if err := validateStableID("target block ID", string(op.TargetBlock), maxHandleLength); err != nil {
		return err
	}
	if err := validateStableID("target relation ID", string(op.Target), maxCatalogIDLength); err != nil {
		return err
	}
	return validateAfterRelationItem(op.After)
}
