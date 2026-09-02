package aidocumentadapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	formintrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
)

const (
	formRootKind                core.BlockKind = "form"
	formStepKind                core.BlockKind = "form_step"
	formFieldKind               core.BlockKind = "form_field"
	formOptionKind              core.BlockKind = "form_option"
	formValidatorKind           core.BlockKind = "form_validator"
	formRawField                core.FieldID   = "source"
	formSchemaIDField           core.FieldID   = "schema_id"
	formRootTitleField          core.FieldID   = formintrav1.FormRootTitleFieldHandle
	formStepTitleField          core.FieldID   = formintrav1.FormStepTitleFieldHandle
	formStepDescriptionField    core.FieldID   = formintrav1.FormStepDescriptionFieldHandle
	formFieldLabelField         core.FieldID   = formintrav1.FormFieldLabelFieldHandle
	formFieldDescriptionField   core.FieldID   = formintrav1.FormFieldDescriptionFieldHandle
	formFieldPlaceholderField   core.FieldID   = formintrav1.FormFieldPlaceholderFieldHandle
	formFieldCheckboxLabelField core.FieldID   = formintrav1.FormFieldCheckboxLabelFieldHandle
	formOptionLabelField        core.FieldID   = formintrav1.FormOptionLabelFieldHandle
	formValidatorMessageField   core.FieldID   = formintrav1.FormValidatorMessageFieldHandle
)

const formRootID core.BlockID = formintrav1.FormRootBlockHandle

type formPort struct {
	service formDocumentAPI
	catalog core.Catalog
}

type formDocumentAPI interface {
	LoadAIDocumentState(context.Context, string, string) (formdomain.AIDocumentState, error)
	ExecuteAIDocumentMutation(
		context.Context,
		string,
		string,
		formdomain.AIDocumentExecutionMode,
		formdomain.AIDocumentMutationCompiler,
	) (formdomain.AIDocumentMutationResult, error)
}

func NewFormRegistration(service *formdomain.InternalFormService) (DomainRegistration, error) {
	if service == nil {
		return DomainRegistration{}, errors.New("form AI document service is required")
	}
	return DomainRegistration{Domain: core.DomainForm, Port: &formPort{service: service, catalog: formCatalog()}}, nil
}

func formCatalog() core.Catalog {
	fields := []core.FieldRule{
		{BlockKind: formRootKind, Field: formSchemaIDField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
		{BlockKind: formRootKind, Field: formRootTitleField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		{BlockKind: formStepKind, Field: formRawField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
		{BlockKind: formStepKind, Field: formStepTitleField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		{BlockKind: formStepKind, Field: formStepDescriptionField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		{BlockKind: formFieldKind, Field: formRawField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
		{BlockKind: formFieldKind, Field: formFieldLabelField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		{BlockKind: formFieldKind, Field: formFieldDescriptionField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		{BlockKind: formFieldKind, Field: formFieldPlaceholderField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		{BlockKind: formFieldKind, Field: formFieldCheckboxLabelField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		{BlockKind: formOptionKind, Field: formRawField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
		{BlockKind: formOptionKind, Field: formOptionLabelField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		{BlockKind: formValidatorKind, Field: formRawField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipSource},
		{BlockKind: formValidatorKind, Field: formValidatorMessageField, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
	}
	fingerprint := sha256.Sum256([]byte("dcdp/1:form:stable-direct-handles:source-topology:locale-copy:v2"))
	return core.Catalog{Fingerprint: hex.EncodeToString(fingerprint[:]), BlockKinds: []core.BlockKind{formRootKind, formStepKind, formFieldKind, formOptionKind, formValidatorKind}, Fields: fields}
}

func (p *formPort) Load(ctx context.Context, identity core.DocumentIdentity, locale core.Locale) (core.Document, error) {
	if err := validateFormDocumentIdentity(identity); err != nil {
		return core.Document{}, err
	}
	state, err := p.service.LoadAIDocumentState(ctx, string(identity.Reference), string(locale))
	if err != nil {
		return core.Document{}, err
	}
	return p.document(identity, locale, state)
}

func (p *formPort) ValidateMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	validation, _, err := p.executeMutation(ctx, request, formdomain.AIDocumentExecutionValidate)
	return validation, err
}

func (p *formPort) ExecuteMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	_, result, err := p.executeMutation(ctx, request, formdomain.AIDocumentExecutionApply)
	return result, err
}

func (p *formPort) executeMutation(
	ctx context.Context,
	request core.ApplyRequest,
	mode formdomain.AIDocumentExecutionMode,
) (core.ValidationResult, core.ApplyResult, error) {
	identity := request.Identity()
	if err := validateFormDocumentIdentity(identity); err != nil {
		return core.ValidationResult{}, core.ApplyResult{}, err
	}

	run := newExactMutationRun("Form")
	domainResult, err := p.service.ExecuteAIDocumentMutation(
		ctx,
		string(identity.Reference),
		string(request.Locale),
		mode,
		func(state formdomain.AIDocumentState) (formdomain.AIDocumentMutation, error) {
			current, err := p.document(identity, request.Locale, state)
			if err != nil {
				return formdomain.AIDocumentMutation{}, err
			}
			if err := run.validateLoaded(current, request); err != nil {
				return formdomain.AIDocumentMutation{}, err
			}
			contributor, err := canonicalFormContributor(state.ViewerMemberID)
			if err != nil {
				return formdomain.AIDocumentMutation{}, err
			}
			mutation, issues, err := p.compile(state, current, contributor, run.command.Operations)
			if err != nil {
				return formdomain.AIDocumentMutation{}, err
			}
			if err := run.rejectIssues(issues); err != nil {
				return formdomain.AIDocumentMutation{}, err
			}
			return mutation, nil
		},
	)
	if err != nil {
		if invalid, mapped, handled := handleExactMutationValidationError(
			err, mode == formdomain.AIDocumentExecutionValidate,
		); handled {
			return invalid, core.ApplyResult{}, mapped
		}
		var conflict *formdomain.AIDocumentRevisionConflictError
		if errors.As(err, &conflict) {
			return core.ValidationResult{}, core.ApplyResult{}, formConflict(conflict, run.command.AffectedHandles)
		}
		return core.ValidationResult{}, core.ApplyResult{}, err
	}
	if mode == formdomain.AIDocumentExecutionValidate {
		return run.validation, core.ApplyResult{}, nil
	}

	output := core.ApplyResult{
		DocumentRevision: core.Revision(domainResult.DocumentRevision),
		TargetRevision:   formCoreRevision(domainResult.TargetRevision),
		Changed:          domainResult.Changed,
	}
	if domainResult.Changed {
		for index, operation := range run.command.Operations {
			output.Changes = append(output.Changes, core.Change{
				Operation: index, Kind: operation.Kind,
				AffectedHandles: compactOperationHandles(operation, run.command.Locale, "form"),
			})
		}
	}
	accepted, err := run.accept(output)
	return run.validation, accepted, err
}

func (p *formPort) document(identity core.DocumentIdentity, locale core.Locale, state formdomain.AIDocumentState) (core.Document, error) {
	if state.FormID != string(identity.Reference) || state.Locale != string(locale) {
		return core.Document{}, errors.New("form AI document facade returned inconsistent state")
	}
	nodes, err := projectFormNodes(state)
	if err != nil {
		return core.Document{}, fmt.Errorf("project Form document: %w", err)
	}
	return core.Document{
		Identity: identity, DocumentRevision: core.Revision(state.DocumentRevision),
		TargetRevision: formCoreRevision(state.TargetRevision), SourceLocale: core.Locale(state.SourceLocale),
		Locale: locale, LocaleExists: state.LocaleExists, Catalog: p.catalog, Nodes: nodes,
	}, nil
}

func canonicalFormContributor(member string) (string, error) {
	member = strings.TrimSpace(member)
	parsed, err := uuid.Parse(member)
	if err != nil || parsed == uuid.Nil || parsed.String() != member {
		return "", errors.New("form contributor Member must be a canonical UUID")
	}
	return member, nil
}

func (p *formPort) compile(state formdomain.AIDocumentState, document core.Document, contributor string, operations []core.Operation) (formdomain.AIDocumentMutation, []core.OperationIssue, error) {
	mutation := formdomain.AIDocumentMutation{
		FormID: state.FormID, Locale: string(document.Locale),
		ExpectedDocumentRevision: string(document.DocumentRevision),
		ExpectedTargetRevision:   formStringRevision(document.TargetRevision),
		ExpectedSource:           state.SourceLocale, ExpectedPresence: state.LocaleExists,
		ContributorMemberID: contributor,
	}
	if len(operations) == 1 && operations[0].Kind == core.OperationCreateTranslation {
		mutation.CreateTranslation = true
		return mutation, nil, nil
	}
	if len(operations) == 1 && operations[0].Kind == core.OperationDeleteTranslation {
		mutation.DeleteTranslation = true
		return mutation, nil, nil
	}
	for index, operation := range operations {
		if operation.Kind == core.OperationUnsetField && operation.UnsetField != nil {
			return formdomain.AIDocumentMutation{}, []core.OperationIssue{{Operation: index, Code: core.IssueInvalidOperation, Handle: strings.Join(compactOperationHandles(operation, document.Locale, "form"), "/"), Message: "Form locale copy uses explicit empty instead of unset"}}, nil
		}
		if formOperationMutatesRoot(operation) {
			return formdomain.AIDocumentMutation{}, []core.OperationIssue{{Operation: index, Code: core.IssueInvalidOperation, Handle: "block:document", Message: "Form document root cannot be structurally changed"}}, nil
		}
	}
	next, err := core.DocumentAfterOperations(document, operations)
	if err != nil {
		return formdomain.AIDocumentMutation{}, nil, err
	}
	title, titlePresent, err := formDocumentTitle(next)
	if err != nil {
		return formdomain.AIDocumentMutation{}, nil, err
	}
	schema, err := formDocumentSchema(next)
	if err != nil {
		return formdomain.AIDocumentMutation{}, nil, err
	}
	currentTitle, currentTitlePresent, err := formDocumentTitle(document)
	if err != nil {
		return formdomain.AIDocumentMutation{}, nil, err
	}
	currentSchema, err := formDocumentSchema(document)
	if err != nil {
		return formdomain.AIDocumentMutation{}, nil, err
	}
	mutation.SetTitle = titlePresent != currentTitlePresent || title != currentTitle
	if mutation.SetTitle {
		mutation.Title = &title
	}
	mutation.SetSchema = !bytes.Equal(schema, currentSchema)
	if mutation.SetSchema {
		mutation.Schema = schema
	}
	if !mutation.SetTitle && !mutation.SetSchema {
		mutation.Noop = true
	}
	return mutation, nil, nil
}

func formOperationMutatesRoot(operation core.Operation) bool {
	switch operation.Kind {
	case core.OperationDeleteBlock:
		return operation.DeleteBlock.Block == formRootID
	case core.OperationMoveBlock:
		return operation.MoveBlock.Block == formRootID
	case core.OperationReplaceBlockKind:
		return operation.ReplaceBlockKind.Block == formRootID
	default:
		return false
	}
}

func validateFormDocumentIdentity(identity core.DocumentIdentity) error {
	if identity.Domain != core.DomainForm {
		return fmt.Errorf("form AI document requires domain %q", core.DomainForm)
	}
	value := string(identity.Reference)
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || id.String() != value {
		return errors.New("form AI document reference must be a canonical UUID")
	}
	return nil
}
func formConflict(conflict *formdomain.AIDocumentRevisionConflictError, handles []string) error {
	code := core.ConflictDocumentRevision
	if conflict.Kind == formdomain.AIDocumentTargetRevisionConflict {
		code = core.ConflictTargetRevision
	}
	return &core.ConflictError{Conflict: core.Conflict{
		Code: code, CurrentDocumentRevision: core.Revision(conflict.CurrentDocumentRevision),
		CurrentTargetRevision: formCoreRevision(conflict.CurrentTargetRevision),
		AffectedHandles:       append([]string(nil), handles...),
	}}
}

func formCoreRevision(value *string) *core.Revision {
	if value == nil {
		return nil
	}
	revision := core.Revision(*value)
	return &revision
}

func formStringRevision(value *core.Revision) *string {
	if value == nil {
		return nil
	}
	revision := string(*value)
	return &revision
}

func projectFormNodes(state formdomain.AIDocumentState) ([]core.Node, error) {
	if !state.LocaleExists && len(state.LocaleSchema) != 0 {
		return nil, errors.New("missing Form locale cannot carry a persisted schema")
	}
	if err := formdomain.ValidateAIDocumentAuthoringSchemas(state.SourceSchema, state.LocaleSchema); err != nil {
		return nil, err
	}
	source, err := decodeFormSchema(state.SourceSchema)
	if err != nil {
		return nil, err
	}
	var localized map[string]any
	if state.LocaleExists && len(state.LocaleSchema) != 0 {
		localized, err = decodeFormSchema(state.LocaleSchema)
		if err != nil {
			return nil, err
		}
	}
	root := core.Node{ID: formRootID, Kind: formRootKind, Shared: []core.FieldValue{{ID: formSchemaIDField, Value: core.Text(formString(source, "id"))}}}
	title := state.LocaleTitle
	if state.Locale == state.SourceLocale {
		title = state.SourceTitle
	}
	if title != nil {
		root.Localized = []core.FieldValue{{ID: formRootTitleField, Value: core.Text(*title)}}
	}
	nodes := []core.Node{root}
	targetSteps := indexFormObjects(formArray(localized, "steps"), "id")
	for stepIndex, sourceStep := range formArray(source, "steps") {
		stableID := formString(sourceStep, "id")
		handle, err := formintrav1.FormStepBlockHandle(stableID)
		if err != nil {
			return nil, fmt.Errorf("form step %q: %w", stableID, err)
		}
		stepID := core.BlockID(handle)
		targetStep := targetSteps[stableID]
		step := core.Node{ID: stepID, Kind: formStepKind, Parent: formRootID, Order: stepIndex, Shared: []core.FieldValue{{ID: formRawField, Value: core.Text(formRaw(sourceStep, "fields", "title", "description"))}}}
		step.Localized = formLocalizedFields(targetOrSource(targetStep, sourceStep, state.Locale == state.SourceLocale), map[core.FieldID]string{formStepTitleField: "title", formStepDescriptionField: "description"})
		nodes = append(nodes, step)
		targetFields := indexFormObjects(formArray(targetStep, "fields"), "id")
		for fieldIndex, sourceField := range formArray(sourceStep, "fields") {
			stableID := formString(sourceField, "id")
			handle, err := formintrav1.FormFieldBlockHandle(stableID)
			if err != nil {
				return nil, fmt.Errorf("form field %q: %w", stableID, err)
			}
			fieldID := core.BlockID(handle)
			targetField := targetFields[stableID]
			field := core.Node{ID: fieldID, Kind: formFieldKind, Parent: stepID, Order: fieldIndex, Shared: []core.FieldValue{{ID: formRawField, Value: core.Text(formFieldSourceRaw(sourceField))}}}
			field.Localized = formLocalizedFields(targetOrSource(targetField, sourceField, state.Locale == state.SourceLocale), map[core.FieldID]string{formFieldLabelField: "label", formFieldDescriptionField: "description", formFieldPlaceholderField: "placeholder", formFieldCheckboxLabelField: "checkboxLabel"})
			nodes = append(nodes, field)
			targetOptions := indexFormObjects(formArray(targetField, "options"), "id")
			for optionIndex, sourceOption := range formArray(sourceField, "options") {
				stableID := formString(sourceOption, "id")
				handle, err := formintrav1.FormOptionBlockHandle(stableID)
				if err != nil {
					return nil, fmt.Errorf("form option %q: %w", stableID, err)
				}
				option := core.Node{ID: core.BlockID(handle), Kind: formOptionKind, Parent: fieldID, Order: optionIndex, Shared: []core.FieldValue{{ID: formRawField, Value: core.Text(formRaw(sourceOption, "label"))}}}
				option.Localized = formLocalizedFields(targetOrSource(targetOptions[stableID], sourceOption, state.Locale == state.SourceLocale), map[core.FieldID]string{formOptionLabelField: "label"})
				nodes = append(nodes, option)
			}
			sourceValidation := formObject(sourceField, "validation")
			targetValidation := formObject(targetField, "validation")
			targetValidators := indexFormObjects(formArray(targetValidation, "validators"), "id")
			for validatorIndex, sourceValidator := range formArray(sourceValidation, "validators") {
				stableID := formString(sourceValidator, "id")
				handle, err := formintrav1.FormValidatorBlockHandle(stableID)
				if err != nil {
					return nil, fmt.Errorf("form validator %q: %w", stableID, err)
				}
				validator := core.Node{ID: core.BlockID(handle), Kind: formValidatorKind, Parent: fieldID, Order: len(formArray(sourceField, "options")) + validatorIndex, Shared: []core.FieldValue{{ID: formRawField, Value: core.Text(formRaw(sourceValidator, "message"))}}}
				validator.Localized = formLocalizedFields(targetOrSource(targetValidators[stableID], sourceValidator, state.Locale == state.SourceLocale), map[core.FieldID]string{formValidatorMessageField: "message"})
				nodes = append(nodes, validator)
			}
		}
	}
	return nodes, nil
}

func formDocumentTitle(document core.Document) (string, bool, error) {
	node := formNode(document.Nodes, formRootID)
	if node == nil || node.Kind != formRootKind {
		return "", false, errors.New("form root is missing")
	}
	value, ok := formField(node.Localized, formRootTitleField)
	if !ok {
		return "", false, nil
	}
	if value.Kind != core.ValueKindText {
		return "", false, errors.New("form title must be text")
	}
	return value.Text, true, nil
}

func formDocumentSchema(document core.Document) ([]byte, error) {
	if err := validateFormDocumentGraph(document.Nodes); err != nil {
		return nil, err
	}
	root := formNode(document.Nodes, formRootID)
	if root == nil || root.Kind != formRootKind {
		return nil, errors.New("form root is missing")
	}
	schemaID, ok := formField(root.Shared, formSchemaIDField)
	if !ok || schemaID.Kind != core.ValueKindText || strings.TrimSpace(schemaID.Text) == "" {
		return nil, errors.New("form schema identity is required")
	}
	schema := map[string]any{"id": schemaID.Text}
	steps := make([]any, 0)
	for _, stepNode := range formChildren(document.Nodes, formRootID, formStepKind) {
		step, err := formObjectFromNode(stepNode)
		if err != nil {
			return nil, err
		}
		formApplyLocaleFields(step, stepNode.Localized, map[core.FieldID]string{formStepTitleField: "title", formStepDescriptionField: "description"})
		fields := make([]any, 0)
		for _, fieldNode := range formChildren(document.Nodes, stepNode.ID, formFieldKind) {
			field, err := formObjectFromNode(fieldNode)
			if err != nil {
				return nil, err
			}
			formApplyLocaleFields(field, fieldNode.Localized, map[core.FieldID]string{formFieldLabelField: "label", formFieldDescriptionField: "description", formFieldPlaceholderField: "placeholder", formFieldCheckboxLabelField: "checkboxLabel"})
			options := make([]any, 0)
			validators := make([]any, 0)
			for _, child := range formChildrenAny(document.Nodes, fieldNode.ID) {
				object, err := formObjectFromNode(child)
				if err != nil {
					return nil, err
				}
				switch child.Kind {
				case formOptionKind:
					formApplyLocaleFields(object, child.Localized, map[core.FieldID]string{formOptionLabelField: "label"})
					options = append(options, object)
				case formValidatorKind:
					formApplyLocaleFields(object, child.Localized, map[core.FieldID]string{formValidatorMessageField: "message"})
					validators = append(validators, object)
				default:
					return nil, fmt.Errorf("form field has invalid child kind %q", child.Kind)
				}
			}
			if len(options) != 0 {
				field["options"] = options
			}
			validation, _ := field["validation"].(map[string]any)
			if validation == nil {
				validation = map[string]any{}
			}
			if len(validators) != 0 {
				validation["validators"] = validators
			}
			if len(validation) != 0 {
				field["validation"] = validation
			}
			fields = append(fields, field)
		}
		step["fields"] = fields
		steps = append(steps, step)
	}
	schema["steps"] = steps
	return json.Marshal(schema)
}

func validateFormDocumentGraph(nodes []core.Node) error {
	kinds := make(map[core.BlockID]core.BlockKind, len(nodes))
	rootCount := 0
	for _, node := range nodes {
		if _, duplicate := kinds[node.ID]; duplicate {
			return fmt.Errorf("duplicate Form block %q", node.ID)
		}
		kinds[node.ID] = node.Kind
		if node.ID == formRootID {
			rootCount++
			if node.Kind != formRootKind || node.Parent != "" {
				return errors.New("form root identity, kind, or parent is invalid")
			}
		}
	}
	if rootCount != 1 {
		return errors.New("form requires exactly one document root")
	}
	for _, node := range nodes {
		if node.ID == formRootID {
			continue
		}
		parentKind, exists := kinds[node.Parent]
		if !exists {
			return fmt.Errorf("form block %q has no parent", node.ID)
		}
		switch node.Kind {
		case formStepKind:
			if node.Parent != formRootID {
				return fmt.Errorf("form step %q must belong to the document root", node.ID)
			}
		case formFieldKind:
			if parentKind != formStepKind {
				return fmt.Errorf("form field %q must belong to a step", node.ID)
			}
		case formOptionKind, formValidatorKind:
			if parentKind != formFieldKind {
				return fmt.Errorf("form child %q must belong to a field", node.ID)
			}
		default:
			return fmt.Errorf("unsupported Form block kind %q", node.Kind)
		}
	}
	return nil
}

func decodeFormSchema(body []byte) (map[string]any, error) {
	var value map[string]any
	if len(body) == 0 {
		return nil, errors.New("form schema is required")
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, err
	}
	return value, nil
}
func formString(value map[string]any, key string) string {
	if value == nil {
		return ""
	}
	text, _ := value[key].(string)
	return text
}
func formArray(value map[string]any, key string) []map[string]any {
	if value == nil {
		return nil
	}
	raw, _ := value[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if object, ok := item.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}
func formObject(value map[string]any, key string) map[string]any {
	if value == nil {
		return nil
	}
	object, _ := value[key].(map[string]any)
	return object
}
func formRaw(value map[string]any, omitted ...string) string {
	copy := make(map[string]any, len(value))
	for key, nested := range value {
		copy[key] = nested
	}
	for _, key := range omitted {
		delete(copy, key)
	}
	body, _ := json.Marshal(copy)
	return string(body)
}

func formFieldSourceRaw(value map[string]any) string {
	copy := make(map[string]any, len(value))
	for key, nested := range value {
		copy[key] = nested
	}
	for _, key := range []string{"label", "description", "placeholder", "checkboxLabel"} {
		delete(copy, key)
	}
	if options, exists := copy["options"].([]any); exists && len(options) != 0 {
		delete(copy, "options")
	}
	if validation, ok := copy["validation"].(map[string]any); ok {
		validationCopy := make(map[string]any, len(validation))
		for key, nested := range validation {
			if key != "validators" {
				validationCopy[key] = nested
			}
		}
		copy["validation"] = validationCopy
	}
	body, _ := json.Marshal(copy)
	return string(body)
}
func targetOrSource(target, source map[string]any, useSource bool) map[string]any {
	if useSource {
		return source
	}
	return target
}
func formLocalizedFields(value map[string]any, fields map[core.FieldID]string) []core.FieldValue {
	if value == nil {
		return nil
	}
	ids := make([]string, 0, len(fields))
	for id := range fields {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	out := make([]core.FieldValue, 0, len(ids))
	for _, rawID := range ids {
		id := core.FieldID(rawID)
		key := fields[id]
		raw, exists := value[key]
		if !exists {
			continue
		}
		text, ok := raw.(string)
		if ok {
			out = append(out, core.FieldValue{ID: id, Value: core.Text(text)})
		}
	}
	return out
}
func indexFormObjects(values []map[string]any, key string) map[string]map[string]any {
	return indexFormObjectsByIdentity(values, func(value map[string]any) string { return formString(value, key) })
}
func indexFormObjectsByIdentity(values []map[string]any, identity func(map[string]any) string) map[string]map[string]any {
	out := make(map[string]map[string]any, len(values))
	for _, value := range values {
		out[identity(value)] = value
	}
	return out
}
func formNode(nodes []core.Node, id core.BlockID) *core.Node {
	for index := range nodes {
		if nodes[index].ID == id {
			return &nodes[index]
		}
	}
	return nil
}
func formField(values []core.FieldValue, id core.FieldID) (core.Value, bool) {
	for _, value := range values {
		if value.ID == id {
			return value.Value, true
		}
	}
	return core.Value{}, false
}
func formChildren(nodes []core.Node, parent core.BlockID, kind core.BlockKind) []core.Node {
	children := formChildrenAny(nodes, parent)
	out := children[:0]
	for _, child := range children {
		if child.Kind == kind {
			out = append(out, child)
		} else {
			return nil
		}
	}
	return out
}
func formChildrenAny(nodes []core.Node, parent core.BlockID) []core.Node {
	out := make([]core.Node, 0)
	for _, node := range nodes {
		if node.Parent == parent {
			out = append(out, node)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order == out[j].Order {
			return out[i].ID < out[j].ID
		}
		return out[i].Order < out[j].Order
	})
	return out
}
func formObjectFromNode(node core.Node) (map[string]any, error) {
	raw, ok := formField(node.Shared, formRawField)
	if !ok || raw.Kind != core.ValueKindText {
		return nil, fmt.Errorf("form %s source payload is required", node.ID)
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(raw.Text), &object); err != nil {
		return nil, fmt.Errorf("form %s source payload: %w", node.ID, err)
	}
	return object, nil
}
func formApplyLocaleFields(object map[string]any, values []core.FieldValue, fields map[core.FieldID]string) {
	for id, key := range fields {
		if value, ok := formField(values, id); ok && value.Kind == core.ValueKindText {
			object[key] = value.Text
		}
	}
}

var _ core.DomainPort = (*formPort)(nil)
var _ core.ExactMutationPort = (*formPort)(nil)
