package application

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/translation"
)

func (m *TranslationJobManager) loadTranslationSourceDocument(
	ctx context.Context,
	entityType string,
	entityID string,
) (*translation.SourceDocument, error) {
	if m.domains == nil {
		return nil, fmt.Errorf("translation domain registry is required")
	}
	return m.domains.LoadSourceDocument(ctx, m.db, m.contentBlocks, entityType, entityID)
}
