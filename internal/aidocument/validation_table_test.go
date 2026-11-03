package aidocument

import (
	"reflect"
	"testing"
)

// The tables in this file pin the exact issue and error text of
// operationValidator, selectNodes, validateProjectedNodes and
// validateDocumentCatalogValues branch by branch, so decomposing those
// functions shows up as a deliberate expectation diff.

func validationTableDocument(locale Locale, exists bool) Document {
	document := testDocument(locale, exists)
	text := FieldSchema{Kind: ValueKindText, Ownership: FieldOwnershipShared}
	document.Catalog.Fields = append(document.Catalog.Fields, FieldRule{
		BlockKind: "document", Field: "settings", ValueKind: ValueKindObject, Ownership: FieldOwnershipShared,
		Schema: &FieldSchema{Kind: ValueKindObject, Ownership: FieldOwnershipShared, Fields: []NestedFieldRule{
			{Field: "theme", Schema: text},
			{Field: "tags", Schema: FieldSchema{Kind: ValueKindList, Ownership: FieldOwnershipShared, Item: &text, Identity: ListIdentityRule{Kind: ListIdentityValue}}},
			{Field: "slots", Schema: FieldSchema{Kind: ValueKindList, Ownership: FieldOwnershipShared, Item: &text, Identity: ListIdentityRule{Kind: ListIdentityFixed, Handles: []RelationItemID{"primary", "secondary"}}}},
			{Field: "lines", Schema: FieldSchema{Kind: ValueKindList, Ownership: FieldOwnershipShared, Item: &text, Identity: ListIdentityRule{Kind: ListIdentityPositional}}},
			{Field: "cover", Schema: FieldSchema{Ownership: FieldOwnershipShared, File: true}},
		}},
	})
	document.Catalog.RelationFields = append(document.Catalog.RelationFields, RelationFieldRule{
		BlockKind: "document", Relation: "credits", ItemKind: "credit", Field: "meta", ValueKind: ValueKindObject, Ownership: FieldOwnershipShared,
		Schema: &FieldSchema{Kind: ValueKindObject, Ownership: FieldOwnershipShared, Fields: []NestedFieldRule{{Field: "note", Schema: text}}},
	})
	return document
}

func relationFieldTarget(block BlockID, relation RelationID, item RelationItemID, field FieldID, path ...FieldPathSegment) FieldTarget {
	return FieldTarget{Block: block, Relation: relation, Item: item, Field: field, Path: path}
}

func setTargetOperation(target FieldTarget, value Value) Operation {
	return Operation{Kind: OperationSetField, SetField: &SetField{Target: target, Value: value}}
}

func attachTargetOperation(target FieldTarget, file FileReference) Operation {
	return Operation{Kind: OperationAttachFile, AttachFile: &AttachFile{Target: target, File: file}}
}

func detachTargetOperation(target FieldTarget) Operation {
	return Operation{Kind: OperationDetachFile, DetachFile: &DetachFile{Target: target}}
}

func tableIssue(operation int, code IssueCode, handle, message string) OperationIssue {
	return OperationIssue{Operation: operation, Code: code, Handle: handle, Message: message}
}

type validationLocale int

const (
	sourceLocale validationLocale = iota
	existingTarget
	missingTarget
)

type validationOperationCase struct {
	name       string
	locale     validationLocale
	operations []Operation
	want       []OperationIssue
}

func TestValidateOperationIssueTable(t *testing.T) {
	sourceOnly := "only the current source locale may alter the shared block graph, shared fields, or file relations"
	tests := fieldTargetIssueCases()
	tests = append(tests, fieldMutationIssueCases()...)
	tests = append(tests, blockMutationIssueCases(sourceOnly)...)
	tests = append(tests, relationItemIssueCases(sourceOnly)...)
	tests = append(tests, fileMutationIssueCases(sourceOnly)...)
	tests = append(tests, translationLifecycleIssueCases()...)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var request ApplyRequest
			var document Document
			switch tc.locale {
			case sourceLocale:
				document = validationTableDocument("ko", true)
				request = applyRequest("ko", "rev-7", tc.operations...)
			case existingTarget:
				document = validationTableDocument("en", true)
				request = applyRequest("en", "rev-7", tc.operations...)
			case missingTarget:
				document = validationTableDocument("en", false)
				request = missingTargetRequest("en", "rev-7", tc.operations...)
			}
			result := ValidateOperations(document, request)
			if result.Conflict != nil {
				t.Fatalf("conflict = %+v", result.Conflict)
			}
			if !reflect.DeepEqual(result.Issues, tc.want) {
				t.Fatalf("issues = %#v\nwant     %#v", result.Issues, tc.want)
			}
		})
	}
}

func fieldTargetIssueCases() []validationOperationCase {
	return []validationOperationCase{
		// Field target resolution, shared by set, unset, attach and detach.
		{"set invalid field ID", sourceLocale, []Operation{SetFieldOperation("root", "1", Text("x"))},
			[]OperationIssue{tableIssue(0, IssueInvalidOperation, "field:root/1", `field ID "1" contains a positional index`)}},
		{"set unknown block", sourceLocale, []Operation{SetFieldOperation("ghost", "title", Text("x"))},
			[]OperationIssue{tableIssue(0, IssueUnknownBlock, "block:ghost", "field target block does not exist")}},
		{"set unknown field", sourceLocale, []Operation{SetFieldOperation("root", "nope", Text("x"))},
			[]OperationIssue{tableIssue(0, IssueUnknownField, "field:root/nope", "field is not defined for the target block kind")}},
		{"set nested path on scalar field", sourceLocale, []Operation{SetNestedFieldOperation("root", "title", []FieldPathSegment{ObjectPath("x")}, Text("x"))},
			[]OperationIssue{tableIssue(0, IssueUnknownField, "field:root/title/field:x", "field does not define nested catalog shape")}},
		{"set unknown relation", sourceLocale, []Operation{SetRelationFieldOperation("root", "nope", "credit-primary", "role", Text("x"))},
			[]OperationIssue{tableIssue(0, IssueUnknownRelation, "relation:root/nope", "relation does not exist")}},
		{"set unknown relation item", sourceLocale, []Operation{SetRelationFieldOperation("root", "credits", "ghost", "role", Text("x"))},
			[]OperationIssue{tableIssue(0, IssueUnknownRelationItem, "relation-item:root/credits/ghost", "relation item does not exist")}},
		{"set unknown relation field", sourceLocale, []Operation{SetRelationFieldOperation("root", "credits", "credit-primary", "nope", Text("x"))},
			[]OperationIssue{tableIssue(0, IssueUnknownField, "field:root/credits/credit-primary/nope", "field is not defined for the relation item kind")}},
		{"set relation nested path on scalar field", sourceLocale, []Operation{setTargetOperation(relationFieldTarget("root", "credits", "credit-primary", "role", ObjectPath("x")), Text("x"))},
			[]OperationIssue{tableIssue(0, IssueUnknownField, "field:root/credits/credit-primary/role/field:x", "field does not define nested catalog shape")}},
		{"set relation nested field", sourceLocale, []Operation{setTargetOperation(relationFieldTarget("root", "credits", "credit-primary", "meta", ObjectPath("note")), Text("x"))},
			nil},
		{"set relation-item target missing item", sourceLocale, []Operation{setTargetOperation(relationFieldTarget("root", "credits", "", "role"), Text("x"))},
			[]OperationIssue{tableIssue(0, IssueInvalidOperation, "field:root/credits//role", "relation-item field target requires both relation and item IDs")}},
		{"set invalid path segment", sourceLocale, []Operation{SetNestedFieldOperation("root", "settings", []FieldPathSegment{{}}, Text("x"))},
			[]OperationIssue{tableIssue(0, IssueInvalidOperation, "field:root/settings/item:", "field path segment must contain exactly one field or list item handle")}},
		{"set path field below scalar", sourceLocale, []Operation{SetNestedFieldOperation("root", "settings", []FieldPathSegment{ObjectPath("theme"), ObjectPath("x")}, Text("x"))},
			[]OperationIssue{tableIssue(0, IssueUnknownField, "field:root/settings/field:theme/field:x", `path field "x" does not target an object`)}},
		{"set path field outside catalog", sourceLocale, []Operation{SetNestedFieldOperation("root", "settings", []FieldPathSegment{ObjectPath("nope")}, Text("x"))},
			[]OperationIssue{tableIssue(0, IssueUnknownField, "field:root/settings/field:nope", `path field "nope" is not in the catalog`)}},
		{"set path item below object", sourceLocale, []Operation{SetNestedFieldOperation("root", "settings", []FieldPathSegment{ListPath("a")}, Text("x"))},
			[]OperationIssue{tableIssue(0, IssueUnknownField, "field:root/settings/item:a", `path item "a" does not target a list`)}},
		{"set path item in positional list", sourceLocale, []Operation{SetNestedFieldOperation("root", "settings", []FieldPathSegment{ObjectPath("lines"), ListPath("a")}, Text("x"))},
			[]OperationIssue{tableIssue(0, IssueUnknownField, "field:root/settings/field:lines/item:a", "positional lists can only be replaced as a whole typed value")}},
		{"set unknown fixed list item", sourceLocale, []Operation{SetNestedFieldOperation("root", "settings", []FieldPathSegment{ObjectPath("slots"), ListPath("tertiary")}, Text("x"))},
			[]OperationIssue{tableIssue(0, IssueUnknownField, "field:root/settings/field:slots/item:tertiary", `fixed list item "tertiary" is not in the catalog`)}},
		{"set fixed and value list items", sourceLocale, []Operation{
			SetNestedFieldOperation("root", "settings", []FieldPathSegment{ObjectPath("slots"), ListPath("secondary")}, Text("x")),
			SetNestedFieldOperation("root", "settings", []FieldPathSegment{ObjectPath("tags"), ListPath("x")}, Text("x")),
		}, nil},
	}
}

func fieldMutationIssueCases() []validationOperationCase {
	return []validationOperationCase{
		// Set field.
		{"set file field", sourceLocale, []Operation{SetFieldOperation("root", "hero", Text("x"))},
			[]OperationIssue{tableIssue(0, IssueUnknownField, "field:root/hero", "file fields require a typed file operation")}},
		{"set nested file field", sourceLocale, []Operation{SetNestedFieldOperation("root", "settings", []FieldPathSegment{ObjectPath("cover")}, Text("x"))},
			[]OperationIssue{tableIssue(0, IssueUnknownField, "field:root/settings/field:cover", "file fields require a typed file operation")}},
		{"set value kind mismatch", sourceLocale, []Operation{SetFieldOperation("root", "title", Boolean(true))},
			[]OperationIssue{tableIssue(0, IssueValueKindMismatch, "field:root/title", "field value kind does not match the domain catalog")}},
		{"set invalid value", sourceLocale, []Operation{SetFieldOperation("paragraph-a", "rich", RichText(InlineItem{Kind: "bogus"}))},
			[]OperationIssue{tableIssue(0, IssueValueKindMismatch, "field:paragraph-a/rich", `unsupported inline item kind "bogus"`)}},
		{"set value outside schema", sourceLocale, []Operation{SetFieldOperation("root", "settings", Object(ObjectValue("nope", Text("x"))))},
			[]OperationIssue{tableIssue(0, IssueValueKindMismatch, "field:root/settings", `object field "nope" is not in the catalog`)}},
		{"set value inside schema", sourceLocale, []Operation{SetFieldOperation("root", "settings", Object(ObjectValue("theme", Text("dark"))))},
			nil},
		{"target set shared field", existingTarget, []Operation{SetFieldOperation("root", "slug", Text("x"))},
			[]OperationIssue{tableIssue(0, IssueTargetFieldForbidden, "field:root/slug", "non-source locales may modify only locale-owned translatable values")}},
		{"target set untranslatable locale field", existingTarget, []Operation{SetFieldOperation("paragraph-a", "internal_note", Text("x"))},
			[]OperationIssue{tableIssue(0, IssueTargetFieldForbidden, "field:paragraph-a/internal_note", "non-source locales may modify only locale-owned translatable values")}},
		{"target set translatable fields", existingTarget, []Operation{
			SetFieldOperation("root", "title", Text("x")),
			SetRelationFieldOperation("root", "credits", "credit-primary", "bio", Text("x")),
		}, nil},

		// Unset field.
		{"unset unknown block", sourceLocale, []Operation{UnsetFieldOperation("ghost", "title")},
			[]OperationIssue{tableIssue(0, IssueUnknownBlock, "block:ghost", "field target block does not exist")}},
		{"target unset field", existingTarget, []Operation{UnsetFieldOperation("root", "title")},
			[]OperationIssue{tableIssue(0, IssueTargetFieldForbidden, "field:root/title", "non-source locales cannot unset fields; set an explicit empty value or delete the translation")}},
		{"unset file field", sourceLocale, []Operation{UnsetFieldOperation("root", "hero")},
			[]OperationIssue{tableIssue(0, IssueTargetFieldForbidden, "field:root/hero", "file fields require a typed file operation")}},
		{"unset field", sourceLocale, []Operation{UnsetFieldOperation("root", "title")}, nil},
	}
}

func blockMutationIssueCases(sourceOnly string) []validationOperationCase {
	return []validationOperationCase{
		// Insert block.
		{"target insert block", existingTarget, []Operation{InsertBlockOperation("new", "paragraph", "root", "")},
			[]OperationIssue{tableIssue(0, IssueSourceAuthorityRequired, "block:new", sourceOnly)}},
		{"insert invalid block ID", sourceLocale, []Operation{InsertBlockOperation("1", "paragraph", "root", "")},
			[]OperationIssue{tableIssue(0, IssueInvalidOperation, "block:1", `block ID "1" contains a positional index`)}},
		{"insert duplicate block", sourceLocale, []Operation{InsertBlockOperation("paragraph-a", "paragraph", "root", "")},
			[]OperationIssue{tableIssue(0, IssueDuplicateBlock, "block:paragraph-a", "block ID already exists")}},
		{"insert unknown kind", sourceLocale, []Operation{InsertBlockOperation("new", "table", "root", "")},
			[]OperationIssue{tableIssue(0, IssueUnknownBlockKind, "block:new", "block kind is not in the domain catalog")}},
		{"insert under missing parent", sourceLocale, []Operation{InsertBlockOperation("new", "paragraph", "ghost", "")},
			[]OperationIssue{tableIssue(0, IssueInvalidBlockRelation, "block:ghost", "parent block does not exist")}},
		{"insert after missing sibling", sourceLocale, []Operation{InsertBlockOperation("new", "paragraph", "root", "ghost")},
			[]OperationIssue{tableIssue(0, IssueInvalidBlockRelation, "block:ghost", "after block does not exist")}},
		{"insert after sibling of other parent", sourceLocale, []Operation{InsertBlockOperation("new", "paragraph", "paragraph-a", "paragraph-b")},
			[]OperationIssue{tableIssue(0, IssueInvalidBlockRelation, "block:paragraph-b", "after block is not a child of the requested parent")}},
		{"insert block", sourceLocale, []Operation{InsertBlockOperation("new", "paragraph", "root", "paragraph-a")}, nil},

		// Delete block.
		{"target delete block", existingTarget, []Operation{DeleteBlockOperation("paragraph-a")},
			[]OperationIssue{tableIssue(0, IssueSourceAuthorityRequired, "block:paragraph-a", sourceOnly)}},
		{"delete unknown block", sourceLocale, []Operation{DeleteBlockOperation("ghost")},
			[]OperationIssue{tableIssue(0, IssueUnknownBlock, "block:ghost", "block does not exist")}},
		{"delete block then target its child", sourceLocale, []Operation{DeleteBlockOperation("root"), SetFieldOperation("paragraph-a", "content", Text("x"))},
			[]OperationIssue{tableIssue(1, IssueUnknownBlock, "block:paragraph-a", "field target block does not exist")}},

		// Move block.
		{"target move block", existingTarget, []Operation{MoveBlockOperation("paragraph-a", "root", "")},
			[]OperationIssue{tableIssue(0, IssueSourceAuthorityRequired, "block:paragraph-a", sourceOnly)}},
		{"move unknown block", sourceLocale, []Operation{MoveBlockOperation("ghost", "root", "")},
			[]OperationIssue{tableIssue(0, IssueUnknownBlock, "block:ghost", "block does not exist")}},
		{"move block under itself", sourceLocale, []Operation{MoveBlockOperation("paragraph-a", "paragraph-a", "")},
			[]OperationIssue{tableIssue(0, IssueBlockCycle, "block:paragraph-a", "move would create a block cycle")}},
		{"move block under its descendant", sourceLocale, []Operation{MoveBlockOperation("root", "paragraph-a", "")},
			[]OperationIssue{tableIssue(0, IssueBlockCycle, "block:root", "move would create a block cycle")}},
		{"move block after itself", sourceLocale, []Operation{MoveBlockOperation("paragraph-a", "root", "paragraph-a")},
			[]OperationIssue{tableIssue(0, IssueInvalidBlockRelation, "block:paragraph-a", "block cannot be placed after itself")}},
		{"move block", sourceLocale, []Operation{MoveBlockOperation("paragraph-a", "root", "paragraph-b")}, nil},

		// Replace block kind.
		{"target replace block kind", existingTarget, []Operation{ReplaceBlockKindOperation("paragraph-a", "heading")},
			[]OperationIssue{tableIssue(0, IssueSourceAuthorityRequired, "block:paragraph-a", sourceOnly)}},
		{"replace kind of unknown block", sourceLocale, []Operation{ReplaceBlockKindOperation("ghost", "heading")},
			[]OperationIssue{tableIssue(0, IssueUnknownBlock, "block:ghost", "block does not exist")}},
		{"replace with unknown kind", sourceLocale, []Operation{ReplaceBlockKindOperation("paragraph-a", "table")},
			[]OperationIssue{tableIssue(0, IssueUnknownBlockKind, "block:paragraph-a", "block kind is not in the domain catalog")}},
		{"replace block kind", sourceLocale, []Operation{ReplaceBlockKindOperation("paragraph-a", "heading")}, nil},
	}
}

func relationItemIssueCases(sourceOnly string) []validationOperationCase {
	return []validationOperationCase{
		// Insert relation item.
		{"target insert relation item", existingTarget, []Operation{InsertRelationItemOperation("root", "credits", "credit-new", "credit", "")},
			[]OperationIssue{tableIssue(0, IssueSourceAuthorityRequired, "relation-item:root/credits/credit-new", sourceOnly)}},
		{"insert relation item with invalid IDs", sourceLocale, []Operation{InsertRelationItemOperation("root", "credits", "1", "credit", "")},
			[]OperationIssue{tableIssue(0, IssueInvalidOperation, "relation-item:root/credits/1", `relation item ID "1" contains a positional index`)}},
		{"insert relation item with invalid kind", sourceLocale, []Operation{InsertRelationItemOperation("root", "credits", "credit-new", "1", "")},
			[]OperationIssue{tableIssue(0, IssueInvalidOperation, "relation-item:root/credits/credit-new", `relation item kind "1" contains a positional index`)}},
		{"insert relation item after invalid ID", sourceLocale, []Operation{InsertRelationItemOperation("root", "credits", "credit-new", "credit", "1")},
			[]OperationIssue{tableIssue(0, IssueInvalidOperation, "relation-item:root/credits/credit-new", `after relation item ID "1" contains a positional index`)}},
		{"insert relation item on unknown block", sourceLocale, []Operation{InsertRelationItemOperation("ghost", "credits", "credit-new", "credit", "")},
			[]OperationIssue{tableIssue(0, IssueUnknownBlock, "block:ghost", "relation owner block does not exist")}},
		{"insert relation item into undefined relation", sourceLocale, []Operation{InsertRelationItemOperation("root", "links", "link-new", "link", "")},
			[]OperationIssue{tableIssue(0, IssueUnknownRelation, "relation:root/links", "relation is not defined for the block kind")}},
		{"insert relation item of disallowed kind", sourceLocale, []Operation{InsertRelationItemOperation("root", "credits", "credit-new", "link", "")},
			[]OperationIssue{tableIssue(0, IssueInvalidRelationItemMove, "relation-item:root/credits/credit-new", "relation item kind is not allowed")}},
		{"insert duplicate relation item", sourceLocale, []Operation{InsertRelationItemOperation("root", "credits", "credit-primary", "credit", "")},
			[]OperationIssue{tableIssue(0, IssueDuplicateRelationItem, "relation-item:root/credits/credit-primary", "relation item ID already exists")}},
		{"insert relation item after itself", sourceLocale, []Operation{InsertRelationItemOperation("root", "credits", "credit-new", "credit", "credit-new")},
			[]OperationIssue{tableIssue(0, IssueInvalidRelationItemMove, "relation-item:root/credits/credit-new", "relation item cannot be placed after itself")}},
		{"insert relation item after missing item", sourceLocale, []Operation{InsertRelationItemOperation("root", "credits", "credit-new", "credit", "ghost")},
			[]OperationIssue{tableIssue(0, IssueInvalidRelationItemMove, "relation-item:root/credits/ghost", "after relation item does not exist in the target relation")}},
		{"insert relation item into empty relation", sourceLocale, []Operation{InsertRelationItemOperation("paragraph-a", "links", "link-new", "link", "")}, nil},

		// Delete relation item.
		{"target delete relation item", existingTarget, []Operation{DeleteRelationItemOperation("root", "credits", "credit-primary")},
			[]OperationIssue{tableIssue(0, IssueSourceAuthorityRequired, "relation-item:root/credits/credit-primary", sourceOnly)}},
		{"delete relation item with invalid IDs", sourceLocale, []Operation{DeleteRelationItemOperation("root", "1", "credit-primary")},
			[]OperationIssue{tableIssue(0, IssueInvalidOperation, "relation-item:root/1/credit-primary", `relation ID "1" contains a positional index`)}},
		{"delete relation item on unknown block", sourceLocale, []Operation{DeleteRelationItemOperation("ghost", "credits", "credit-primary")},
			[]OperationIssue{tableIssue(0, IssueUnknownBlock, "block:ghost", "relation owner block does not exist")}},
		{"delete relation item from missing relation", sourceLocale, []Operation{DeleteRelationItemOperation("root", "links", "credit-primary")},
			[]OperationIssue{tableIssue(0, IssueUnknownRelation, "relation:root/links", "relation does not exist")}},
		{"delete missing relation item", sourceLocale, []Operation{DeleteRelationItemOperation("root", "credits", "ghost")},
			[]OperationIssue{tableIssue(0, IssueUnknownRelationItem, "relation-item:root/credits/ghost", "relation item does not exist")}},
		{"delete relation item", sourceLocale, []Operation{DeleteRelationItemOperation("root", "credits", "credit-primary")}, nil},

		// Move relation item.
		{"target move relation item", existingTarget, []Operation{MoveRelationItemOperation("root", "credits", "credit-primary", "root", "credits", "")},
			[]OperationIssue{tableIssue(0, IssueSourceAuthorityRequired, "relation-item:root/credits/credit-primary", sourceOnly)}},
		{"move relation item with invalid source IDs", sourceLocale, []Operation{MoveRelationItemOperation("1", "credits", "credit-primary", "root", "credits", "")},
			[]OperationIssue{tableIssue(0, IssueInvalidOperation, "relation-item:1/credits/credit-primary", `block ID "1" contains a positional index`)}},
		{"move relation item with invalid target IDs", sourceLocale, []Operation{MoveRelationItemOperation("root", "credits", "credit-primary", "root", "1", "")},
			[]OperationIssue{tableIssue(0, IssueInvalidOperation, "relation-item:root/1/credit-primary", `relation ID "1" contains a positional index`)}},
		{"move relation item after invalid ID", sourceLocale, []Operation{MoveRelationItemOperation("root", "credits", "credit-primary", "root", "credits", "1")},
			[]OperationIssue{tableIssue(0, IssueInvalidOperation, "relation-item:root/credits/credit-primary", `after relation item ID "1" contains a positional index`)}},
		{"move missing relation item", sourceLocale, []Operation{MoveRelationItemOperation("root", "credits", "ghost", "root", "credits", "")},
			[]OperationIssue{tableIssue(0, IssueUnknownRelationItem, "relation-item:root/credits/ghost", "relation item does not exist")}},
		{"move relation item to unknown block", sourceLocale, []Operation{MoveRelationItemOperation("root", "credits", "credit-primary", "ghost", "credits", "")},
			[]OperationIssue{tableIssue(0, IssueUnknownBlock, "block:ghost", "target relation owner block does not exist")}},
		{"move relation item to undefined relation", sourceLocale, []Operation{MoveRelationItemOperation("root", "credits", "credit-primary", "paragraph-a", "credits", "")},
			[]OperationIssue{tableIssue(0, IssueUnknownRelation, "relation:paragraph-a/credits", "target relation is not defined for the block kind")}},
		{"move relation item to relation of other kind", sourceLocale, []Operation{MoveRelationItemOperation("root", "credits", "credit-primary", "paragraph-a", "links", "")},
			[]OperationIssue{tableIssue(0, IssueInvalidRelationItemMove, "relation-item:root/credits/credit-primary", "target relation does not allow the item kind")}},
		{"move relation item after itself", sourceLocale, []Operation{MoveRelationItemOperation("root", "credits", "credit-primary", "root", "credits", "credit-primary")},
			[]OperationIssue{tableIssue(0, IssueInvalidRelationItemMove, "relation-item:root/credits/credit-primary", "relation item cannot be placed after itself")}},
		{"move relation item", sourceLocale, []Operation{MoveRelationItemOperation("root", "credits", "credit-primary", "root", "credits", "credit-secondary")}, nil},
	}
}

func fileMutationIssueCases(sourceOnly string) []validationOperationCase {
	return []validationOperationCase{
		// Attach file.
		{"target attach file", existingTarget, []Operation{AttachFileOperation("root", "hero", "file-new")},
			[]OperationIssue{tableIssue(0, IssueSourceAuthorityRequired, "field:root/hero", sourceOnly)}},
		{"attach file to unknown field", sourceLocale, []Operation{AttachFileOperation("root", "nope", "file-new")},
			[]OperationIssue{tableIssue(0, IssueUnknownField, "field:root/nope", "field is not defined for the target block kind")}},
		{"attach file to value field", sourceLocale, []Operation{AttachFileOperation("root", "slug", "file-new")},
			[]OperationIssue{tableIssue(0, IssueUnknownField, "field:root/slug", "field is not a file slot")}},
		{"attach inline data file", sourceLocale, []Operation{AttachFileOperation("root", "hero", "data:image/png;base64,AA")},
			[]OperationIssue{tableIssue(0, IssueInvalidFileReference, "field:root/hero", "inline data file payloads are forbidden")}},
		{"attach files", sourceLocale, []Operation{
			AttachFileOperation("root", "hero", "file-new"),
			attachTargetOperation(FieldTarget{Block: "root", Field: "settings", Path: []FieldPathSegment{ObjectPath("cover")}}, "file-cover"),
			AttachRelationFileOperation("root", "credits", "credit-secondary", "avatar", "file-avatar-2"),
		}, nil},

		// Detach file.
		{"target detach file", existingTarget, []Operation{DetachFileOperation("root", "hero")},
			[]OperationIssue{tableIssue(0, IssueSourceAuthorityRequired, "field:root/hero", sourceOnly)}},
		{"detach file from unknown block", sourceLocale, []Operation{DetachFileOperation("ghost", "hero")},
			[]OperationIssue{tableIssue(0, IssueUnknownBlock, "block:ghost", "field target block does not exist")}},
		{"detach file from value field", sourceLocale, []Operation{DetachFileOperation("root", "slug")},
			[]OperationIssue{tableIssue(0, IssueUnknownField, "field:root/slug", "field is not a file slot")}},
		{"detach files", sourceLocale, []Operation{
			DetachFileOperation("root", "hero"),
			detachTargetOperation(relationFieldTarget("root", "credits", "credit-primary", "avatar")),
		}, nil},
	}
}

func translationLifecycleIssueCases() []validationOperationCase {
	return []validationOperationCase{
		// Translation lifecycle.
		{"source create translation", sourceLocale, []Operation{CreateTranslationOperation()},
			[]OperationIssue{tableIssue(0, IssueTranslationIsSource, "translation", "the current source locale cannot be created as a translation")}},
		{"create translation in batch", missingTarget, []Operation{CreateTranslationOperation(), SetFieldOperation("root", "title", Text("x"))},
			[]OperationIssue{tableIssue(0, IssueLocaleOperationNotExclusive, "translation", "translation create must be an exclusive aggregate operation")}},
		{"create existing translation", existingTarget, []Operation{CreateTranslationOperation()},
			[]OperationIssue{tableIssue(0, IssueTranslationAlreadyExists, "translation", "translation locale already exists")}},
		{"create translation", missingTarget, []Operation{CreateTranslationOperation()}, nil},
		{"source delete translation", sourceLocale, []Operation{DeleteTranslationOperation()},
			[]OperationIssue{tableIssue(0, IssueTranslationIsSource, "translation", "the current source locale cannot be deleted as a translation")}},
		{"delete translation in batch", existingTarget, []Operation{DeleteTranslationOperation(), SetFieldOperation("root", "title", Text("x"))},
			[]OperationIssue{tableIssue(0, IssueLocaleOperationNotExclusive, "translation", "translation delete must be an exclusive aggregate operation")}},
		{"delete missing translation", missingTarget, []Operation{DeleteTranslationOperation()},
			[]OperationIssue{tableIssue(0, IssueTranslationMissing, "translation", "translation locale does not exist")}},
		{"delete translation", existingTarget, []Operation{DeleteTranslationOperation()}, nil},
	}
}

// The request envelope already rejects malformed payloads, so the validator's
// own shape check is only reachable by calling it directly.
func TestValidateOperationRejectsMalformedShapeDirectly(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation Operation
		want      OperationIssue
	}{
		{"no payload", Operation{Kind: OperationSetField},
			tableIssue(3, IssueInvalidOperation, "", "operation must contain exactly one typed payload")},
		{"payload of another kind", Operation{Kind: OperationUnsetField, SetField: &SetField{}},
			tableIssue(3, IssueInvalidOperation, "", `operation kind "fu" does not match its payload`)},
		{"unknown kind", Operation{Kind: "zz", SetField: &SetField{}},
			tableIssue(3, IssueInvalidOperation, "", `operation kind "zz" does not match its payload`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := (&operationValidator{index: 3, role: LocaleRoleSource, localeExists: true, batchSize: 1}).validate(tc.operation)
			if got == nil || *got != tc.want {
				t.Fatalf("issue = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestSelectNodesTable(t *testing.T) {
	document := validationTableDocument("ko", true)
	document.Nodes = canonicalNodes(document.Nodes)
	document.Nodes[1].Relations = []Relation{{ID: "links", Items: []RelationItem{{ID: "link-a", Kind: "link", Localized: []FieldValue{{ID: "label", Value: Text("Docs")}}}}}}
	outline := []Node{
		{ID: "root", Kind: "document", Relations: []Relation{{ID: "credits", Items: []RelationItem{
			{ID: "credit-primary", Kind: "credit"},
			{ID: "credit-secondary", Kind: "credit", Order: 1},
		}}}},
		{ID: "paragraph-a", Kind: "paragraph", Parent: "root", Relations: []Relation{{ID: "links", Items: []RelationItem{{ID: "link-a", Kind: "link"}}}}},
		{ID: "paragraph-b", Kind: "paragraph", Parent: "root", Order: 1},
	}
	tests := []struct {
		name    string
		request ReadRequest
		want    []Node
		wantErr string
	}{
		{name: "outline", request: ReadRequest{Mode: ReadOutline}, want: outline},
		{name: "blocks in document order", request: ReadRequest{Mode: ReadBlocks, Blocks: []BlockID{"paragraph-b", "paragraph-a"}},
			want: []Node{document.Nodes[1], document.Nodes[2]}},
		{name: "blocks with invalid ID", request: ReadRequest{Mode: ReadBlocks, Blocks: []BlockID{"1"}},
			wantErr: `block ID "1" contains a positional index`},
		{name: "blocks with missing ID", request: ReadRequest{Mode: ReadBlocks, Blocks: []BlockID{"root", "ghost"}},
			wantErr: "one or more selected blocks do not exist; reread the outline for current block handles"},
		{name: "fields across block, file and relation values", request: ReadRequest{Mode: ReadFields, Fields: []FieldSelection{
			{Block: "root", Field: "title"},
			{Block: "root", Field: "slug"},
			{Block: "root", Field: "hero"},
			{Block: "root", Relation: "credits", Item: "credit-primary", Field: "role"},
			{Block: "root", Relation: "credits", Item: "credit-primary", Field: "bio"},
			{Block: "root", Relation: "credits", Item: "credit-primary", Field: "avatar"},
			{Block: "paragraph-b", Field: "image"},
		}}, want: []Node{
			{
				ID: "root", Kind: "document",
				Shared:    []FieldValue{{ID: "slug", Value: Text("hello")}},
				Localized: []FieldValue{{ID: "title", Value: Text("Hello")}},
				Files:     []FileBinding{{Field: "hero", File: "file-handle-hero"}},
				Relations: []Relation{{ID: "credits", Items: []RelationItem{{
					ID: "credit-primary", Kind: "credit",
					Shared:    []FieldValue{{ID: "role", Value: Text("author")}},
					Localized: []FieldValue{{ID: "bio", Value: Text("")}},
					Files:     []FileBinding{{Field: "avatar", File: "file-avatar"}},
				}}}},
			},
			{ID: "paragraph-b", Kind: "paragraph", Parent: "root", Order: 1, Files: []FileBinding{{Field: "image", File: "file-handle-b"}}},
		}},
		{name: "fields skip relations without selected items", request: ReadRequest{Mode: ReadFields, Fields: []FieldSelection{{Block: "paragraph-a", Field: "content"}}},
			want: []Node{{ID: "paragraph-a", Kind: "paragraph", Parent: "root", Localized: []FieldValue{{ID: "content", Value: Text("First")}}}}},
		{name: "fields with known but absent value", request: ReadRequest{Mode: ReadFields, Fields: []FieldSelection{{Block: "paragraph-b", Field: "rich"}}},
			want: []Node{{ID: "paragraph-b", Kind: "paragraph", Parent: "root", Order: 1}}},
		{name: "fields with invalid target", request: ReadRequest{Mode: ReadFields, Fields: []FieldSelection{{Block: "root", Field: "1"}}},
			wantErr: `field ID "1" contains a positional index`},
		{name: "fields on missing block", request: ReadRequest{Mode: ReadFields, Fields: []FieldSelection{{Block: "ghost", Field: "title"}}},
			wantErr: `selected block "ghost" does not exist`},
		{name: "fields outside catalog", request: ReadRequest{Mode: ReadFields, Fields: []FieldSelection{{Block: "root", Field: "nope"}}},
			wantErr: `selected field "nope" is not defined for block "root"`},
		{name: "fields on missing relation item", request: ReadRequest{Mode: ReadFields, Fields: []FieldSelection{{Block: "root", Relation: "credits", Item: "ghost", Field: "role"}}},
			wantErr: `selected relation item "ghost" does not exist`},
		{name: "fields outside relation catalog", request: ReadRequest{Mode: ReadFields, Fields: []FieldSelection{{Block: "root", Relation: "credits", Item: "credit-primary", Field: "nope"}}},
			wantErr: `selected field "nope" is not defined for relation item "credit-primary"`},
		{name: "unsupported mode", request: ReadRequest{Mode: "everything"}, wantErr: "unsupported read mode"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectNodes(document, tc.request)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectNodes: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("nodes = %#v\nwant    %#v", got, tc.want)
			}
		})
	}
}

func TestValidateProjectedNodesTable(t *testing.T) {
	item := func(edit func(*RelationItem)) []Node {
		value := RelationItem{ID: "item", Kind: "credit"}
		edit(&value)
		return []Node{{ID: "root", Kind: "document", Relations: []Relation{{ID: "credits", Items: []RelationItem{value}}}}}
	}
	tests := []struct {
		name  string
		nodes []Node
		want  string
	}{
		{"valid partial projection with outside parent", []Node{
			{ID: "root", Kind: "document", Files: []FileBinding{{Field: "hero", Path: []FieldPathSegment{ObjectPath("cover")}, File: "file"}}},
			{ID: "child", Kind: "paragraph", Parent: "outside"},
		}, ""},
		{"invalid block ID", []Node{{ID: "1"}}, `block ID "1" contains a positional index`},
		{"duplicate block ID", []Node{{ID: "a"}, {ID: "a"}}, `duplicate block ID "a"`},
		{"invalid parent ID", []Node{{ID: "a", Parent: "1"}}, `parent block ID "1" contains a positional index`},
		{"negative order", []Node{{ID: "a", Order: -1}}, `block "a" has negative order`},
		{"invalid shared field", []Node{{ID: "a", Shared: []FieldValue{{ID: "1", Value: Text("x")}}}}, `block "a" shared fields: field ID "1" contains a positional index`},
		{"invalid localized field", []Node{{ID: "a", Localized: []FieldValue{{ID: "x", Value: Text("x")}, {ID: "x", Value: Text("y")}}}}, `block "a" localized fields: duplicate field "x"`},
		{"duplicate file target", []Node{{ID: "a", Files: []FileBinding{{Field: "f", File: "x"}, {Field: "f", File: "y"}}}}, `block "a" has duplicate file target "f"`},
		{"invalid file field ID", []Node{{ID: "a", Files: []FileBinding{{Field: "1", File: "x"}}}}, `file field ID "1" contains a positional index`},
		{"invalid file path", []Node{{ID: "a", Files: []FileBinding{{Field: "f", Path: []FieldPathSegment{{}}, File: "x"}}}}, "field path segment must contain exactly one field or list item handle"},
		{"invalid file reference", []Node{{ID: "a", Files: []FileBinding{{Field: "f", File: "data:x"}}}}, "inline data file payloads are forbidden"},
		{"invalid relation ID", []Node{{ID: "a", Relations: []Relation{{ID: "1"}}}}, `relation ID "1" contains a positional index`},
		{"duplicate relation", []Node{{ID: "a", Relations: []Relation{{ID: "r"}, {ID: "r"}}}}, `block "a" has duplicate relation "r"`},
		{"invalid relation item ID", item(func(i *RelationItem) { i.ID = "1" }), `relation item ID "1" contains a positional index`},
		{"invalid relation item kind", item(func(i *RelationItem) { i.Kind = "1" }), `relation item kind "1" contains a positional index`},
		{"duplicate relation item across relations", []Node{{ID: "root", Relations: []Relation{
			{ID: "r1", Items: []RelationItem{{ID: "item", Kind: "k"}}},
			{ID: "r2", Items: []RelationItem{{ID: "item", Kind: "k"}}},
		}}}, `block "root" has duplicate relation item ID "item"`},
		{"negative relation item order", item(func(i *RelationItem) { i.Order = -1 }), `relation item "item" has negative order`},
		{"invalid relation item shared field", item(func(i *RelationItem) { i.Shared = []FieldValue{{ID: "1", Value: Text("x")}} }), `relation item "item" shared fields: field ID "1" contains a positional index`},
		{"invalid relation item localized field", item(func(i *RelationItem) { i.Localized = []FieldValue{{ID: "x", Value: Value{Kind: "bogus"}}} }), `relation item "item" localized fields: field "x": unsupported value kind "bogus"`},
		{"duplicate relation item file target", item(func(i *RelationItem) {
			i.Files = []FileBinding{{Field: "f", Path: []FieldPathSegment{ListPath("a")}, File: "x"}, {Field: "f", Path: []FieldPathSegment{ListPath("a")}, File: "y"}}
		}), `relation item "item" has duplicate file target "f/item:a"`},
		{"invalid relation item file field ID", item(func(i *RelationItem) { i.Files = []FileBinding{{Field: "1", File: "x"}} }), `file field ID "1" contains a positional index`},
		{"invalid relation item file path", item(func(i *RelationItem) {
			i.Files = []FileBinding{{Field: "f", Path: []FieldPathSegment{{Field: "a", Item: "b"}}, File: "x"}}
		}), "field path segment must contain exactly one field or list item handle"},
		{"invalid relation item file reference", item(func(i *RelationItem) { i.Files = []FileBinding{{Field: "f", File: ""}} }), "file reference must contain 1 to 256 characters"},
		{"parent cycle", []Node{{ID: "a", Parent: "b"}, {ID: "b", Parent: "a"}}, `block "a" is part of a parent cycle`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProjectedNodes(tc.nodes)
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != tc.want {
				t.Fatalf("err = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateDocumentCatalogValuesTable(t *testing.T) {
	catalog := validationTableDocument("ko", true).Catalog
	catalog.Fields = append(catalog.Fields, FieldRule{
		BlockKind: "paragraph", Field: "caption", ValueKind: ValueKindObject, Ownership: FieldOwnershipLocale, Translatable: true,
		Schema: &FieldSchema{Kind: ValueKindObject, Ownership: FieldOwnershipLocale, Translatable: true, Fields: []NestedFieldRule{
			{Field: "text", Schema: FieldSchema{Kind: ValueKindText, Ownership: FieldOwnershipLocale, Translatable: true}},
		}},
	})
	catalog.RelationFields = append(catalog.RelationFields, RelationFieldRule{
		BlockKind: "document", Relation: "credits", ItemKind: "credit", Field: "alias", ValueKind: ValueKindObject, Ownership: FieldOwnershipLocale, Translatable: true,
		Schema: &FieldSchema{Kind: ValueKindObject, Ownership: FieldOwnershipLocale, Translatable: true, Fields: []NestedFieldRule{
			{Field: "text", Schema: FieldSchema{Kind: ValueKindText, Ownership: FieldOwnershipLocale, Translatable: true}},
		}},
	})
	unknown := Object(ObjectValue("nope", Text("x")))
	root := func(edit func(*Node)) []Node {
		node := Node{ID: "root", Kind: "document"}
		edit(&node)
		return []Node{node}
	}
	credit := func(edit func(*RelationItem)) []Node {
		item := RelationItem{ID: "credit-a", Kind: "credit"}
		edit(&item)
		return []Node{{ID: "root", Kind: "document", Relations: []Relation{{ID: "credits", Items: []RelationItem{item}}}}}
	}
	tests := []struct {
		name  string
		nodes []Node
		want  string
	}{
		{"valid values", []Node{
			{
				ID: "root", Kind: "document",
				Shared:    []FieldValue{{ID: "settings", Value: Object(ObjectValue("theme", Text("dark")))}},
				Files:     []FileBinding{{Field: "hero", File: "x"}, {Field: "settings", Path: []FieldPathSegment{ObjectPath("cover")}, File: "y"}},
				Relations: []Relation{{ID: "credits", Items: []RelationItem{{ID: "credit-a", Kind: "credit", Localized: []FieldValue{{ID: "alias", Value: Object(ObjectValue("text", Text("x")))}}}}}},
			},
			{ID: "p", Kind: "paragraph", Parent: "root", Localized: []FieldValue{{ID: "caption", Value: Object(ObjectValue("text", Text("x")))}}},
		}, ""},
		{"unknown block kind", []Node{{ID: "root", Kind: "table"}}, `block "root" has unknown kind "table"`},
		{"shared field outside catalog", root(func(n *Node) { n.Shared = []FieldValue{{ID: "nope", Value: Text("x")}} }), `block "root" shared field "nope" does not match the catalog`},
		{"shared value in locale field", root(func(n *Node) { n.Shared = []FieldValue{{ID: "title", Value: Text("x")}} }), `block "root" shared field "title" does not match the catalog`},
		{"shared value in file field", root(func(n *Node) { n.Shared = []FieldValue{{ID: "hero", Value: Text("x")}} }), `block "root" shared field "hero" does not match the catalog`},
		{"shared value of wrong kind", root(func(n *Node) { n.Shared = []FieldValue{{ID: "slug", Value: Boolean(true)}} }), `block "root" shared field "slug" does not match the catalog`},
		{"shared value outside schema", root(func(n *Node) { n.Shared = []FieldValue{{ID: "settings", Value: unknown}} }), `block "root" shared field "settings": object field "nope" is not in the catalog`},
		{"localized field outside catalog", root(func(n *Node) { n.Localized = []FieldValue{{ID: "slug", Value: Text("x")}} }), `block "root" localized field "slug" does not match the catalog`},
		{"localized value outside schema", []Node{{ID: "p", Kind: "paragraph", Localized: []FieldValue{{ID: "caption", Value: unknown}}}}, `block "p" localized field "caption": object field "nope" is not in the catalog`},
		{"file field outside catalog", root(func(n *Node) { n.Files = []FileBinding{{Field: "nope", File: "x"}} }), `block "root" file field "nope" does not match the catalog`},
		{"file on value field", root(func(n *Node) { n.Files = []FileBinding{{Field: "slug", File: "x"}} }), `block "root" file target "slug" does not match the catalog`},
		{"file on nested value field", root(func(n *Node) {
			n.Files = []FileBinding{{Field: "settings", Path: []FieldPathSegment{ObjectPath("theme")}, File: "x"}}
		}), `block "root" file target "settings/field:theme" does not match the catalog`},
		{"relation outside catalog", root(func(n *Node) { n.Relations = []Relation{{ID: "links"}} }), `block "root" relation "links" does not match the catalog`},
		{"relation item of disallowed kind", credit(func(i *RelationItem) { i.Kind = "link" }), `relation item "credit-a" kind "link" does not match the catalog`},
		{"relation shared field outside catalog", credit(func(i *RelationItem) { i.Shared = []FieldValue{{ID: "bio", Value: Text("x")}} }), `relation item "credit-a" shared field "bio" does not match the catalog`},
		{"relation shared value outside schema", credit(func(i *RelationItem) { i.Shared = []FieldValue{{ID: "meta", Value: unknown}} }), `relation item "credit-a" shared field "meta": object field "nope" is not in the catalog`},
		{"relation localized field outside catalog", credit(func(i *RelationItem) { i.Localized = []FieldValue{{ID: "role", Value: Text("x")}} }), `relation item "credit-a" localized field "role" does not match the catalog`},
		{"relation localized value outside schema", credit(func(i *RelationItem) { i.Localized = []FieldValue{{ID: "alias", Value: unknown}} }), `relation item "credit-a" localized field "alias": object field "nope" is not in the catalog`},
		{"relation file field outside catalog", credit(func(i *RelationItem) { i.Files = []FileBinding{{Field: "nope", File: "x"}} }), `relation item "credit-a" file field "nope" does not match the catalog`},
		{"relation file on value field", credit(func(i *RelationItem) { i.Files = []FileBinding{{Field: "role", File: "x"}} }), `relation item "credit-a" file target "role" does not match the catalog`},
		{"relation file with path on file field", credit(func(i *RelationItem) {
			i.Files = []FileBinding{{Field: "avatar", Path: []FieldPathSegment{ObjectPath("x")}, File: "x"}}
		}), `relation item "credit-a" file target "avatar/field:x" does not match the catalog`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDocumentCatalogValues(catalog, tc.nodes)
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != tc.want {
				t.Fatalf("err = %q, want %q", got, tc.want)
			}
		})
	}
}

// A missing grandparent used to surface as a parent cycle of the grandchild
// when the grandchild was listed first.
func TestValidateNodesReportsMissingAncestorParent(t *testing.T) {
	nodes := []Node{
		{ID: "child", Kind: "paragraph", Parent: "parent"},
		{ID: "parent", Kind: "paragraph", Parent: "ghost"},
	}
	err := validateNodes(nodes)
	if err == nil || err.Error() != `block "parent" has missing parent "ghost"` {
		t.Fatalf("err = %v", err)
	}
}
