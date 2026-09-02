package aidocumentadapter

import (
	"errors"
	"fmt"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

// exactMutationRun owns only the schema-independent exact-mutation steps that
// are identical across adapters. Owning-domain locking, authorization,
// compilation, conflict mapping, persistence, and callbacks remain in each
// port.
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

func handleExactMutationValidationError(
	err error,
	validate bool,
) (core.ValidationResult, error, bool) {
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
