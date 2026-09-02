package form

import (
	"errors"
	"fmt"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
)

// AIDocumentState keeps Form's raw source topology and exact requested-locale
// values separate. The DCDP adapter may project them, but cannot manufacture
// source fallback into persisted target values.
type AIDocumentState struct {
	FormID           string
	Status           string
	DocumentRevision string
	TargetRevision   *string
	SourceLocale     string
	Locale           string
	LocaleExists     bool
	SourceTitle      *string
	SourceSchema     []byte
	LocaleTitle      *string
	LocaleSchema     []byte
	// ViewerMemberID is resolved only after the locked root's exact mutation
	// authorization succeeds. The adapter uses it as mutation attribution and
	// cannot substitute a different principal.
	ViewerMemberID string
}

type AIDocumentMutation struct {
	FormID                   string
	Locale                   string
	ExpectedDocumentRevision string
	ExpectedTargetRevision   *string
	ExpectedSource           string
	ExpectedPresence         bool
	SetTitle                 bool
	Title                    *string
	SetSchema                bool
	Schema                   []byte
	CreateTranslation        bool
	DeleteTranslation        bool
	Noop                     bool
	ContributorMemberID      string
}

type AIDocumentMutationResult struct {
	DocumentRevision string
	TargetRevision   *string
	Changed          bool
}

type AIDocumentRevisionConflictKind string

const (
	AIDocumentDocumentRevisionConflict AIDocumentRevisionConflictKind = "document"
	AIDocumentTargetRevisionConflict   AIDocumentRevisionConflictKind = "target"
)

type AIDocumentRevisionConflictError struct {
	Kind                    AIDocumentRevisionConflictKind
	CurrentDocumentRevision string
	CurrentTargetRevision   *string
}

func (e *AIDocumentRevisionConflictError) Error() string {
	return fmt.Sprintf("Form AI document %s revision conflict", e.Kind)
}

// AIDocumentExecutionMode selects whether the exact owning-domain mutation
// commits or deliberately rolls back after the same authorization, compiler,
// CAS, validation, and persistence path has run.
type AIDocumentExecutionMode uint8

const (
	AIDocumentExecutionValidate AIDocumentExecutionMode = iota
	AIDocumentExecutionApply
)

// AIDocumentMutationCompiler is implemented by the DCDP schema adapter. It is
// invoked only after Form's root and active principal are locked and the one
// exact Form.Edit decision succeeds.
type AIDocumentMutationCompiler func(AIDocumentState) (AIDocumentMutation, error)

type formAIDocumentCompilerError struct{ cause error }

func (e *formAIDocumentCompilerError) Error() string { return e.cause.Error() }
func (e *formAIDocumentCompilerError) Unwrap() error { return e.cause }

func validateFormAIDocumentMutation(input AIDocumentMutation) error {
	if !IsValidUUID(input.FormID) {
		return errs.InvalidArgument("form_id", "must be a canonical UUID")
	}
	if strings.TrimSpace(input.Locale) == "" || strings.TrimSpace(input.ExpectedSource) == "" || strings.TrimSpace(input.ExpectedDocumentRevision) == "" {
		return errs.InvalidArgument("revision", "locale, source locale, and document revision are required")
	}
	modes := 0
	if input.SetTitle || input.SetSchema {
		modes++
	}
	if input.CreateTranslation {
		modes++
	}
	if input.DeleteTranslation {
		modes++
	}
	if input.Noop {
		modes++
	}
	if modes != 1 {
		return errs.InvalidArgument("operation", "exactly one Form AI document mutation mode is required")
	}
	if input.CreateTranslation && (input.Locale == input.ExpectedSource || input.ExpectedPresence) {
		return errs.InvalidArgument("locale", "only a missing non-source Form translation can be created")
	}
	if input.DeleteTranslation && (input.Locale == input.ExpectedSource || !input.ExpectedPresence) {
		return errs.InvalidArgument("locale", "only an existing non-source Form translation can be deleted")
	}
	if input.Locale == input.ExpectedSource && input.SetTitle && (input.Title == nil || strings.TrimSpace(*input.Title) == "") {
		return errs.Required("title")
	}
	if input.SetTitle && input.Title == nil {
		return errs.InvalidArgument("title", "Form locale copy uses explicit empty instead of missing")
	}
	if input.SetSchema && len(input.Schema) == 0 {
		return errs.Required("schema")
	}
	return nil
}

var errRollbackFormAIDocumentValidation = errors.New("rollback Form AI document validation")
