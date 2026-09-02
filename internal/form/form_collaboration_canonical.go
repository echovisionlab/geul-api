package form

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func formCanonicalDocumentFromMeta(meta *intrav1.FormMeta) (*string, []byte, error) {
	if meta == nil || meta.Schema == nil {
		return nil, nil, fmt.Errorf("canonical Form schema is required")
	}
	schema, err := normalizeCollaborativeFormSchemaPatch([]byte(*meta.Schema))
	if err != nil {
		return nil, nil, err
	}
	return meta.Title, schema, nil
}

func formCanonicalLocaleTargets(
	title *string,
	schema []byte,
) ([]*managev1.AIDocumentFieldTarget, error) {
	if err := validateCanonicalFormSchema(schema); err != nil {
		return nil, err
	}
	var value formDocumentObject
	if err := json.Unmarshal(schema, &value); err != nil {
		return nil, fmt.Errorf("schema must be valid JSON: %w", err)
	}
	targets := make([]*managev1.AIDocumentFieldTarget, 0)
	if title != nil {
		targets = append(targets, intrav1.FormRootTitleTarget())
	}
	var visitErr error
	walkFormSchemaTranslationText(formValueSlice(value["steps"]), func(text formSchemaTranslationText) {
		if visitErr != nil {
			return
		}
		raw, exists := text.object[text.field]
		if !exists {
			return
		}
		if _, ok := raw.(string); !ok {
			visitErr = fmt.Errorf("%s must be a string when present", text.path)
			return
		}
		targets = append(targets, &managev1.AIDocumentFieldTarget{
			Owner: &managev1.AIDocumentFieldTarget_BlockHandle{
				BlockHandle: text.blockHandle,
			},
			FieldHandle: text.fieldHandle,
		})
	})
	if visitErr != nil {
		return nil, visitErr
	}
	sort.Slice(targets, func(i, j int) bool {
		return formLocaleTargetKey(targets[i]) < formLocaleTargetKey(targets[j])
	})
	return targets, nil
}

func validateFormCanonicalLocalePresence(
	title *string,
	schema []byte,
	provided []*managev1.AIDocumentFieldTarget,
) error {
	expected, err := formCanonicalLocaleTargets(title, schema)
	if err != nil {
		return err
	}
	if len(provided) != len(expected) {
		return fmt.Errorf("present_locale_values must exactly match canonical Form locale values")
	}
	previous := ""
	for index, target := range provided {
		key, err := validatedFormLocaleTargetKey(target)
		if err != nil {
			return err
		}
		if index != 0 && key <= previous {
			return fmt.Errorf("present_locale_values must be canonical-sorted and duplicate-free")
		}
		if key != formLocaleTargetKey(expected[index]) {
			return fmt.Errorf("present_locale_values must exactly match canonical Form locale values")
		}
		previous = key
	}
	return nil
}

func validatedFormLocaleTargetKey(target *managev1.AIDocumentFieldTarget) (string, error) {
	if target == nil || target.GetBlockHandle() == "" || target.GetFieldHandle() == "" ||
		target.GetRelationItem() != nil || len(target.GetPath()) != 0 {
		return "", fmt.Errorf("present_locale_values contains a forbidden Form target")
	}
	return formLocaleTargetKey(target), nil
}

func formLocaleTargetKey(target *managev1.AIDocumentFieldTarget) string {
	return target.GetBlockHandle() + "\x00" + target.GetFieldHandle()
}

func formAIDocumentEmptyTargetSchema(source []byte) ([]byte, error) {
	if err := validateCanonicalFormSchema(source); err != nil {
		return nil, fmt.Errorf("form source schema is invalid: %w", err)
	}
	var value any
	if err := json.Unmarshal(source, &value); err != nil {
		return nil, fmt.Errorf("form source schema is invalid: %w", err)
	}
	stripped, err := json.Marshal(formAIDocumentSharedShape(value))
	if err != nil {
		return nil, fmt.Errorf("encode empty Form target schema: %w", err)
	}
	if err := validateFormAIDocumentTargetSchema(source, stripped); err != nil {
		return nil, err
	}
	return stripped, nil
}

func formMetaFromCanonicalRow(row formAIDocumentLocaleRow) (*intrav1.FormMeta, error) {
	if len(row.Schema) == 0 {
		return nil, fmt.Errorf("canonical Form locale schema is missing")
	}
	if err := validateCanonicalFormSchema(row.Schema); err != nil {
		return nil, err
	}
	schema := string(row.Schema)
	return &intrav1.FormMeta{Title: row.Title, Schema: &schema}, nil
}

func formCanonicalContentText(schema []byte) *string {
	text := strings.TrimSpace(extractFormSchemaTextFromJSON(schema))
	if text == "" {
		return nil
	}
	return &text
}
