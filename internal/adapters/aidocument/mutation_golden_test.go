package aidocumentadapter

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
)

// Fixed identities keep every mutation golden byte-stable.
const (
	goldenEntityID                = "0e000000-0000-4000-8000-000000000001"
	goldenDocumentID              = "0d000000-0000-4000-8000-000000000001"
	goldenBlockID                 = "0b000000-0000-4000-8000-000000000001"
	goldenSecondBlockID           = "0b000000-0000-4000-8000-000000000002"
	goldenMemberID                = "0c000000-0000-4000-8000-000000000001"
	goldenFileID                  = "0f000000-0000-4000-8000-000000000001"
	goldenDocumentRevision        = "0a000000-0000-4000-8000-000000000001"
	goldenNextDocumentRevision    = "0a000000-0000-4000-8000-000000000002"
	goldenCurrentDocumentRevision = "0a000000-0000-4000-8000-000000000003"
	goldenTargetRevision          = "tr1_golden-loaded"
	goldenNextTargetRevision      = "tr1_golden-next"
	goldenCurrentTargetRevision   = "tr1_golden-current"
)

// mutationGoldenDomainResult is the domain-neutral persisted outcome a
// subject converts into its owning domain's result type.
type mutationGoldenDomainResult struct {
	DocumentRevision string
	TargetRevision   *string
	Changed          bool
}

// mutationGoldenSubject adapts one exact-mutation port and its test fake to
// the shared scenario table, so every adapter is pinned with the same inputs.
type mutationGoldenSubject struct {
	name                       string
	domain                     core.Domain
	reference                  core.DocumentReference
	sourceLocale, targetLocale core.Locale
	service                    *core.Service
	handles                    func(core.Operation) []string
	// arrange switches the fake to the source or target locale state and sets
	// what the owning domain returns after the adapter compiler accepted.
	arrange   func(target bool, result mutationGoldenDomainResult, err error)
	conflict  func(target bool) error
	sourceOps []core.Operation
	targetOps []core.Operation
	// structureOps is a source batch of graph operations, when the domain
	// profile supports them.
	structureOps []core.Operation
	// extra holds domain-specific scenarios appended after the shared ones.
	extra []mutationGoldenScenarioInput
}

type mutationGoldenScenarioInput struct {
	name       string
	target     bool
	operations []core.Operation
	result     mutationGoldenDomainResult
	err        error
}

type mutationGolden struct {
	Domain           string                   `json:"domain"`
	OperationHandles []mutationGoldenHandles  `json:"operation_handles"`
	Scenarios        []mutationGoldenScenario `json:"scenarios"`
}

type mutationGoldenHandles struct {
	Operation string   `json:"operation"`
	Kind      string   `json:"kind"`
	Handles   []string `json:"handles"`
}

type mutationGoldenScenario struct {
	Name        string                `json:"name"`
	Locale      string                `json:"locale"`
	Operations  []string              `json:"operations"`
	DomainError string                `json:"domain_error,omitempty"`
	Validate    mutationGoldenOutcome `json:"validate"`
	Apply       mutationGoldenOutcome `json:"apply"`
}

type mutationGoldenOutcome struct {
	Validation *mutationGoldenValidation `json:"validation,omitempty"`
	Result     *mutationGoldenResult     `json:"result,omitempty"`
	Error      *mutationGoldenError      `json:"error,omitempty"`
}

type mutationGoldenValidation struct {
	Valid      bool                  `json:"valid"`
	Normalized []string              `json:"normalized,omitempty"`
	Issues     []core.OperationIssue `json:"issues,omitempty"`
	Conflict   *core.Conflict        `json:"conflict,omitempty"`
}

type mutationGoldenResult struct {
	DocumentRevision string                 `json:"document_revision"`
	TargetRevision   *core.Revision         `json:"target_revision,omitempty"`
	Changed          bool                   `json:"changed"`
	Changes          []mutationGoldenChange `json:"changes"`
	Normalized       []string               `json:"normalized"`
}

type mutationGoldenChange struct {
	Operation       int      `json:"operation"`
	Kind            string   `json:"kind"`
	AffectedHandles []string `json:"affected_handles"`
}

type mutationGoldenError struct {
	Type        string                `json:"type"`
	Message     string                `json:"message"`
	ConnectCode string                `json:"connect_code,omitempty"`
	RawDomain   bool                  `json:"raw_domain_error,omitempty"`
	Conflict    *core.Conflict        `json:"conflict,omitempty"`
	Issues      []core.OperationIssue `json:"issues,omitempty"`
}

var (
	errGoldenInvalidMutation = fmt.Errorf("golden domain rejection: %w", contentblock.ErrInvalidMutation)
	errGoldenFileReference   = fmt.Errorf("golden File rejection: %w", contentblock.ErrFileReference)
	errGoldenInvalidArgument = errs.InvalidArgument("batch", "golden domain rejection")
)

func runMutationGolden(t *testing.T, subject mutationGoldenSubject) {
	t.Helper()
	sourceChanged := mutationGoldenDomainResult{DocumentRevision: goldenNextDocumentRevision, Changed: true}
	nextTarget := goldenNextTargetRevision
	targetChanged := mutationGoldenDomainResult{
		DocumentRevision: goldenDocumentRevision, TargetRevision: &nextTarget, Changed: true,
	}
	unchanged := mutationGoldenDomainResult{DocumentRevision: goldenDocumentRevision}

	inputs := []mutationGoldenScenarioInput{
		{name: "source edit", operations: subject.sourceOps, result: sourceChanged},
		{name: "source edit unchanged", operations: subject.sourceOps, result: unchanged},
	}
	if len(subject.structureOps) != 0 {
		inputs = append(inputs, mutationGoldenScenarioInput{
			name: "source structure", operations: subject.structureOps, result: sourceChanged,
		})
	}
	inputs = append(inputs,
		mutationGoldenScenarioInput{name: "target edit", target: true, operations: subject.targetOps, result: targetChanged},
		mutationGoldenScenarioInput{name: "document revision conflict", operations: subject.sourceOps, err: subject.conflict(false)},
		mutationGoldenScenarioInput{name: "target revision conflict", target: true, operations: subject.targetOps, err: subject.conflict(true)},
		mutationGoldenScenarioInput{name: "domain invalid mutation error", operations: subject.sourceOps, err: errGoldenInvalidMutation},
		mutationGoldenScenarioInput{name: "domain File reference error", operations: subject.sourceOps, err: errGoldenFileReference},
		mutationGoldenScenarioInput{name: "domain InvalidArgument error", operations: subject.sourceOps, err: errGoldenInvalidArgument},
	)
	inputs = append(inputs, subject.extra...)

	golden := mutationGolden{Domain: subject.name, OperationHandles: mutationGoldenHandleTable(subject.handles)}
	for _, input := range inputs {
		if input.result == (mutationGoldenDomainResult{}) {
			input.result = unchanged
		}
		request := subject.request(input.target, input.operations...)
		scenario := mutationGoldenScenario{
			Name: input.name, Locale: string(request.Locale), Operations: mutationGoldenKinds(input.operations),
		}
		if input.err != nil {
			scenario.DomainError = fmt.Sprintf("%T: %v", input.err, input.err)
		}
		subject.arrange(input.target, input.result, input.err)
		validation, err := subject.service.Validate(t.Context(), request)
		scenario.Validate = mutationGoldenValidateOutcome(validation, err, input.err)
		subject.arrange(input.target, input.result, input.err)
		result, err := subject.service.Apply(t.Context(), request)
		scenario.Apply = mutationGoldenApplyOutcome(result, err, input.err)
		golden.Scenarios = append(golden.Scenarios, scenario)
	}
	assertGoldenJSON(t, "mutation/"+subject.name+".golden.json", golden)
}

func (subject mutationGoldenSubject) request(target bool, operations ...core.Operation) core.ApplyRequest {
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: subject.domain, Document: subject.reference,
		Locale: subject.sourceLocale, ExpectedDocumentRevision: goldenDocumentRevision, Operations: operations,
	}
	if target {
		revision := core.Revision(goldenTargetRevision)
		request.Locale, request.ExpectedTargetRevision = subject.targetLocale, &revision
	}
	return request
}

// mutationGoldenHandleTable feeds one operation of every kind to a domain's
// change-handle projection, including kinds the domain profile rejects, so
// every handle dialect is visible side by side.
func mutationGoldenHandleTable(handles func(core.Operation) []string) []mutationGoldenHandles {
	operations := []struct {
		name      string
		operation core.Operation
	}{
		{"set field", core.SetFieldOperation("block-a", "field-a", core.Text("value"))},
		{"set nested field", core.SetNestedFieldOperation("block-a", "field-a", []core.FieldPathSegment{core.ObjectPath("nested")}, core.Text("value"))},
		{"set relation field", core.SetRelationFieldOperation("block-a", "relation-a", "item-a", "field-a", core.Text("value"))},
		{"unset field", core.UnsetFieldOperation("block-a", "field-a")},
		{"insert block", core.InsertBlockOperation("block-b", "paragraph", "", "block-a")},
		{"delete block", core.DeleteBlockOperation("block-a")},
		{"move block", core.MoveBlockOperation("block-a", "parent-a", "block-b")},
		{"replace block kind", core.ReplaceBlockKindOperation("block-a", "heading")},
		{"insert relation item", core.InsertRelationItemOperation("block-a", "relation-a", "item-b", "kind-a", "item-a")},
		{"delete relation item", core.DeleteRelationItemOperation("block-a", "relation-a", "item-a")},
		{"move relation item", core.MoveRelationItemOperation("block-a", "relation-a", "item-a", "block-b", "relation-b", "")},
		{"attach file", core.AttachFileOperation("block-a", "file-field", core.FileReference(goldenFileID))},
		{"detach file", core.DetachFileOperation("block-a", "file-field")},
		{"create translation", core.CreateTranslationOperation()},
		{"delete translation", core.DeleteTranslationOperation()},
	}
	table := make([]mutationGoldenHandles, 0, len(operations))
	for _, entry := range operations {
		table = append(table, mutationGoldenHandles{
			Operation: entry.name, Kind: string(entry.operation.Kind), Handles: handles(entry.operation),
		})
	}
	return table
}

func mutationGoldenKinds(operations []core.Operation) []string {
	kinds := make([]string, 0, len(operations))
	for _, operation := range operations {
		kinds = append(kinds, string(operation.Kind))
	}
	return kinds
}

func mutationGoldenValidateOutcome(validation core.ValidationResult, err, injected error) mutationGoldenOutcome {
	if err != nil {
		return mutationGoldenOutcome{Error: mutationGoldenDescribeError(err, injected)}
	}
	return mutationGoldenOutcome{Validation: &mutationGoldenValidation{
		Valid: validation.Valid(), Normalized: mutationGoldenKinds(validation.Normalized),
		Issues: validation.Issues, Conflict: validation.Conflict,
	}}
}

func mutationGoldenApplyOutcome(result core.ApplyResult, err, injected error) mutationGoldenOutcome {
	if err != nil {
		return mutationGoldenOutcome{Error: mutationGoldenDescribeError(err, injected)}
	}
	described := &mutationGoldenResult{
		DocumentRevision: string(result.DocumentRevision), TargetRevision: result.TargetRevision,
		Changed: result.Changed, Changes: []mutationGoldenChange{}, Normalized: mutationGoldenKinds(result.Normalized),
	}
	for _, change := range result.Changes {
		described.Changes = append(described.Changes, mutationGoldenChange{
			Operation: change.Operation, Kind: string(change.Kind), AffectedHandles: change.AffectedHandles,
		})
	}
	return mutationGoldenOutcome{Result: described}
}

func mutationGoldenDescribeError(err, injected error) *mutationGoldenError {
	described := &mutationGoldenError{Type: fmt.Sprintf("%T", err), Message: err.Error()}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		described.ConnectCode = connectErr.Code().String()
	}
	described.RawDomain = injected != nil && errors.Is(err, injected)
	var conflict *core.ConflictError
	if errors.As(err, &conflict) {
		described.Conflict = &conflict.Conflict
	}
	var invalid *core.ValidationError
	if errors.As(err, &invalid) {
		described.Issues = invalid.Result.Issues
		if invalid.Result.Conflict != nil {
			described.Conflict = invalid.Result.Conflict
		}
	}
	return described
}

func goldenStringPointer(value string) *string { return &value }
