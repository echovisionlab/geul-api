package documenttext

import (
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

// A document root's handle differs per type, and its title unit carries no block
// but is named after the field.
func TestTranslationUnitEditNamesTheUnitOfTheEditedField(t *testing.T) {
	targets := map[string]string{"ko": "번역"}
	for _, root := range []core.BlockID{"document", "series", "form-root"} {
		edit := translationUnitEdit("Title", core.FieldTarget{Block: root, Field: "title"}, targets)
		if edit.Block != "" || edit.FieldUnit != "entity:title" {
			t.Errorf("root %s edit = %+v", root, edit)
		}
	}
	const block = "44444444-4444-4444-8444-444444444444"
	edit := translationUnitEdit("Body", core.FieldTarget{Block: block, Field: "content"}, targets)
	if edit.Block != block || edit.FieldUnit != "" || edit.SourceContains != "Body" {
		t.Errorf("content block edit = %+v", edit)
	}
	if edit := translationUnitEdit("Item", core.FieldTarget{Block: "document", Relation: "credits", Item: "a", Field: "title"}, targets); edit.FieldUnit != "" {
		t.Errorf("a relation item's field is not the document's: %+v", edit)
	}
}
