package artist

import (
	"context"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

const sourceTitleSQL = `COALESCE((SELECT translation.title FROM artist_translation AS translation
	JOIN artist AS source ON source.id = translation.entity_id
		AND source.source_locale = translation.locale
	WHERE translation.entity_id = artist.id LIMIT 1), '')`

const featuredImageSQL = `(SELECT artist_file.file_id FROM artist_file
	WHERE artist_file.artist_id = artist.id ORDER BY artist_file.sort_order ASC LIMIT 1)`

// Requests reads exact-current Artist locale snapshots for OG generation.
type Requests struct{ localized *og.LocalizedRequests }

func NewRequests() *Requests {
	return &Requests{localized: og.NewLocalizedRequests(og.LocalizedRequestSpec{
		EntityType: "artist", Table: "artist", TranslationTable: "artist_translation",
		SourceTitleExpression: sourceTitleSQL, FeaturedImageExpression: featuredImageSQL,
	})}
}

func (*Requests) Handles(entityType string) bool { return entityType == "artist" }

func (r *Requests) Resolve(ctx context.Context, db *gorm.DB, entityType, entityID string, selection *managev1.OgTargetSelection) ([]og.Request, error) {
	if !r.Handles(entityType) {
		return nil, errs.InvalidEntityType(entityType)
	}
	return r.localized.Resolve(ctx, db, entityID, selection)
}

func (r *Requests) All(ctx context.Context, db *gorm.DB) ([]og.Request, error) {
	return r.localized.All(ctx, db)
}
