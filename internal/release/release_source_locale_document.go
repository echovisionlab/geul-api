package release

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

func releaseSourceLocaleColumnSQL(tableAlias string, column string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "release"
	}
	return fmt.Sprintf(
		"(SELECT rt.%s FROM release_translation AS rt JOIN release AS owner ON owner.id = rt.entity_id AND owner.source_locale = rt.locale WHERE rt.entity_id = %s.id LIMIT 1)",
		column,
		alias,
	)
}

func ReleaseSourceTitleSQL(tableAlias string) string {
	return fmt.Sprintf("COALESCE(%s, '')", releaseSourceLocaleColumnSQL(tableAlias, "title"))
}

func saveReleaseSourceLocaleDocumentState(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
	locale string,
	input translationLocaleDocumentSaveInput,
) error {
	return saveReleaseSourceLocaleMetadata(ctx, db, releaseID, locale, input)
}

func touchReleaseSourceLocaleState(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
	now time.Time,
) error {
	result := db.WithContext(ctx).
		Model(&model.Release{}).
		Where("id = ?", releaseID).
		Updates(structured.Fields{"updated_at": now})
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	if result.RowsAffected == 0 {
		return errs.NotFound("release", releaseID)
	}
	return nil
}

func collectManageReleaseIDs(releases []model.Release) []string {
	ids := make([]string, 0, len(releases))
	for _, release := range releases {
		ids = append(ids, release.ID)
	}
	return ids
}
