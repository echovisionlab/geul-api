package artist

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"gorm.io/gorm"
)

type PublicCreativeContentDocument struct {
	Document    *contentv1.LocalizedRichTextDocument
	Revision    string
	SourceTitle string
}

func LoadCreativeContentDocumentForPublicInTransaction(ctx context.Context, tx *gorm.DB, store *contentblock.Store, artistID, locale string) (PublicCreativeContentDocument, error) {
	snapshot, source, err := loadCreativeContentSnapshotInTransaction(ctx, tx, store, artistContentEntity, artistID)
	if err != nil {
		return PublicCreativeContentDocument{}, err
	}
	title, err := loadCreativeSourceTitle(ctx, tx, artistContentEntity, artistID, source.SourceLocale)
	if err != nil {
		return PublicCreativeContentDocument{}, err
	}
	selected := strings.TrimSpace(locale)
	if selected == "" {
		selected = source.SourceLocale
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, selected)
	if err != nil {
		return PublicCreativeContentDocument{}, normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	return PublicCreativeContentDocument{Document: document, Revision: snapshot.Document.Revision.String(), SourceTitle: title}, nil
}

func LoadCreativeSourceTitlesForPublic(ctx context.Context, db *gorm.DB, artistIDs []string) (map[string]string, error) {
	result := make(map[string]string, len(artistIDs))
	if len(artistIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		EntityID string `gorm:"column:entity_id"`
		Title    string `gorm:"column:title"`
	}
	if err := db.WithContext(ctx).Table("artist_translation AS localized").Select("localized.entity_id, localized.title").Joins(`JOIN artist ON artist.id = localized.entity_id AND artist.source_locale = localized.locale`).Where("localized.entity_id IN ?", artistIDs).Scan(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	for _, row := range rows {
		result[row.EntityID] = row.Title
	}
	for _, id := range artistIDs {
		if _, ok := result[id]; !ok {
			return nil, errs.NotFound("artist_translation", id)
		}
	}
	return result, nil
}

func LoadCreativeSourceOgAssetIDsForPublic(ctx context.Context, db *gorm.DB, artistIDs []string) (map[string]*string, error) {
	result := make(map[string]*string, len(artistIDs))
	if len(artistIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		EntityID  string  `gorm:"column:entity_id"`
		OgAssetID *string `gorm:"column:og_asset_id"`
	}
	if err := db.WithContext(ctx).Table("artist_translation AS localized").Select("localized.entity_id, localized.og_asset_id").Joins(`JOIN artist ON artist.id = localized.entity_id AND artist.source_locale = localized.locale`).Where("localized.entity_id IN ?", artistIDs).Scan(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	for _, row := range rows {
		result[row.EntityID] = row.OgAssetID
	}
	return result, nil
}
