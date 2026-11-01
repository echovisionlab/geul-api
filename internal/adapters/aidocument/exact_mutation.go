package aidocumentadapter

import (
	"errors"
	"fmt"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

type exactMutationSpec[S, M, R any] struct {
	Domain       string
	ValidateOnly bool
	// Execute supplies authorized state under the owning domain's root lock.
	Execute func(func(S) (M, error)) (R, error)
	Project func(S) (core.Document, error)
	Compile func(S, core.Document, core.ValidatedApply) (M, []core.OperationIssue, error)
	Result  func(R, core.ValidatedApply) (core.ApplyResult, error)

	ConflictHandles   func(*core.RevisionConflict, core.ValidatedApply) []string
	PreConflictIssues func(error) ([]core.OperationIssue, bool)
	DomainIssue       func(error, []core.Operation) *core.OperationIssue
}

func withOperationChanges(command core.ValidatedApply, result core.ApplyResult) core.ApplyResult {
	if result.Changed {
		for index, operation := range command.Operations {
			result.Changes = append(result.Changes, core.Change{
				Operation: index, Kind: operation.Kind, AffectedHandles: core.OperationHandles(operation, command.Locale),
			})
		}
	}
	return result
}

func executeExactMutation[S, M, R any](request core.ApplyRequest, spec exactMutationSpec[S, M, R]) (core.ValidationResult, core.ApplyResult, error) {
	run := newExactMutationRun(spec.Domain)
	result, err := spec.Execute(func(state S) (M, error) {
		var zero M
		document, err := spec.Project(state)
		if err != nil {
			return zero, err
		}
		if err := run.validateLoaded(document, request); err != nil {
			return zero, err
		}
		compiled, issues, err := spec.Compile(state, document, run.command)
		if err != nil {
			return zero, err
		}
		if err := run.rejectIssues(issues); err != nil {
			return zero, err
		}
		return compiled, nil
	})
	if err != nil {
		return exactMutationFailure(run, request, spec, err)
	}
	if spec.ValidateOnly {
		return run.validation, core.ApplyResult{}, nil
	}
	output, err := spec.Result(result, run.command)
	if err != nil {
		return core.ValidationResult{}, core.ApplyResult{}, err
	}
	accepted, err := run.accept(output)
	return run.validation, accepted, err
}

func exactMutationFailure[S, M, R any](run *exactMutationRun, request core.ApplyRequest, spec exactMutationSpec[S, M, R], err error) (core.ValidationResult, core.ApplyResult, error) {
	if invalid, mapped, handled := handleExactMutationValidationError(err, spec.ValidateOnly); handled {
		return invalid, core.ApplyResult{}, mapped
	}
	if spec.PreConflictIssues != nil {
		if issues, handled := spec.PreConflictIssues(err); handled {
			return run.domainIssues(issues, spec.ValidateOnly)
		}
	}
	var conflict *core.RevisionConflict
	if errors.As(err, &conflict) {
		handles := run.command.AffectedHandles
		if spec.ConflictHandles != nil {
			handles = spec.ConflictHandles(conflict, run.command)
		}
		code := core.ConflictDocumentRevision
		if conflict.Kind == core.ConflictTargetRevision {
			code = core.ConflictTargetRevision
		}
		return core.ValidationResult{}, core.ApplyResult{}, &core.ConflictError{Conflict: core.Conflict{
			Code: code, CurrentDocumentRevision: core.Revision(conflict.CurrentDocumentRevision),
			CurrentTargetRevision: core.OptionalRevision(conflict.CurrentTargetRevision),
			AffectedHandles:       append([]string(nil), handles...),
		}}
	}
	if spec.DomainIssue != nil {
		operations := run.command.Operations
		if len(operations) == 0 {
			operations = request.Operations
		}
		if issue := spec.DomainIssue(err, operations); issue != nil {
			return run.domainIssues([]core.OperationIssue{*issue}, spec.ValidateOnly)
		}
	}
	return core.ValidationResult{}, core.ApplyResult{}, err
}

func (run *exactMutationRun) domainIssues(issues []core.OperationIssue, validateOnly bool) (core.ValidationResult, core.ApplyResult, error) {
	run.validation.Issues = append(run.validation.Issues, issues...)
	if validateOnly {
		return run.validation, core.ApplyResult{}, nil
	}
	return core.ValidationResult{}, core.ApplyResult{}, &core.ValidationError{Result: run.validation}
}

// exactMutationRun keeps validation and normalized operations for one
// domain-owned compiler invocation.
type exactMutationRun struct {
	domain     string
	command    core.ValidatedApply
	validation core.ValidationResult
}

func newExactMutationRun(domain string) *exactMutationRun {
	return &exactMutationRun{domain: domain}
}

func (run *exactMutationRun) validateLoaded(
	document core.Document,
	request core.ApplyRequest,
) error {
	run.command, run.validation = core.ValidateLoadedApply(document, request)
	if run.validation.Valid() {
		return nil
	}
	return rejectExactMutation(run.domain, run.validation)
}

func (run *exactMutationRun) rejectIssues(issues []core.OperationIssue) error {
	if len(issues) == 0 {
		return nil
	}
	run.validation.Issues = append(run.validation.Issues, issues...)
	return rejectExactMutation(run.domain, run.validation)
}

func (run *exactMutationRun) accept(result core.ApplyResult) (core.ApplyResult, error) {
	return core.AcceptValidatedApply(run.command, result)
}

// exactMutationValidationError is the adapter/compiler sentinel shared by all
// owning-domain exact mutation callbacks. It never crosses the public service
// boundary: Validate returns the result, while Apply maps it to the matching
// conflict or validation error.
type exactMutationValidationError struct {
	domain string
	result core.ValidationResult
}

func (e *exactMutationValidationError) Error() string {
	return fmt.Sprintf("%s AI document operations are invalid", e.domain)
}

func rejectExactMutation(domain string, result core.ValidationResult) error {
	return &exactMutationValidationError{domain: domain, result: result}
}

func handleExactMutationValidationError(err error, validate bool) (core.ValidationResult, error, bool) {
	var invalid *exactMutationValidationError
	if !errors.As(err, &invalid) {
		return core.ValidationResult{}, nil, false
	}
	if validate {
		return invalid.result, nil, true
	}
	if invalid.result.Conflict != nil {
		return core.ValidationResult{}, &core.ConflictError{Conflict: *invalid.result.Conflict}, true
	}
	return core.ValidationResult{}, &core.ValidationError{Result: invalid.result}, true
}
