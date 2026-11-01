package release

import (
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

type AIDocumentState struct {
	ReleaseID         string
	Status            string
	DocumentID        uuid.UUID
	DocumentRevision  string
	TargetRevision    *string
	SourceLocale      string
	Locale            string
	LocaleExists      bool
	Document          *contentv1.LocalizedRichTextDocument
	CreditIDs         []string
	SourceMetadata    AIDocumentLocaleMetadata
	RequestedMetadata *AIDocumentLocaleMetadata
	ViewerMemberID    string
}

type AIDocumentLocaleMetadata struct {
	Title       *string
	CreditNotes map[string]string
}

type AIDocumentMutation struct {
	ReleaseID                string
	Locale                   string
	ExpectedDocumentRevision string
	ExpectedTargetRevision   *string
	ExpectedSource           string
	ExpectedPresence         bool
	ContributorMemberID      uuid.UUID
	Batch                    *contentblock.Batch
	SetTitle                 bool
	Title                    *string
	CreditNotePatch          map[string]string
	CreateTranslation        bool
	DeleteTranslation        bool
}

// AIDocumentExecutionMode selects whether the exact Release mutation
// transaction commits or deliberately rolls back after exercising the same
// authorization, compiler, CAS, persistence, and side-effect path.
type AIDocumentExecutionMode uint8

const (
	AIDocumentExecutionValidate AIDocumentExecutionMode = iota
	AIDocumentExecutionApply
)

// AIDocumentMutationCompiler is implemented by the schema adapter. It only
// receives Release state after the root lock and exact Edit decision succeed.
type AIDocumentMutationCompiler func(AIDocumentState) (AIDocumentMutation, error)

type releaseAIDocumentCompilerError struct{ cause error }

func (e *releaseAIDocumentCompilerError) Error() string { return e.cause.Error() }
func (e *releaseAIDocumentCompilerError) Unwrap() error { return e.cause }

type AIDocumentMutationResult struct {
	DocumentRevision string
	TargetRevision   *string
	Changed          bool
}

func validateReleaseAIDocumentMutation(input AIDocumentMutation) (uuid.UUID, uuid.UUID, error) {
	releaseID, err := uuidFromCanonicalString(input.ReleaseID)
	if err != nil {
		return uuid.Nil, uuid.Nil, domainerr.InvalidField("release_id", "must be a canonical UUID")
	}
	expected, err := uuidFromCanonicalString(input.ExpectedDocumentRevision)
	if err != nil {
		return uuid.Nil, uuid.Nil, domainerr.InvalidField("expected_revision", "must be a canonical UUID")
	}
	if strings.TrimSpace(input.Locale) == "" || strings.TrimSpace(input.ExpectedSource) == "" {
		return uuid.Nil, uuid.Nil, domainerr.InvalidField("locale", "requested and source locales are required")
	}
	modes := 0
	if input.Batch != nil || input.SetTitle || len(input.CreditNotePatch) != 0 {
		modes++
	}
	if input.CreateTranslation {
		modes++
	}
	if input.DeleteTranslation {
		modes++
	}
	if modes != 1 {
		return uuid.Nil, uuid.Nil, domainerr.InvalidField("operation", "exactly one Release AI document mutation mode is required")
	}
	if input.CreateTranslation && (input.Locale == input.ExpectedSource || input.ExpectedPresence) {
		return uuid.Nil, uuid.Nil, domainerr.InvalidField("locale", "only a missing non-source Release translation can be created")
	}
	if input.DeleteTranslation && (input.Locale == input.ExpectedSource || !input.ExpectedPresence) {
		return uuid.Nil, uuid.Nil, domainerr.InvalidField("locale", "only an existing non-source Release translation can be deleted")
	}
	return releaseID, expected, nil
}

func mapReleaseAIDocumentMutationError(err error) error {
	var conflict *aidocument.RevisionConflict
	if errors.As(err, &conflict) {
		return conflict
	}
	var stale *contentblock.StaleRevisionError
	if errors.As(err, &stale) {
		return &aidocument.RevisionConflict{
			Kind:                    aidocument.ConflictDocumentRevision,
			CurrentDocumentRevision: stale.CurrentRevision.String(),
			Message:                 fmt.Sprintf("Release AI document revision conflict: current document revision is %q", stale.CurrentRevision.String()),
		}
	}
	return normalizeReleaseContentBlockError(err)
}
