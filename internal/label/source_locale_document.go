package label

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

type translationLocaleDocumentSaveInput struct {
	StructureHash       *string
	MaterializationHash *string
	Title               *string
	Summary             *string
	ContentJSON         []byte
	ContentHTML         *string
	ContentText         *string
	OverwriteNullFields bool
	Now                 time.Time
}

func labelSourceLocaleColumnSQL(tableAlias string, column string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "label"
	}
	return fmt.Sprintf(
		"(SELECT lt.%s FROM label_translation AS lt JOIN label AS source ON source.id = lt.entity_id AND source.source_locale = lt.locale WHERE lt.entity_id = %s.id LIMIT 1)",
		column,
		alias,
	)
}

func LabelSourceTitleSQL(tableAlias string) string {
	return fmt.Sprintf("COALESCE(NULLIF(%s, ''), '')", labelSourceLocaleColumnSQL(tableAlias, "title"))
}

func ArtistSourceTitleSQL(alias string) string {
	return fmt.Sprintf("COALESCE(NULLIF((SELECT at.title FROM artist_translation AS at JOIN artist AS source ON source.id = at.entity_id AND source.source_locale = at.locale WHERE at.entity_id = %s.id LIMIT 1), ''), '')", alias)
}

func ReleaseSourceTitleSQL(alias string) string {
	return fmt.Sprintf("COALESCE(NULLIF((SELECT rt.title FROM release_translation AS rt JOIN release AS source ON source.id = rt.entity_id AND source.source_locale = rt.locale WHERE rt.entity_id = %s.id LIMIT 1), ''), '')", alias)
}

func saveLabelSourceLocaleDocumentState(
	ctx context.Context,
	db *gorm.DB,
	labelID string,
	locale string,
	input translationLocaleDocumentSaveInput,
) error {
	if len(input.ContentJSON) != 0 || input.ContentHTML != nil || input.ContentText != nil || input.Summary != nil || input.StructureHash != nil || input.MaterializationHash != nil {
		return errs.FailedPrecondition("label source body is stored as typed Content Blocks")
	}
	if input.Title == nil || strings.TrimSpace(*input.Title) == "" {
		return errs.InvalidArgument("title", "must not be empty")
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return db.WithContext(ctx).Exec(`
		INSERT INTO label_translation (
			entity_id, locale, title, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			title = EXCLUDED.title,
			updated_at = EXCLUDED.updated_at`,
		labelID, locale, strings.TrimSpace(*input.Title), now, now,
	).Error
}

func touchLabelRootUpdatedAt(
	ctx context.Context,
	db *gorm.DB,
	labelID string,
	now time.Time,
) error {
	result := db.WithContext(ctx).
		Model(&model.Label{}).
		Where("id = ?", labelID).
		Updates(structured.Fields{"updated_at": now})
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	if result.RowsAffected == 0 {
		return errs.NotFound("label", labelID)
	}
	return nil
}
