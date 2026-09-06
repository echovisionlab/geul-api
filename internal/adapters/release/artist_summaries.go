package release

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	releasepkg "github.com/echovisionlab/geul-api/internal/release"
)

type ArtistSummaries struct {
	db *gorm.DB
}

func NewArtistSummaries(db *gorm.DB) *ArtistSummaries {
	if db == nil {
		panic("release artist summaries: db is required")
	}
	return &ArtistSummaries{db: db}
}

func (a *ArtistSummaries) LoadArtistSummaries(ctx context.Context, artistIDs []string) (map[string]releasepkg.ArtistSummary, error) {
	return a.LoadArtistSummariesWithDB(ctx, a.db, artistIDs)
}

func (a *ArtistSummaries) LoadArtistSummariesWithDB(ctx context.Context, db *gorm.DB, artistIDs []string) (map[string]releasepkg.ArtistSummary, error) {
	result := make(map[string]releasepkg.ArtistSummary, len(artistIDs))
	if len(artistIDs) == 0 {
		return result, nil
	}
	type row struct {
		ID    string `gorm:"column:id"`
		Title string `gorm:"column:title"`
		Slug  string `gorm:"column:slug"`
	}
	var rows []row
	if err := db.WithContext(ctx).
		Table("artist AS artist").
		Select("artist.id, "+artistSourceTitleSQL("artist")+" AS title, COALESCE(artist.slug, '') AS slug").
		Where("artist.id IN ?", artistIDs).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, value := range rows {
		result[value.ID] = releasepkg.ArtistSummary{Title: value.Title, Slug: value.Slug}
	}
	return result, nil
}

func artistSourceTitleSQL(tableAlias string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "artist"
	}
	return fmt.Sprintf(
		"COALESCE((SELECT at.title FROM artist_translation AS at WHERE at.entity_id = %s.id AND at.locale = %s.source_locale LIMIT 1), '')",
		alias,
		alias,
	)
}
