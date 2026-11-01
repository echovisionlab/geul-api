package aidocument

import (
	"slices"
	"testing"
)

func TestOperationHandlesUseCoreSelectorVocabulary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		operation Operation
		want      []string
	}{
		{"nested field", SetNestedFieldOperation("root", "settings", []FieldPathSegment{ObjectPath("labels"), ListPath("entry-a")}, Text("Label")), []string{"field:root/settings/field:labels/item:entry-a"}},
		{"relation field", SetRelationFieldOperation("root", "credits", "lead", "role", Text("Author")), []string{"field:root/credits/lead/role"}},
		{"insert with parent", InsertBlockOperation("child", "paragraph", "root", ""), []string{"block:child", "block:root"}},
		{"move to document root", MoveBlockOperation("child", "", ""), []string{"block:child"}},
		{"move relation item", MoveRelationItemOperation("root", "credits", "lead", "other", "credits", ""), []string{"relation-item:root/credits/lead", "relation:other/credits", "relation:root/credits"}},
		{"attach file", AttachFileOperation("root", "cover", FileReference("file-a")), []string{"field:root/cover"}},
		{"translation", CreateTranslationOperation(), []string{"translation:ko"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := OperationHandles(c.operation, "ko"); !slices.Equal(got, c.want) {
				t.Fatalf("handles = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAffectedHandlesDeduplicateTargetsAcrossOperations(t *testing.T) {
	t.Parallel()
	operations := []Operation{
		SetFieldOperation("root", "title", Text("First")),
		InsertBlockOperation("child", "paragraph", "root", ""),
		SetFieldOperation("root", "title", Text("Second")),
		CreateTranslationOperation(),
	}
	want := []string{"block:child", "block:root", "field:root/title", "translation:ko"}
	if got := AffectedHandles(operations, "ko"); !slices.Equal(got, want) {
		t.Fatalf("handles = %q, want %q", got, want)
	}
}
