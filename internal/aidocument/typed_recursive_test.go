package aidocument

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTypedRecursiveValueCompactRoundTripPreservesStableIdentityAndExplicitEmpty(t *testing.T) {
	value := List(
		StableItem("common", Object(
			ObjectValue("kind", Text("common")),
			ObjectValue("source", Text("")),
			ObjectValue("channels", List(
				StableItem("channel-a", Object(ObjectValue("kind", Text("none")))),
				StableItem("channel-b", Object(ObjectValue("kind", Text("none")))),
			)),
		)),
	)
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Value
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, value) {
		t.Fatalf("typed recursive value changed during compact round trip:\nwant: %#v\n got: %#v", value, decoded)
	}
}

func TestRecursiveCatalogValidatesFixedAndFieldIdentifiedLists(t *testing.T) {
	text := func() FieldSchema {
		return FieldSchema{Kind: ValueKindText, Ownership: FieldOwnershipSource}
	}
	channel := FieldSchema{
		Kind: ValueKindObject, Ownership: FieldOwnershipSource,
		Fields: []NestedFieldRule{{Field: "kind", Schema: text()}},
	}
	stage := FieldSchema{
		Kind: ValueKindObject, Ownership: FieldOwnershipSource,
		Fields: []NestedFieldRule{
			{Field: "kind", Schema: text()},
			{Field: "channels", Schema: FieldSchema{
				Kind: ValueKindList, Ownership: FieldOwnershipSource,
				Item:     &channel,
				Identity: ListIdentityRule{Kind: ListIdentityFixed, Handles: []RelationItemID{"channel-a", "channel-b"}},
			}},
		},
	}
	stages := FieldSchema{
		Kind: ValueKindList, Ownership: FieldOwnershipSource,
		Item:     &stage,
		Identity: ListIdentityRule{Kind: ListIdentityField, Field: "kind"},
	}
	document := Document{
		Identity: DocumentIdentity{Domain: DomainPost, Reference: "post-id"}, DocumentRevision: "rev-1",
		SourceLocale: "ko", Locale: "ko", LocaleExists: true,
		Catalog: Catalog{
			Fingerprint: "catalog", BlockKinds: []BlockKind{"shader"},
			Fields: []FieldRule{{
				BlockKind: "shader", Field: "stages", ValueKind: ValueKindList,
				Ownership: FieldOwnershipSource, Schema: &stages,
			}},
		},
		Nodes: []Node{{ID: "shader-id", Kind: "shader", Shared: []FieldValue{{
			ID: "stages", Value: List(StableItem("common", Object(
				ObjectValue("kind", Text("common")),
				ObjectValue("channels", List(
					StableItem("channel-a", Object(ObjectValue("kind", Text("none")))),
					StableItem("channel-b", Object(ObjectValue("kind", Text("none")))),
				)),
			))),
		}}}},
	}
	if err := document.validate(); err != nil {
		t.Fatalf("valid stable recursive document rejected: %v", err)
	}

	broken := cloneValue(document.Nodes[0].Shared[0].Value)
	broken.List[0].ID = "vertex"
	document.Nodes[0].Shared[0].Value = broken
	if err := document.validate(); err == nil || !strings.Contains(err.Error(), "must equal text field") {
		t.Fatalf("field-identified list mismatch was not rejected: %v", err)
	}
}

func TestNestedFieldOperationUsesCatalogShapeAndStableListHandle(t *testing.T) {
	text := FieldSchema{Kind: ValueKindText, Ownership: FieldOwnershipSource}
	stage := FieldSchema{
		Kind: ValueKindObject, Ownership: FieldOwnershipSource,
		Fields: []NestedFieldRule{{Field: "source", Schema: text}},
	}
	stages := FieldSchema{
		Kind: ValueKindList, Ownership: FieldOwnershipSource, Item: &stage,
		Identity: ListIdentityRule{Kind: ListIdentityFixed, Handles: []RelationItemID{"common", "vertex"}},
	}
	document := Document{
		Identity: DocumentIdentity{Domain: DomainPost, Reference: "post-id"}, DocumentRevision: "rev-1",
		SourceLocale: "ko", Locale: "ko", LocaleExists: true,
		Catalog: Catalog{Fingerprint: "catalog", BlockKinds: []BlockKind{"shader"}, Fields: []FieldRule{{
			BlockKind: "shader", Field: "stages", ValueKind: ValueKindList,
			Ownership: FieldOwnershipSource, Schema: &stages,
		}}},
		Nodes: []Node{{ID: "shader-id", Kind: "shader", Shared: []FieldValue{{
			ID: "stages", Value: List(
				StableItem("common", Object(ObjectValue("source", Text("")))),
				StableItem("vertex", Object(ObjectValue("source", Text("void main() {}")))),
			),
		}}}},
	}
	request := ApplyRequest{
		Protocol: ProtocolVersion, Profile: DomainPost, Document: "post-id", Locale: "ko", ExpectedDocumentRevision: "rev-1",
		Operations: []Operation{SetNestedFieldOperation(
			"shader-id", "stages", []FieldPathSegment{ListPath("vertex"), ObjectPath("source")}, Text("void main() { gl_Position = vec4(0.0); }"),
		)},
	}
	if result := ValidateOperations(document, request); !result.Valid() {
		t.Fatalf("stable nested field operation rejected: %+v", result)
	}

	request.Operations[0] = SetNestedFieldOperation(
		"shader-id", "stages", []FieldPathSegment{ListPath("channel-0"), ObjectPath("source")}, Text("x"),
	)
	if result := ValidateOperations(document, request); result.Valid() || result.Issues[0].Code != IssueUnknownField {
		t.Fatalf("unknown fixed list handle was not rejected: %+v", result)
	}
}

func TestCanonicalProjectionDeepCopiesRecursiveValues(t *testing.T) {
	source := []Node{{ID: "block", Kind: "shader", Shared: []FieldValue{{
		ID: "stages", Value: List(StableItem("common", Object(ObjectValue("source", Text("original"))))),
	}}}}
	projected := canonicalNodes(source)
	projected[0].Shared[0].Value.List[0].Value.Object[0].Value.Text = "changed"
	if got := source[0].Shared[0].Value.List[0].Value.Object[0].Value.Text; got != "original" {
		t.Fatalf("canonical projection aliased recursive source value: %q", got)
	}
}
