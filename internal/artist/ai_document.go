package artist

import (
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

// AIDocumentState is Artist's authorized compact-document projection. DCDP
// conversion remains in the adapter; this type keeps persistence and policy
// details inside Artist.
type AIDocumentState struct {
	ArtistID       string
	Status         string
	DocumentID     uuid.UUID
	Revision       string
	TargetRevision *string
	SourceLocale   string
	Locale         string
	LocaleExists   bool
	Document       *contentv1.LocalizedRichTextDocument
	ViewerMemberID string
}

type AIDocumentMutation struct {
	ArtistID               string
	Locale                 string
	ExpectedRevision       string
	ExpectedTargetRevision *string
	ExpectedSource         string
	ExpectedPresence       bool
	ContributorMemberID    uuid.UUID
	Batch                  *contentblock.Batch
	CreateTranslation      bool
	DeleteTranslation      bool
}

type AIDocumentExecutionMode uint8

const (
	AIDocumentExecutionValidate AIDocumentExecutionMode = iota
	AIDocumentExecutionApply
)

type AIDocumentMutationCompiler func(AIDocumentState) (AIDocumentMutation, error)

type artistAIDocumentCompilerError struct{ cause error }

func (e *artistAIDocumentCompilerError) Error() string { return e.cause.Error() }
func (e *artistAIDocumentCompilerError) Unwrap() error { return e.cause }

type AIDocumentMutationResult struct {
	Revision       string
	TargetRevision *string
	Changed        bool
}

type AIDocumentRevisionConflictKind string

const (
	AIDocumentDocumentRevisionConflict AIDocumentRevisionConflictKind = "document"
	AIDocumentTargetRevisionConflict   AIDocumentRevisionConflictKind = "target"
)

type AIDocumentRevisionConflictError struct {
	Kind                  AIDocumentRevisionConflictKind
	CurrentRevision       string
	CurrentTargetRevision *string
}

func (e *AIDocumentRevisionConflictError) Error() string {
	return fmt.Sprintf("Artist AI document revision conflict: current revision is %q", e.CurrentRevision)
}

func validateArtistAIDocumentMutation(input AIDocumentMutation) (uuid.UUID, uuid.UUID, error) {
	artistID, err := canonicalArtistUUID(input.ArtistID)
	if err != nil {
		return uuid.Nil, uuid.Nil, errs.InvalidArgument("artist_id", "must be a canonical UUID")
	}
	expected, err := canonicalArtistUUID(input.ExpectedRevision)
	if err != nil {
		return uuid.Nil, uuid.Nil, errs.InvalidArgument("expected_revision", "must be a canonical UUID")
	}
	if strings.TrimSpace(input.Locale) == "" || strings.TrimSpace(input.ExpectedSource) == "" {
		return uuid.Nil, uuid.Nil, errs.InvalidArgument("locale", "requested and source locales are required")
	}
	modes := 0
	if input.Batch != nil {
		modes++
	}
	if input.CreateTranslation {
		modes++
	}
	if input.DeleteTranslation {
		modes++
	}
	if modes != 1 {
		return uuid.Nil, uuid.Nil, errs.InvalidArgument("operation", "exactly one Artist AI document mutation mode is required")
	}
	if input.CreateTranslation && (input.Locale == input.ExpectedSource || input.ExpectedPresence) {
		return uuid.Nil, uuid.Nil, errs.InvalidArgument("locale", "only a missing non-source Artist translation can be created")
	}
	if input.DeleteTranslation && (input.Locale == input.ExpectedSource || !input.ExpectedPresence) {
		return uuid.Nil, uuid.Nil, errs.InvalidArgument("locale", "only an existing non-source Artist translation can be deleted")
	}
	return artistID, expected, nil
}

func mapArtistAIDocumentMutationError(err error) error {
	var conflict *AIDocumentRevisionConflictError
	if errors.As(err, &conflict) {
		return conflict
	}
	var stale *contentblock.StaleRevisionError
	if errors.As(err, &stale) {
		return &AIDocumentRevisionConflictError{
			Kind: AIDocumentDocumentRevisionConflict, CurrentRevision: stale.CurrentRevision.String(),
		}
	}
	var targetConflict *translation.TargetRevisionConflict
	if errors.As(err, &targetConflict) {
		return targetConflict
	}
	return normalizeCreativeContentBlockError(artistContentEntity, err)
}
