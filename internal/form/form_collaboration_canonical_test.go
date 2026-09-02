package form

import (
	"encoding/json"
	"testing"
	"time"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const formCanonicalCollaborationSchema = "{\"id\":\"schema_A\",\"steps\":[{\"id\":\"step_A\",\"title\":\"\",\"description\":\"\",\"fields\":[]},{\"id\":\"step_B\",\"title\":\"Second\",\"fields\":[{\"id\":\"field_A\",\"key\":\"email\",\"type\":\"email\",\"label\":\"\",\"validation\":{\"validators\":[]}}]}]}"

func TestFormCanonicalLocaleTargetsPreserveEmptyAndAbsentValues(t *testing.T) {
	title := ""
	targets, err := formCanonicalLocaleTargets(
		&title, []byte(formCanonicalCollaborationSchema),
	)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(targets))
	for _, target := range targets {
		keys = append(keys, formLocaleTargetKey(target))
	}
	for _, expected := range []string{
		"document\x00title",
		"form:field:field_A\x00label",
		"form:step:step_A\x00description",
		"form:step:step_A\x00title",
		"form:step:step_B\x00title",
	} {
		if !containsFormTargetKey(keys, expected) {
			t.Fatalf("canonical locale targets %q do not contain %q", keys, expected)
		}
	}
	if containsFormTargetKey(keys, "form:field:field_A\x00description") {
		t.Fatal("absent field description became a present locale value")
	}
	if err := validateFormCanonicalLocalePresence(
		&title, []byte(formCanonicalCollaborationSchema), targets,
	); err != nil {
		t.Fatal(err)
	}
}

func TestValidateFormCanonicalLocalePresenceRejectsOrderDuplicatesAndMismatch(t *testing.T) {
	title := "Contact"
	schema := []byte(formCanonicalCollaborationSchema)
	targets, err := formCanonicalLocaleTargets(&title, schema)
	if err != nil {
		t.Fatal(err)
	}
	reversed := append([]*managev1.AIDocumentFieldTarget(nil), targets...)
	reversed[0], reversed[len(reversed)-1] = reversed[len(reversed)-1], reversed[0]
	if err := validateFormCanonicalLocalePresence(&title, schema, reversed); err == nil {
		t.Fatal("non-canonical presence order was accepted")
	}
	duplicate := append(append([]*managev1.AIDocumentFieldTarget(nil), targets...), targets[len(targets)-1])
	if err := validateFormCanonicalLocalePresence(&title, schema, duplicate); err == nil {
		t.Fatal("duplicate presence was accepted")
	}
	if err := validateFormCanonicalLocalePresence(nil, schema, targets); err == nil {
		t.Fatal("title presence without a canonical title was accepted")
	}
}

func TestFormEmptyTargetSchemaPreservesOrderedSharedTopology(t *testing.T) {
	target, err := formAIDocumentEmptyTargetSchema([]byte(formCanonicalCollaborationSchema))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(target, &value); err != nil {
		t.Fatal(err)
	}
	steps := value["steps"].([]any)
	first := steps[0].(map[string]any)
	second := steps[1].(map[string]any)
	field := second["fields"].([]any)[0].(map[string]any)
	if len(steps) != 2 || first["id"] != "step_A" || second["id"] != "step_B" {
		t.Fatalf("ordered target steps = %+v", steps)
	}
	if _, exists := first["title"]; exists {
		t.Fatal("empty target retained first source title")
	}
	if _, exists := second["title"]; exists {
		t.Fatal("empty target retained second source title")
	}
	if _, exists := field["label"]; exists {
		t.Fatal("empty target retained source field label")
	}
}

func TestFormCollaborativeCanonicalNoopAndExplicitEmptyTitle(t *testing.T) {
	schema := []byte(formCanonicalCollaborationSchema)
	now := time.Date(2026, time.August, 25, 0, 0, 0, 0, time.UTC)
	current := formAIDocumentLocaleRow{Schema: schema}
	update, titleChanged := formCollaborativeLocaleUpdates(current, nil, schema, now)
	if titleChanged || len(update.fields) != 0 {
		t.Fatalf("canonical no-op = (%+v, %v)", update, titleChanged)
	}
	empty := ""
	update, titleChanged = formCollaborativeLocaleUpdates(current, &empty, schema, now)
	if !titleChanged || len(update.fields) != 2 || update.fields["title"] == nil {
		t.Fatalf("explicit empty title update = (%+v, %v)", update, titleChanged)
	}
}

func TestFormCanonicalDocumentRejectsInvalidSchemaAtomically(t *testing.T) {
	invalid := "{\"id\":\"schema_A\",\"steps\":\"invalid\"}"
	if _, _, err := formCanonicalDocumentFromMeta(&intrav1.FormMeta{Schema: &invalid}); err == nil {
		t.Fatal("invalid canonical Form schema was accepted")
	}
}

func containsFormTargetKey(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
