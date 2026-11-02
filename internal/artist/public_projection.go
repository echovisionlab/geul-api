package artist

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// PublicCreativeContentDocument is an Artist's content document in the
// locale a public read displays, with its source title.
type PublicCreativeContentDocument struct {
	Document    *contentv1.LocalizedRichTextDocument
	Revision    string
	SourceTitle string
}

// LoadCreativeContentDocumentForPublic reads the Artist's content document
// in locale, the source locale when empty, in the public read's snapshot,
// which store is bound to.
func LoadCreativeContentDocumentForPublic(ctx context.Context, joined Joined, artistID, locale string) (PublicCreativeContentDocument, error) {
	store := joined.Store

	snapshot, sourceLocale, err := loadCreativeContentSnapshotInTransaction(ctx, store, artistID)
	if err != nil {
		return PublicCreativeContentDocument{}, err
	}
	title, err := loadCreativeSourceTitle(ctx, store, artistID, sourceLocale)
	if err != nil {
		return PublicCreativeContentDocument{}, err
	}
	selected := strings.TrimSpace(locale)
	if selected == "" {
		selected = sourceLocale
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, selected)
	if err != nil {
		return PublicCreativeContentDocument{}, normalizeCreativeContentBlockError(err)
	}
	return PublicCreativeContentDocument{Document: document, Revision: snapshot.Document.Revision.String(), SourceTitle: title}, nil
}
