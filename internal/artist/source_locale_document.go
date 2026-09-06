package artist

import (
	"context"
	"fmt"
	"strings"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
)

func artistSourceLocaleColumnSQL(tableAlias string, column string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "artist"
	}
	return fmt.Sprintf(
		"(SELECT at.%s FROM artist_translation AS at WHERE at.entity_id = %s.id AND at.locale = %s.source_locale LIMIT 1)",
		column,
		alias,
		alias,
	)
}

func ArtistSourceTitleSQL(tableAlias string) string {
	return fmt.Sprintf("COALESCE(%s, '')", artistSourceLocaleColumnSQL(tableAlias, "title"))
}

func saveArtistSourceLocaleDocumentState(
	ctx context.Context,
	db *gorm.DB,
	artistID string,
	locale string,
	input translationLocaleDocumentSaveInput,
) error {
	return saveCreativeSourceLocaleMetadata(ctx, db, "artist", artistID, locale, input)
}

func touchArtistRootUpdatedAt(
	ctx context.Context,
	db *gorm.DB,
	artistID string,
	now time.Time,
) error {
	result := db.WithContext(ctx).
		Model(&model.Artist{}).
		Where("id = ?", artistID).
		Updates(structured.Fields{"updated_at": now})
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	if result.RowsAffected == 0 {
		return errs.NotFound("artist", artistID)
	}
	return nil
}

func collectManageArtistIDs(artists []model.Artist) []string {
	ids := make([]string, 0, len(artists))
	for _, artist := range artists {
		ids = append(ids, artist.ID)
	}
	return ids
}
