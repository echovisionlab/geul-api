package legal

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// TranslationJob is the Translation job a typed candidate applies.
type TranslationJob struct {
	EntityType          string
	EntityID            string
	SourceLocale        string
	TargetLocale        string
	RequestedByMemberID string
}

// LoadTypedTranslationSourceDocument loads the canonical policy Rich Text
// document used for translation extraction.
func LoadTypedTranslationSourceDocument(
	ctx context.Context,
	store Store,
	entityType string,
	entityID string,
) (*translation.SourceDocument, error) {
	kind, err := legalKind(entityType)
	if err != nil {
		return nil, err
	}
	root, err := loadLegalContentDocumentRoot(ctx, store, kind, entityID, false)
	if err != nil {
		return nil, err
	}
	content, err := loadLegalTypedTranslationContentForLocale(ctx, store, kind, entityID, root.SourceLocale)
	if err != nil {
		return nil, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(content.snapshot, root.SourceLocale)
	if err != nil {
		return nil, normalizeLegalContentBlockError(entityType, err)
	}
	return &translation.SourceDocument{
		SourceLocale:            root.SourceLocale,
		Title:                   content.title,
		ContentDocumentRevision: content.snapshot.Document.Revision.String(),
		ContentBlockDocument:    document,
	}, nil
}

type legalTypedTranslationContent struct {
	title    string
	snapshot contentblock.Snapshot
}

func loadLegalTypedTranslationContentForLocale(
	ctx context.Context,
	store Store,
	kind *policyKind,
	entityID string,
	locale string,
) (legalTypedTranslationContent, error) {
	locale, err := canonicalLegalLocale(locale)
	if err != nil {
		return legalTypedTranslationContent{}, err
	}
	root, err := loadLegalContentDocumentRoot(ctx, store, kind, entityID, false)
	if err != nil {
		return legalTypedTranslationContent{}, err
	}
	title := root.Title
	if locale != root.SourceLocale {
		targetTitle, err := store.TranslationTitle(ctx, kind.kind, entityID, locale)
		if err != nil {
			if errors.Is(err, ErrTranslationNotFound) {
				return legalTypedTranslationContent{}, domainerr.ResourceNotFound(kind.entity()+" translation", locale)
			}
			return legalTypedTranslationContent{}, err
		}
		if targetTitle == nil || strings.TrimSpace(*targetTitle) == "" {
			return legalTypedTranslationContent{}, domainerr.Precondition(kind.entity() + " target locale title is missing")
		}
		title = strings.TrimSpace(*targetTitle)
	}
	snapshot, err := store.ContentSnapshot(ctx, *root.ContentDocumentID, locale)
	if err != nil {
		return legalTypedTranslationContent{}, normalizeLegalContentBlockError(kind.entity(), err)
	}
	if snapshot.Document.Profile != legalContentDocumentProfile {
		return legalTypedTranslationContent{}, domainerr.Precondition(kind.entity() + " content document must use the policy profile")
	}
	return legalTypedTranslationContent{title: title, snapshot: snapshot}, nil
}

// legalTranslationApply writes one validated translation batch.
type legalTranslationApply struct {
	store       Store
	kind        *policyKind
	job         TranslationJob
	root        Root
	batch       contentblock.Batch
	title       string
	now         time.Time
	auditWriter domainaudit.Appender
}
