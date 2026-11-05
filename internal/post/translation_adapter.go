package post

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// TranslationJob is the translation job a delivery or interchange applies.
type TranslationJob struct {
	EntityType          string
	EntityID            string
	SourceLocale        string
	TargetLocale        string
	RequestedByMemberID string
}

// LoadTypedTranslationSourceDocument reads the Post's source-locale title,
// summary and content document for translation, through store bound to the
// translation domain's database handle.
func LoadTypedTranslationSourceDocument(ctx context.Context, joined Joined, postID string) (*translation.SourceDocument, error) {
	store := joined.Store

	sourceLocale, err := store.SourceLocale(ctx, postID)
	if err != nil {
		return nil, err
	}
	metadata, err := loadPostLocaleMetadataRow(ctx, store, postID, sourceLocale, false)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPostContentDocumentID(ctx, store, postID)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.ContentSnapshot(ctx, documentID, sourceLocale)
	if err != nil {
		return nil, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, sourceLocale)
	if err != nil {
		return nil, err
	}
	return &translation.SourceDocument{
		Title: derefString(metadata.Title), Summary: metadata.Summary,
		ContentBlockDocument: document, ContentDocumentRevision: snapshot.Document.Revision.String(),
	}, nil
}

type postTranslationCandidateValidation uint8

const (
	postTranslationCandidateRequireTitle postTranslationCandidateValidation = iota
	postTranslationCandidateAllowSparseInterchange
)

func validatePostTranslationCandidate(
	candidate *translation.Candidate,
	validation postTranslationCandidateValidation,
) error {
	if candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed post translation candidate and content Block store are required")
	}
	switch validation {
	case postTranslationCandidateRequireTitle:
		if candidate.Title == nil {
			return domainerr.Precondition("translated Post title is required")
		}
	case postTranslationCandidateAllowSparseInterchange:
		// A nil title is a missing target unit, while a non-nil empty title is
		// an explicit empty locale value. XLIFF interchange preserves both.
	default:
		return errors.New("post translation candidate validation profile is invalid")
	}
	return nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
