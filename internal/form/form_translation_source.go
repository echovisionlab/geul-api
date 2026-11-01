package form

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/translation"
)

// LoadTranslationSourceDocument projects the canonical Form source into the
// provider-neutral Translation contract.
func LoadTranslationSourceDocument(ctx context.Context, store Store, formID string) (*translation.SourceDocument, error) {
	sourceLocale, err := loadFormResolvedSourceLocale(ctx, store, formID)
	if err != nil {
		return nil, err
	}
	state, err := LoadFormCanonicalSourceDocumentState(ctx, store, formID, sourceLocale)
	if err != nil {
		return nil, err
	}
	title := ""
	if state.Title != nil {
		title = *state.Title
	}
	return &translation.SourceDocument{
		Title:       title,
		ContentJSON: state.ContentJSON,
		ContentText: state.ContentText,
	}, nil
}
