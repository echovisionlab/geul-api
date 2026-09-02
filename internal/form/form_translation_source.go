package form

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/translation"
	"gorm.io/gorm"
)

// LoadTranslationSourceDocument projects the canonical Form source into the
// provider-neutral Translation contract.
func LoadTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	formID string,
) (*translation.SourceDocument, error) {
	sourceLocale, err := loadFormResolvedSourceLocale(ctx, db, formID)
	if err != nil {
		return nil, err
	}
	state, err := LoadFormCanonicalSourceDocumentState(ctx, db, formID, sourceLocale)
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
