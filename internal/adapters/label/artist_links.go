package label

import (
	"context"

	"gorm.io/gorm"
)

// labelSourceTitleSQL is the title of the Label aliased label in its source
// locale.
const labelSourceTitleSQL = `COALESCE((SELECT lt.title FROM label_translation AS lt JOIN label AS source ON source.id = lt.entity_id AND source.source_locale = lt.locale WHERE lt.entity_id = label.id LIMIT 1), '')`

// ArtistLinks reads the Labels an Artist links for Artist.
type ArtistLinks struct{}

// NewArtistLinks returns the Label reader of Artist links.
func NewArtistLinks() ArtistLinks { return ArtistLinks{} }

// LabelTitles maps each existing Label of labelIDs to its source title.
func (ArtistLinks) LabelTitles(ctx context.Context, db *gorm.DB, labelIDs []string) (map[string]string, error) {
	titles := make(map[string]string, len(labelIDs))
	if len(labelIDs) == 0 {
		return titles, nil
	}
	var rows []struct {
		ID    string `gorm:"column:id"`
		Title string `gorm:"column:title"`
	}
	if err := db.WithContext(ctx).Table("label").
		Select("label.id::text AS id, "+labelSourceTitleSQL+" AS title").
		Where("label.id IN ?", labelIDs).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		titles[row.ID] = row.Title
	}
	return titles, nil
}
