package application

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/translation"
)

// loadTranslationSourceDocument reads the entity's current source document.
// An owning domain's loader may lock or materialize rows, so it runs read
// committed rather than in a read-only snapshot.
func (m *TranslationJobManager) loadTranslationSourceDocument(
	ctx context.Context,
	entityType string,
	entityID string,
) (*translation.SourceDocument, error) {
	var source *translation.SourceDocument
	err := m.units.ReadCurrent(ctx, func(store JobStore) error {
		var err error
		source, err = store.Domains().LoadSourceDocument(ctx, entityType, entityID)
		return err
	})
	return source, err
}
