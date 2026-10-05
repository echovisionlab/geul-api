package release

import (
	"context"
	"fmt"
	"strings"

	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	"gorm.io/gorm"
)

type LabelSummaries struct{ db *gorm.DB }

func NewLabelSummaries(db *gorm.DB) *LabelSummaries {
	if db == nil {
		panic("release label summaries: db is required")
	}
	return &LabelSummaries{db: db}
}

func (loader *LabelSummaries) LoadLabelSummaries(ctx context.Context, labelIDs []string) (map[string]releasepkg.LabelSummary, error) {
	return loader.LoadLabelSummariesWithDB(ctx, loader.db, labelIDs)
}

func (loader *LabelSummaries) LoadLabelSummariesWithDB(ctx context.Context, db *gorm.DB, labelIDs []string) (map[string]releasepkg.LabelSummary, error) {
	result := make(map[string]releasepkg.LabelSummary, len(labelIDs))
	if len(labelIDs) == 0 {
		return result, nil
	}
	type row struct {
		ID    string `gorm:"column:id"`
		Title string `gorm:"column:title"`
		Slug  string `gorm:"column:slug"`
	}
	var rows []row
	if err := db.WithContext(ctx).
		Table("label AS label").
		Select("label.id, "+labelSourceTitleSQL("label")+" AS title, COALESCE(label.slug, '') AS slug").
		Where("label.id IN ?", labelIDs).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, value := range rows {
		result[value.ID] = releasepkg.LabelSummary{Title: value.Title, Slug: value.Slug}
	}
	return result, nil
}

func labelSourceTitleSQL(tableAlias string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "label"
	}
	return fmt.Sprintf(
		"COALESCE((SELECT lt.title FROM label_translation AS lt WHERE lt.entity_id = %s.id AND lt.locale = %s.source_locale LIMIT 1), '')",
		alias,
		alias,
	)
}
