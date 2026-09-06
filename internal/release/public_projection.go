package release

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"gorm.io/gorm"
)

type PublicContentDocument struct {
	Document    *contentv1.LocalizedRichTextDocument
	Revision    string
	SourceTitle string
}

func LoadPublicContentDocument(ctx context.Context, tx *gorm.DB, store *contentblock.Store, releaseID, locale string) (PublicContentDocument, error) {
	snapshot, source, err := loadReleaseContentSnapshotInTransaction(ctx, tx, store, releaseID)
	if err != nil {
		return PublicContentDocument{}, err
	}
	title, err := loadReleaseSourceTitle(ctx, tx, releaseID, source.SourceLocale)
	if err != nil {
		return PublicContentDocument{}, err
	}
	if locale == "" {
		locale = source.SourceLocale
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
	if err != nil {
		return PublicContentDocument{}, normalizeReleaseContentBlockError(err)
	}
	return PublicContentDocument{Document: document, Revision: snapshot.Document.Revision.String(), SourceTitle: title}, nil
}

func LoadPublicSourceTitles(ctx context.Context, db *gorm.DB, releaseIDs []string) (map[string]string, error) {
	result := make(map[string]string, len(releaseIDs))
	if len(releaseIDs) == 0 {
		return result, nil
	}
	type row struct {
		EntityID string `gorm:"column:entity_id"`
		Title    string `gorm:"column:title"`
	}
	var rows []row
	if err := db.WithContext(ctx).Table("release_translation AS localized").
		Select("localized.entity_id, localized.title").
		Joins(`JOIN release AS owner ON owner.id = localized.entity_id AND owner.source_locale = localized.locale`).
		Where("localized.entity_id IN ?", releaseIDs).Scan(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	for _, value := range rows {
		result[value.EntityID] = value.Title
	}
	for _, id := range releaseIDs {
		if _, ok := result[id]; !ok {
			return nil, errs.NotFound("release_translation", id)
		}
	}
	return result, nil
}
