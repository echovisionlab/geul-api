package application

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/translation"
)

func (m *TranslationJobManager) loadTranslationSourceDocument(
	ctx context.Context,
	entityType string,
	entityID string,
) (*translation.SourceDocument, error) {
	return m.domains.LoadSourceDocument(ctx, m.db, m.contentBlocks, entityType, entityID)
}
