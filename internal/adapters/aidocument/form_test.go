package aidocumentadapter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	formintrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
)

const formAuthoringSchemaForTest = `{"id":"schema","steps":[{"id":"step-a","title":"Contact","fields":[{"id":"field-a","key":"email","type":"email","label":"Email","validation":{"validators":[]}}]}]}`

type exactFormDocumentAPI struct {
	state         formdomain.AIDocumentState
	result        formdomain.AIDocumentMutationResult
	authorizeErr  error
	executeErr    error
	mutation      formdomain.AIDocumentMutation
	loadCalls     int
	executeCalls  int
	compilerCalls int
}

func (a *exactFormDocumentAPI) LoadAIDocumentState(
	context.Context,
	string,
	string,
) (formdomain.AIDocumentState, error) {
	a.loadCalls++
	return a.state, nil
}

func (a *exactFormDocumentAPI) ExecuteAIDocumentMutation(
	_ context.Context,
	formID string,
	locale string,
	_ formdomain.AIDocumentExecutionMode,
	compiler formdomain.AIDocumentMutationCompiler,
) (formdomain.AIDocumentMutationResult, error) {
	a.executeCalls++
	if a.authorizeErr != nil {
		return formdomain.AIDocumentMutationResult{}, a.authorizeErr
	}
	if formID != a.state.FormID || locale != a.state.Locale {
		return formdomain.AIDocumentMutationResult{}, errors.New("unexpected Form identity or locale")
	}
	a.compilerCalls++
	mutation, err := compiler(a.state)
	if err != nil {
		return formdomain.AIDocumentMutationResult{}, err
	}
	a.mutation = mutation
	if a.executeErr != nil {
		return formdomain.AIDocumentMutationResult{}, a.executeErr
	}
	return a.result, nil
}

func TestProjectFormNodesPreservesMissingAndExplicitEmpty(t *testing.T) {
	sourceTitle := "Contact"
	targetTitle := ""
	state := formdomain.AIDocumentState{
		FormID: "019c89aa-6798-7a37-8532-11e03f729c35", DocumentRevision: "42",
		SourceLocale: "en", Locale: "ko", LocaleExists: true,
		SourceTitle: &sourceTitle, LocaleTitle: &targetTitle,
		SourceSchema: []byte(`{"id":"schema","steps":[{"id":"step-a","title":"Contact","description":"Help","fields":[{"id":"field-a","key":"choice","name":"choice","type":"select","label":"Choice","description":"Reply","options":[{"id":"option-a","value":"a","label":"A"}],"validation":{"validators":[{"id":"validator-a","predicate":"required","message":"Required"}]}}]}]}`),
		LocaleSchema: []byte(`{"id":"schema","steps":[{"id":"step-a","fields":[{"id":"field-a","key":"choice","name":"choice","type":"select","label":"","options":[{"id":"option-a","value":"a","label":""}],"validation":{"validators":[{"id":"validator-a","predicate":"required","message":""}]}}]}]}`),
	}
	nodes, err := projectFormNodes(state)
	if err != nil {
		t.Fatal(err)
	}
	root := formNode(nodes, formRootID)
	if title, ok := formField(root.Localized, formRootTitleField); !ok || title.Text != "" {
		t.Fatalf("root title = (%+v, %v), want explicit empty", title, ok)
	}
	var fieldLocalized map[string]string
	for _, node := range nodes {
		if node.Kind != formFieldKind {
			continue
		}
		fieldLocalized = map[string]string{}
		for _, value := range node.Localized {
			fieldLocalized[string(value.ID)] = value.Value.Text
		}
	}
	if value, ok := fieldLocalized[string(formFieldLabelField)]; !ok || value != "" {
		t.Fatalf("label = (%q, %v), want explicit empty", value, ok)
	}
	if _, ok := fieldLocalized[string(formFieldDescriptionField)]; ok {
		t.Fatal("missing target description was projected as a value")
	}
	wantHandles := map[core.BlockID]bool{
		formintrav1.FormRootBlockHandle: true,
		"form:step:step-a":              true,
		"form:field:field-a":            true,
		"form:option:option-a":          true,
		"form:validator:validator-a":    true,
	}
	for _, node := range nodes {
		delete(wantHandles, node.ID)
	}
	if len(wantHandles) != 0 {
		t.Fatalf("generated Form handles missing from projection: %+v", wantHandles)
	}

	document := formDocumentForTest(state, nodes)
	body, err := formDocumentSchema(document)
	if err != nil {
		t.Fatal(err)
	}
	var projected map[string]any
	if err := json.Unmarshal(body, &projected); err != nil {
		t.Fatal(err)
	}
	field := projected["steps"].([]any)[0].(map[string]any)["fields"].([]any)[0].(map[string]any)
	if value, ok := field["label"]; !ok || value != "" {
		t.Fatalf("serialized label = (%v, %v), want explicit empty", value, ok)
	}
	if _, ok := field["description"]; ok {
		t.Fatal("serialized missing description became present")
	}
}

func TestProjectFormNodesMissingTargetHasNoLocaleValues(t *testing.T) {
	sourceTitle := "Contact"
	state := formdomain.AIDocumentState{FormID: "019c89aa-6798-7a37-8532-11e03f729c35", DocumentRevision: "41", SourceLocale: "en", Locale: "ko", SourceTitle: &sourceTitle, SourceSchema: []byte(`{"id":"schema","steps":[{"id":"step-a","title":"Contact","fields":[{"id":"field-a","key":"email","type":"email","label":"Email","validation":{"validators":[]}}]}]}`)}
	nodes, err := projectFormNodes(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if len(node.Localized) != 0 {
			t.Fatalf("missing locale node %s has values %+v", node.ID, node.Localized)
		}
	}
}

func TestProjectFormNodesExistingSparseTargetMayMaterializeLater(t *testing.T) {
	state := formdomain.AIDocumentState{
		FormID: "019c89aa-6798-7a37-8532-11e03f729c35", DocumentRevision: "41",
		SourceLocale: "en", Locale: "ko", LocaleExists: true,
		SourceSchema: []byte(`{"id":"schema","steps":[{"id":"step-a","title":"Contact","fields":[{"id":"field-a","key":"email","type":"email","label":"Email","validation":{"validators":[]}}]}]}`),
	}
	nodes, err := projectFormNodes(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if len(node.Localized) != 0 {
			t.Fatalf("sparse target node %s has values %+v", node.ID, node.Localized)
		}
	}
}

func TestProjectFormNodesRejectsLegacyFallbackIdentities(t *testing.T) {
	tests := []struct {
		name   string
		schema string
	}{
		{
			name:   "option value fallback",
			schema: `{"id":"schema","steps":[{"id":"step-a","fields":[{"id":"field-a","key":"choice","type":"select","options":[{"value":"a","label":"A"}]}]}]}`,
		},
		{
			name:   "validator predicate fallback",
			schema: `{"id":"schema","steps":[{"id":"step-a","fields":[{"id":"field-a","key":"name","type":"text","validation":{"validators":[{"predicate":"required","message":"Required"}]}}]}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := formdomain.AIDocumentState{
				FormID: "019c89aa-6798-7a37-8532-11e03f729c35", DocumentRevision: "41",
				SourceLocale: "en", Locale: "ko", SourceSchema: []byte(test.schema),
			}
			if _, err := projectFormNodes(state); err == nil {
				t.Fatal("legacy fallback identity was accepted for DCDP authoring")
			}
		})
	}
}

func TestProjectFormNodesRejectsLegacyTargetIdentity(t *testing.T) {
	source := `{"id":"schema","steps":[{"id":"step-a","fields":[{"id":"field-a","key":"choice","type":"select","options":[{"id":"option-a","value":"a","label":"A"}]}]}]}`
	target := `{"id":"schema","steps":[{"id":"step-a","fields":[{"id":"field-a","key":"choice","type":"select","options":[{"value":"a","label":"가"}]}]}]}`
	state := formdomain.AIDocumentState{
		FormID: "019c89aa-6798-7a37-8532-11e03f729c35", DocumentRevision: "41",
		SourceLocale: "en", Locale: "ko", LocaleExists: true,
		SourceSchema: []byte(source), LocaleSchema: []byte(target),
	}
	if _, err := projectFormNodes(state); err == nil {
		t.Fatal("legacy target fallback identity was accepted for DCDP authoring")
	}
}

func TestFormExactMutationPathDoesNotEnterPublicLoad(t *testing.T) {
	formID, contributor := uuid.NewString(), uuid.NewString()
	title := "Contact"
	api := &exactFormDocumentAPI{
		state: formdomain.AIDocumentState{
			FormID: formID, DocumentRevision: "41", SourceLocale: "en", Locale: "en", LocaleExists: true,
			SourceTitle: &title, LocaleTitle: &title, ViewerMemberID: contributor,
			SourceSchema: []byte(formAuthoringSchemaForTest),
			LocaleSchema: []byte(formAuthoringSchemaForTest),
		},
		result: formdomain.AIDocumentMutationResult{DocumentRevision: "42", Changed: true},
	}
	port := &formPort{service: api, catalog: formCatalog()}
	service, err := core.NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainForm,
		Document: core.DocumentReference(formID), Locale: "en", ExpectedDocumentRevision: "41",
		Operations: []core.Operation{core.SetFieldOperation(formRootID, formRootTitleField, core.Text("Changed"))},
	}

	validation, err := service.Validate(t.Context(), request)
	if err != nil || !validation.Valid() {
		t.Fatalf("Validate() = (%+v, %v)", validation, err)
	}
	result, err := service.Apply(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.DocumentRevision != "42" {
		t.Fatalf("document revision = %q, want 42", result.DocumentRevision)
	}
	if api.executeCalls != 2 || api.compilerCalls != 2 {
		t.Fatalf("exact calls = execute:%d compiler:%d", api.executeCalls, api.compilerCalls)
	}
	if api.loadCalls != 0 {
		t.Fatalf("public load calls = %d, want 0", api.loadCalls)
	}
}

func TestFormExactMutationAuthorizesBeforeAdapterCompilation(t *testing.T) {
	formID := uuid.NewString()
	denied := errors.New("form not found")
	api := &exactFormDocumentAPI{
		state:        formdomain.AIDocumentState{FormID: formID, Locale: "en"},
		authorizeErr: denied,
	}
	service, err := core.NewService(&formPort{service: api, catalog: formCatalog()})
	if err != nil {
		t.Fatal(err)
	}
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainForm,
		Document: core.DocumentReference(formID), Locale: "en", ExpectedDocumentRevision: "41",
		Operations: []core.Operation{core.DeleteBlockOperation("unknown")},
	}

	_, err = service.Validate(t.Context(), request)
	if !errors.Is(err, denied) {
		t.Fatalf("Validate() error = %v", err)
	}
	if api.compilerCalls != 0 || api.loadCalls != 0 {
		t.Fatalf("unauthorized request reached adapter: compiler:%d load:%d", api.compilerCalls, api.loadCalls)
	}
}

func TestFormExactTargetMutationPassesTargetFenceAndMapsTargetConflict(t *testing.T) {
	formID, contributor := uuid.NewString(), uuid.NewString()
	title, targetRevision, nextTargetRevision := "문의", "form-target-1", "form-target-2"
	schema := []byte(formAuthoringSchemaForTest)
	api := &exactFormDocumentAPI{
		state: formdomain.AIDocumentState{
			FormID: formID, DocumentRevision: "form-document-1", TargetRevision: &targetRevision,
			SourceLocale: "en", Locale: "ko", LocaleExists: true,
			SourceTitle: &title, LocaleTitle: &title, SourceSchema: schema, LocaleSchema: schema,
			ViewerMemberID: contributor,
		},
		result: formdomain.AIDocumentMutationResult{
			DocumentRevision: "form-document-1", TargetRevision: &nextTargetRevision, Changed: true,
		},
	}
	service, err := core.NewService(&formPort{service: api, catalog: formCatalog()})
	if err != nil {
		t.Fatal(err)
	}
	expectedTarget := core.Revision(targetRevision)
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainForm,
		Document: core.DocumentReference(formID), Locale: "ko",
		ExpectedDocumentRevision: "form-document-1", ExpectedTargetRevision: &expectedTarget,
		Operations: []core.Operation{core.SetFieldOperation(formRootID, formRootTitleField, core.Text("새 문의"))},
	}
	result, err := service.Apply(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.DocumentRevision != "form-document-1" || result.TargetRevision == nil ||
		*result.TargetRevision != "form-target-2" || api.mutation.ExpectedTargetRevision == nil ||
		*api.mutation.ExpectedTargetRevision != targetRevision {
		t.Fatalf("target fence/result mismatch: result=%+v mutation=%+v", result, api.mutation)
	}

	unsetRequest := request
	unsetRequest.Operations = []core.Operation{core.UnsetFieldOperation(formRootID, formRootTitleField)}
	validation, err := service.Validate(t.Context(), unsetRequest)
	if err != nil || len(validation.Issues) != 1 || validation.Issues[0].Code != core.IssueTargetFieldForbidden {
		t.Fatalf("target unset validation = (%+v, %v)", validation, err)
	}

	currentTarget := "form-target-3"
	api.executeErr = &formdomain.AIDocumentRevisionConflictError{
		Kind:                    formdomain.AIDocumentTargetRevisionConflict,
		CurrentDocumentRevision: "form-document-1", CurrentTargetRevision: &currentTarget,
	}
	_, err = service.Apply(t.Context(), request)
	var conflict *core.ConflictError
	if !errors.As(err, &conflict) || conflict.Conflict.Code != core.ConflictTargetRevision ||
		conflict.Conflict.CurrentTargetRevision == nil || *conflict.Conflict.CurrentTargetRevision != core.Revision(currentTarget) {
		t.Fatalf("target conflict mapping = %v", err)
	}
}

func formDocumentForTest(state formdomain.AIDocumentState, nodes []core.Node) core.Document {
	return core.Document{SourceLocale: core.Locale(state.SourceLocale), Locale: core.Locale(state.Locale), LocaleExists: state.LocaleExists, Catalog: formCatalog(), Nodes: nodes}
}
