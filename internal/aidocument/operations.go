package aidocument

import (
	"encoding/json"
	"errors"
	"fmt"
)

type OperationKind string

const (
	OperationSetField           OperationKind = "fs"
	OperationUnsetField         OperationKind = "fu"
	OperationInsertBlock        OperationKind = "bi"
	OperationDeleteBlock        OperationKind = "bd"
	OperationMoveBlock          OperationKind = "bm"
	OperationReplaceBlockKind   OperationKind = "bk"
	OperationInsertRelationItem OperationKind = "ri"
	OperationDeleteRelationItem OperationKind = "rd"
	OperationMoveRelationItem   OperationKind = "rm"
	OperationAttachFile         OperationKind = "fa"
	OperationDetachFile         OperationKind = "fd"
	OperationCreateTranslation  OperationKind = "lc"
	OperationDeleteTranslation  OperationKind = "ld"
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
		return validateStableID("nested field ID", string(s.Field), 120)
	}
	return validateStableID("list item handle", string(s.Item), 160)
}

func (s FieldPathSegment) MarshalJSON() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if s.Field != "" {
		return json.Marshal([]any{"f", s.Field})
	}
	return json.Marshal([]any{"i", s.Item})
}

func (s *FieldPathSegment) UnmarshalJSON(data []byte) error {
	*s = FieldPathSegment{}
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 2 {
		return errors.New("compact field path segment must contain exactly two items")
	}
	var kind string
	if err := json.Unmarshal(parts[0], &kind); err != nil {
		return err
	}
	switch kind {
	case "f":
		if err := json.Unmarshal(parts[1], &s.Field); err != nil {
			return err
		}
	case "i":
		if err := json.Unmarshal(parts[1], &s.Item); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported field path segment %q", kind)
	}
	return s.validate()
}

func (t FieldTarget) relationItem() bool { return t.Relation != "" || t.Item != "" }

func (t FieldTarget) MarshalJSON() ([]byte, error) {
	if len(t.Path) != 0 {
		return json.Marshal([]any{t.Block, t.Relation, t.Item, t.Field, t.Path})
	}
	return json.Marshal([]any{t.Block, t.Relation, t.Item, t.Field})
}

func (t *FieldTarget) UnmarshalJSON(data []byte) error {
	*t = FieldTarget{}
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 4 && len(parts) != 5 {
		return errors.New("compact field target must contain four items plus an optional typed path")
	}
	targets := []any{&t.Block, &t.Relation, &t.Item, &t.Field}
	for index := range targets {
		if err := json.Unmarshal(parts[index], targets[index]); err != nil {
			return fmt.Errorf("compact field target item %d: %w", index, err)
		}
	}
	if len(parts) == 5 {
		if err := json.Unmarshal(parts[4], &t.Path); err != nil {
			return fmt.Errorf("compact field target path: %w", err)
		}
	}
	return nil
}

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

func (o Operation) MarshalJSON() ([]byte, error) {
	if err := o.validateShape(); err != nil {
		return nil, err
	}
	switch o.Kind {
	case OperationSetField:
		return json.Marshal([]any{o.Kind, o.SetField.Target, o.SetField.Value})
	case OperationUnsetField:
		return json.Marshal([]any{o.Kind, o.UnsetField.Target})
	case OperationInsertBlock:
		return json.Marshal([]any{o.Kind, o.InsertBlock.Block, o.InsertBlock.Kind, o.InsertBlock.Parent, o.InsertBlock.After})
	case OperationDeleteBlock:
		return json.Marshal([]any{o.Kind, o.DeleteBlock.Block})
	case OperationMoveBlock:
		return json.Marshal([]any{o.Kind, o.MoveBlock.Block, o.MoveBlock.Parent, o.MoveBlock.After})
	case OperationReplaceBlockKind:
		return json.Marshal([]any{o.Kind, o.ReplaceBlockKind.Block, o.ReplaceBlockKind.Kind})
	case OperationInsertRelationItem:
		return json.Marshal([]any{o.Kind, o.InsertRelationItem.Block, o.InsertRelationItem.Relation, o.InsertRelationItem.Item, o.InsertRelationItem.Kind, o.InsertRelationItem.After})
	case OperationDeleteRelationItem:
		return json.Marshal([]any{o.Kind, o.DeleteRelationItem.Block, o.DeleteRelationItem.Relation, o.DeleteRelationItem.Item})
	case OperationMoveRelationItem:
		return json.Marshal([]any{o.Kind, o.MoveRelationItem.Block, o.MoveRelationItem.Relation, o.MoveRelationItem.Item, o.MoveRelationItem.TargetBlock, o.MoveRelationItem.Target, o.MoveRelationItem.After})
	case OperationAttachFile:
		return json.Marshal([]any{o.Kind, o.AttachFile.Target, o.AttachFile.File})
	case OperationDetachFile:
		return json.Marshal([]any{o.Kind, o.DetachFile.Target})
	case OperationCreateTranslation, OperationDeleteTranslation:
		return json.Marshal([]any{o.Kind})
	default:
		return nil, fmt.Errorf("unsupported operation kind %q", o.Kind)
	}
}

func (o *Operation) UnmarshalJSON(data []byte) error {
	*o = Operation{}
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) == 0 {
		return errors.New("compact operation cannot be empty")
	}
	if err := json.Unmarshal(parts[0], &o.Kind); err != nil {
		return err
	}
	decode := func(want int, targets ...any) error {
		if len(parts) != want {
			return fmt.Errorf("operation %q must contain exactly %d items", o.Kind, want)
		}
		for index, target := range targets {
			if err := json.Unmarshal(parts[index+1], target); err != nil {
				return fmt.Errorf("operation %q item %d: %w", o.Kind, index+1, err)
			}
		}
		return nil
	}
	switch o.Kind {
	case OperationSetField:
		payload := &SetField{}
		if err := decode(3, &payload.Target, &payload.Value); err != nil {
			return err
		}
		switch {
		case payload.Target.relationItem() && len(payload.Target.Path) == 0:
			*o = SetRelationFieldOperation(
				payload.Target.Block,
				payload.Target.Relation,
				payload.Target.Item,
				payload.Target.Field,
				payload.Value,
			)
		case !payload.Target.relationItem() && len(payload.Target.Path) == 0:
			*o = SetFieldOperation(payload.Target.Block, payload.Target.Field, payload.Value)
		case !payload.Target.relationItem():
			*o = SetNestedFieldOperation(
				payload.Target.Block,
				payload.Target.Field,
				payload.Target.Path,
				payload.Value,
			)
		default:
			o.SetField = payload
		}
	case OperationUnsetField:
		payload := &UnsetField{}
		if err := decode(2, &payload.Target); err != nil {
			return err
		}
		switch {
		case payload.Target.relationItem() && len(payload.Target.Path) == 0:
			*o = UnsetRelationFieldOperation(
				payload.Target.Block,
				payload.Target.Relation,
				payload.Target.Item,
				payload.Target.Field,
			)
		case !payload.Target.relationItem() && len(payload.Target.Path) == 0:
			*o = UnsetFieldOperation(payload.Target.Block, payload.Target.Field)
		case !payload.Target.relationItem():
			*o = UnsetNestedFieldOperation(
				payload.Target.Block,
				payload.Target.Field,
				payload.Target.Path,
			)
		default:
			o.UnsetField = payload
		}
	case OperationInsertBlock:
		payload := &InsertBlock{}
		if err := decode(5, &payload.Block, &payload.Kind, &payload.Parent, &payload.After); err != nil {
			return err
		}
		o.InsertBlock = payload
	case OperationDeleteBlock:
		payload := &DeleteBlock{}
		if err := decode(2, &payload.Block); err != nil {
			return err
		}
		o.DeleteBlock = payload
	case OperationMoveBlock:
		payload := &MoveBlock{}
		if err := decode(4, &payload.Block, &payload.Parent, &payload.After); err != nil {
			return err
		}
		o.MoveBlock = payload
	case OperationReplaceBlockKind:
		payload := &ReplaceBlockKind{}
		if err := decode(3, &payload.Block, &payload.Kind); err != nil {
			return err
		}
		o.ReplaceBlockKind = payload
	case OperationInsertRelationItem:
		payload := &InsertRelationItem{}
		if err := decode(6, &payload.Block, &payload.Relation, &payload.Item, &payload.Kind, &payload.After); err != nil {
			return err
		}
		o.InsertRelationItem = payload
	case OperationDeleteRelationItem:
		payload := &DeleteRelationItem{}
		if err := decode(4, &payload.Block, &payload.Relation, &payload.Item); err != nil {
			return err
		}
		o.DeleteRelationItem = payload
	case OperationMoveRelationItem:
		payload := &MoveRelationItem{}
		if err := decode(7, &payload.Block, &payload.Relation, &payload.Item, &payload.TargetBlock, &payload.Target, &payload.After); err != nil {
			return err
		}
		o.MoveRelationItem = payload
	case OperationAttachFile:
		payload := &AttachFile{}
		if err := decode(3, &payload.Target, &payload.File); err != nil {
			return err
		}
		switch {
		case payload.Target.relationItem() && len(payload.Target.Path) == 0:
			*o = AttachRelationFileOperation(
				payload.Target.Block,
				payload.Target.Relation,
				payload.Target.Item,
				payload.Target.Field,
				payload.File,
			)
		case !payload.Target.relationItem() && len(payload.Target.Path) == 0:
			*o = AttachFileOperation(payload.Target.Block, payload.Target.Field, payload.File)
		default:
			o.AttachFile = payload
		}
	case OperationDetachFile:
		payload := &DetachFile{}
		if err := decode(2, &payload.Target); err != nil {
			return err
		}
		switch {
		case payload.Target.relationItem() && len(payload.Target.Path) == 0:
			*o = DetachRelationFileOperation(
				payload.Target.Block,
				payload.Target.Relation,
				payload.Target.Item,
				payload.Target.Field,
			)
		case !payload.Target.relationItem() && len(payload.Target.Path) == 0:
			*o = DetachFileOperation(payload.Target.Block, payload.Target.Field)
		default:
			o.DetachFile = payload
		}
	case OperationCreateTranslation:
		if err := decode(1); err != nil {
			return err
		}
		o.CreateTranslation = &CreateTranslation{}
	case OperationDeleteTranslation:
		if err := decode(1); err != nil {
			return err
		}
		o.DeleteTranslation = &DeleteTranslation{}
	default:
		return fmt.Errorf("unsupported operation kind %q", o.Kind)
	}
	return o.validateShape()
}

type ApplyRequest struct {
	Protocol                 string            `json:"v"`
	Profile                  Domain            `json:"p"`
	Document                 DocumentReference `json:"d"`
	Locale                   Locale            `json:"l"`
	ExpectedDocumentRevision Revision          `json:"edr"`
	ExpectedTargetRevision   *Revision         `json:"etr,omitempty"`
	Operations               []Operation       `json:"o"`
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
	stableTarget := func(target FieldTarget) error {
		if err := validateStableID("block ID", string(target.Block), 160); err != nil {
			return err
		}
		if target.relationItem() {
			if target.Relation == "" || target.Item == "" {
				return errors.New("relation-item field target requires both relation and item IDs")
			}
			if err := validateStableID("relation ID", string(target.Relation), 120); err != nil {
				return err
			}
			if err := validateStableID("relation item ID", string(target.Item), 160); err != nil {
				return err
			}
		}
		if err := validateStableID("field ID", string(target.Field), 120); err != nil {
			return err
		}
		for _, segment := range target.Path {
			if err := segment.validate(); err != nil {
				return err
			}
		}
		return nil
	}
	stableRelation := func(block BlockID, relation RelationID, item RelationItemID) error {
		if err := validateStableID("block ID", string(block), 160); err != nil {
			return err
		}
		if err := validateStableID("relation ID", string(relation), 120); err != nil {
			return err
		}
		return validateStableID("relation item ID", string(item), 160)
	}
	for index, operation := range operations {
		var err error
		switch operation.Kind {
		case OperationSetField:
			if err = stableTarget(operation.SetField.Target); err == nil {
				err = operation.SetField.Value.validate()
			}
		case OperationUnsetField:
			err = stableTarget(operation.UnsetField.Target)
		case OperationInsertBlock:
			if err = validateStableID("block ID", string(operation.InsertBlock.Block), 160); err == nil {
				err = validateStableID("block kind", string(operation.InsertBlock.Kind), 80)
			}
			if err == nil && operation.InsertBlock.Parent != "" {
				err = validateStableID("parent block ID", string(operation.InsertBlock.Parent), 160)
			}
			if err == nil && operation.InsertBlock.After != "" {
				err = validateStableID("after block ID", string(operation.InsertBlock.After), 160)
			}
		case OperationDeleteBlock:
			err = validateStableID("block ID", string(operation.DeleteBlock.Block), 160)
		case OperationMoveBlock:
			if err = validateStableID("block ID", string(operation.MoveBlock.Block), 160); err == nil && operation.MoveBlock.Parent != "" {
				err = validateStableID("parent block ID", string(operation.MoveBlock.Parent), 160)
			}
			if err == nil && operation.MoveBlock.After != "" {
				err = validateStableID("after block ID", string(operation.MoveBlock.After), 160)
			}
		case OperationReplaceBlockKind:
			if err = validateStableID("block ID", string(operation.ReplaceBlockKind.Block), 160); err == nil {
				err = validateStableID("block kind", string(operation.ReplaceBlockKind.Kind), 80)
			}
		case OperationInsertRelationItem:
			err = stableRelation(operation.InsertRelationItem.Block, operation.InsertRelationItem.Relation, operation.InsertRelationItem.Item)
			if err == nil {
				err = validateStableID("relation item kind", string(operation.InsertRelationItem.Kind), 80)
			}
			if err == nil && operation.InsertRelationItem.After != "" {
				err = validateStableID("after relation item ID", string(operation.InsertRelationItem.After), 160)
			}
		case OperationDeleteRelationItem:
			err = stableRelation(operation.DeleteRelationItem.Block, operation.DeleteRelationItem.Relation, operation.DeleteRelationItem.Item)
		case OperationMoveRelationItem:
			err = stableRelation(operation.MoveRelationItem.Block, operation.MoveRelationItem.Relation, operation.MoveRelationItem.Item)
			if err == nil {
				err = validateStableID("target block ID", string(operation.MoveRelationItem.TargetBlock), 160)
			}
			if err == nil {
				err = validateStableID("target relation ID", string(operation.MoveRelationItem.Target), 120)
			}
			if err == nil && operation.MoveRelationItem.After != "" {
				err = validateStableID("after relation item ID", string(operation.MoveRelationItem.After), 160)
			}
		case OperationAttachFile:
			if err = stableTarget(operation.AttachFile.Target); err == nil {
				err = validateFileReference(operation.AttachFile.File)
			}
		case OperationDetachFile:
			err = stableTarget(operation.DetachFile.Target)
		case OperationCreateTranslation, OperationDeleteTranslation:
			// Locale is carried once by the batch envelope.
		default:
			err = fmt.Errorf("unsupported operation kind %q", operation.Kind)
		}
		if err != nil {
			return fmt.Errorf("operation %d: %w", index, err)
		}
	}
	return nil
}
