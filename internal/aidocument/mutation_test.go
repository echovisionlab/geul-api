package aidocument

import (
	"reflect"
	"testing"
)

func TestDocumentAfterOperationsAppliesTypedBatchWithoutMutatingInput(t *testing.T) {
	objectSchema := FieldSchema{
		Kind: ValueKindObject, Ownership: FieldOwnershipShared,
		Fields: []NestedFieldRule{{Field: "caption", Schema: FieldSchema{Kind: ValueKindText, Ownership: FieldOwnershipShared}}},
	}
	document := Document{
		Identity: DocumentIdentity{Domain: DomainPost, Reference: "post-a"}, DocumentRevision: "revision-a",
		SourceLocale: "ko", Locale: "ko", LocaleExists: true,
		Catalog: Catalog{
			Fingerprint: "catalog-a", BlockKinds: []BlockKind{"paragraph"},
			Fields: []FieldRule{
				{BlockKind: "paragraph", Field: "content", ValueKind: ValueKindInline, Ownership: FieldOwnershipLocale, Translatable: true},
				{BlockKind: "paragraph", Field: "settings", ValueKind: ValueKindObject, Ownership: FieldOwnershipShared, Schema: &objectSchema},
				{BlockKind: "paragraph", Field: "attachment", Ownership: FieldOwnershipShared, File: true},
			},
			Relations:      []RelationRule{{BlockKind: "paragraph", Relation: "credits", ItemKinds: []RelationItemKind{"credit"}}},
			RelationFields: []RelationFieldRule{{BlockKind: "paragraph", Relation: "credits", ItemKind: "credit", Field: "name", ValueKind: ValueKindText, Ownership: FieldOwnershipLocale, Translatable: true}},
		},
		Nodes: []Node{{
			ID: "first", Kind: "paragraph", Order: 0,
			Shared:    []FieldValue{{ID: "settings", Value: Object(ObjectValue("caption", Text("before")))}},
			Localized: []FieldValue{{ID: "content", Value: RichText(InlineText("before"))}},
			Relations: []Relation{{ID: "credits", Items: []RelationItem{{ID: "one", Kind: "credit", Localized: []FieldValue{{ID: "name", Value: Text("one")}}}}}},
		}, {ID: "second", Kind: "paragraph", Order: 1}},
	}
	original := canonicalNodes(document.Nodes)
	updated, err := DocumentAfterOperations(document, []Operation{
		SetNestedFieldOperation("first", "settings", []FieldPathSegment{ObjectPath("caption")}, Text("after")),
		AttachFileOperation("first", "attachment", "file-a"),
		InsertRelationItemOperation("first", "credits", "two", "credit", "one"),
		SetRelationFieldOperation("first", "credits", "two", "name", Text("two")),
		InsertBlockOperation("third", "paragraph", "", "first"),
		MoveBlockOperation("second", "", "third"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(document.Nodes, original) {
		t.Fatal("input document was mutated")
	}
	if got := updated.Nodes[0].Shared[0].Value.Object[0].Value.Text; got != "after" {
		t.Fatalf("nested field = %q", got)
	}
	if len(updated.Nodes[0].Files) != 1 || updated.Nodes[0].Files[0].File != "file-a" {
		t.Fatalf("files = %+v", updated.Nodes[0].Files)
	}
	if got := updated.Nodes[0].Relations[0].Items[1].Localized[0].Value.Text; got != "two" {
		t.Fatalf("relation field = %q", got)
	}
	if got := []BlockID{updated.Nodes[0].ID, updated.Nodes[1].ID, updated.Nodes[2].ID}; !reflect.DeepEqual(got, []BlockID{"first", "third", "second"}) {
		t.Fatalf("block order = %v", got)
	}
}

func TestDocumentAfterOperationsDeletesTranslationValuesOnly(t *testing.T) {
	document := Document{
		Identity: DocumentIdentity{Domain: DomainPost, Reference: "post-a"}, DocumentRevision: "revision-a",
		SourceLocale: "ko", Locale: "en", LocaleExists: true,
		Catalog: Catalog{Fingerprint: "catalog-a", BlockKinds: []BlockKind{"paragraph"}, Fields: []FieldRule{
			{BlockKind: "paragraph", Field: "shared", ValueKind: ValueKindText, Ownership: FieldOwnershipShared},
			{BlockKind: "paragraph", Field: "content", ValueKind: ValueKindText, Ownership: FieldOwnershipLocale, Translatable: true},
		}},
		Nodes: []Node{{ID: "first", Kind: "paragraph", Shared: []FieldValue{{ID: "shared", Value: Text("kept")}}, Localized: []FieldValue{{ID: "content", Value: Text("removed")}}}},
	}
	updated, err := DocumentAfterOperations(document, []Operation{DeleteTranslationOperation()})
	if err != nil {
		t.Fatal(err)
	}
	if updated.LocaleExists || len(updated.Nodes[0].Localized) != 0 || len(updated.Nodes[0].Shared) != 1 {
		t.Fatalf("unexpected translation delete result: %+v", updated)
	}
}

func TestDocumentAfterOperationsPreservesEarlierSiblingMoves(t *testing.T) {
	for _, parent := range []BlockID{"", "root"} {
		for _, test := range []struct {
			name string
			tail Operation
			want []BlockID
		}{
			{"insert", InsertBlockOperation("d", "paragraph", parent, "a"), []BlockID{"c", "a", "d", "b"}},
			{"move", MoveBlockOperation("b", parent, "a"), []BlockID{"c", "a", "b"}},
			{"delete", DeleteBlockOperation("b"), []BlockID{"c", "a"}},
		} {
			t.Run(string(parent)+"/"+test.name, func(t *testing.T) {
				document := Document{
					Identity: DocumentIdentity{Domain: DomainForm, Reference: "form-a"}, DocumentRevision: "r1",
					SourceLocale: "ko", Locale: "ko", LocaleExists: true,
					Catalog: Catalog{Fingerprint: "catalog", BlockKinds: []BlockKind{"paragraph"}},
					Nodes: []Node{
						{ID: "a", Kind: "paragraph", Parent: parent, Order: 0},
						{ID: "b", Kind: "paragraph", Parent: parent, Order: 1},
						{ID: "c", Kind: "paragraph", Parent: parent, Order: 2},
					},
				}
				if parent != "" {
					document.Nodes = append(document.Nodes, Node{ID: parent, Kind: "paragraph"})
				}
				operations := []Operation{MoveBlockOperation("c", parent, ""), test.tail}
				validation := ValidateOperations(document, ApplyRequest{
					Protocol: ProtocolVersion, Profile: DomainForm, Document: "form-a", Locale: "ko",
					ExpectedDocumentRevision: "r1", Operations: operations,
				})
				if !validation.Valid() {
					t.Fatalf("valid sibling batch rejected: %+v", validation)
				}
				updated, err := DocumentAfterOperations(document, operations)
				if err != nil {
					t.Fatal(err)
				}
				var got []BlockID
				for _, node := range updated.Nodes {
					if node.Parent == parent {
						if node.Order != len(got) {
							t.Fatalf("sibling %q order=%d, want=%d", node.ID, node.Order, len(got))
						}
						got = append(got, node.ID)
					}
				}
				if !reflect.DeepEqual(got, test.want) {
					t.Fatalf("sibling order=%v, want=%v", got, test.want)
				}
			})
		}
	}
}

// Item kinds of one relation may declare the same field with different
// ownership, so the stored half must follow the target item's kind.
func TestDocumentAfterOperationsStoresRelationFieldByItemKindOwnership(t *testing.T) {
	document := Document{
		Identity: DocumentIdentity{Domain: DomainPost, Reference: "post-a"}, DocumentRevision: "revision-a",
		SourceLocale: "ko", Locale: "ko", LocaleExists: true,
		Catalog: Catalog{
			Fingerprint: "catalog-a", BlockKinds: []BlockKind{"paragraph"},
			Relations: []RelationRule{{BlockKind: "paragraph", Relation: "links", ItemKinds: []RelationItemKind{"internal", "external"}}},
			RelationFields: []RelationFieldRule{
				{BlockKind: "paragraph", Relation: "links", ItemKind: "internal", Field: "label", ValueKind: ValueKindText, Ownership: FieldOwnershipShared},
				{BlockKind: "paragraph", Relation: "links", ItemKind: "external", Field: "label", ValueKind: ValueKindText, Ownership: FieldOwnershipLocale, Translatable: true},
			},
		},
		Nodes: []Node{{ID: "first", Kind: "paragraph", Relations: []Relation{{ID: "links", Items: []RelationItem{
			{ID: "home", Kind: "internal"},
			{ID: "docs", Kind: "external", Order: 1},
		}}}}},
	}
	operations := []Operation{
		SetRelationFieldOperation("first", "links", "home", "label", Text("Home")),
		SetRelationFieldOperation("first", "links", "docs", "label", Text("Docs")),
	}
	request := ApplyRequest{
		Protocol: ProtocolVersion, Profile: DomainPost, Document: "post-a", Locale: "ko",
		ExpectedDocumentRevision: "revision-a", Operations: operations,
	}
	if result := ValidateOperations(document, request); !result.Valid() {
		t.Fatalf("validation = %+v", result)
	}
	updated, err := DocumentAfterOperations(document, operations)
	if err != nil {
		t.Fatal(err)
	}
	items := updated.Nodes[0].Relations[0].Items
	if !reflect.DeepEqual(items[0].Shared, []FieldValue{{ID: "label", Value: Text("Home")}}) || len(items[0].Localized) != 0 {
		t.Fatalf("internal item = %+v", items[0])
	}
	if !reflect.DeepEqual(items[1].Localized, []FieldValue{{ID: "label", Value: Text("Docs")}}) || len(items[1].Shared) != 0 {
		t.Fatalf("external item = %+v", items[1])
	}
	if err := updated.validate(); err != nil {
		t.Fatalf("updated document is invalid: %v", err)
	}
}
