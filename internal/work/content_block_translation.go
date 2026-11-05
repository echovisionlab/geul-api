package work

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"
)

// TranslationJob is the part of a translation job a Work delivery reads.
type TranslationJob struct {
	EntityType          string
	EntityID            string
	SourceLocale        string
	TargetLocale        string
	RequestedByMemberID string
}

// LoadTypedTranslationSourceDocument loads the canonical Work Rich Text
// document used by translation extraction, through store bound to the
// translation domain's database handle.
func LoadTypedTranslationSourceDocument(ctx context.Context, store Store, workID string) (*translation.SourceDocument, error) {
	source, err := LoadRequiredSourceLocaleMetadata(ctx, store, workID)
	if err != nil {
		return nil, err
	}
	documentID, err := loadWorkContentDocumentID(ctx, store, workID, Unlocked)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.ContentSnapshot(ctx, documentID, source.Locale)
	if err != nil {
		return nil, normalizeWorkContentBlockError(err)
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, source.Locale)
	if err != nil {
		return nil, normalizeWorkContentBlockError(err)
	}
	return &translation.SourceDocument{
		Title:                   optionalString(source.Title),
		Summary:                 cloneOptionalString(source.Summary),
		ContentDocumentRevision: snapshot.Document.Revision.String(),
		ContentBlockDocument:    document,
	}, nil
}

func workSystemTranslationDocumentFence(workID string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		work, err := lockWorkForContentDocument(ctx, store, documentID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if work.ID != workID {
			return contentblock.DomainContext{}, fmt.Errorf("work content document changed during translation apply")
		}
		return loadWorkContentDomainContext(ctx, store, workID)
	}
}
