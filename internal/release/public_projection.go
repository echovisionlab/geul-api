package release

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// PublicContentDocument is a Release's content document in the displayed
// locale, with its revision and source title.
type PublicContentDocument struct {
	Document    *contentv1.LocalizedRichTextDocument
	Revision    string
	SourceTitle string
}

// LoadPublicContentDocument share-locks the Release root and reads its
// content document in locale, the source locale when empty, through store
// bound to the public read's snapshot.
func LoadPublicContentDocument(ctx context.Context, store Store, releaseID, locale string) (PublicContentDocument, error) {
	snapshot, sourceLocale, err := loadReleaseContentSnapshotInTransaction(ctx, store, releaseID)
	if err != nil {
		return PublicContentDocument{}, err
	}
	title, err := loadReleaseSourceTitle(ctx, store, releaseID, sourceLocale)
	if err != nil {
		return PublicContentDocument{}, err
	}
	if locale == "" {
		locale = sourceLocale
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
	if err != nil {
		return PublicContentDocument{}, normalizeReleaseContentBlockError(err)
	}
	return PublicContentDocument{Document: document, Revision: snapshot.Document.Revision.String(), SourceTitle: title}, nil
}
