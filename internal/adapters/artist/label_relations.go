package artist

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"gorm.io/gorm"
)

// artistSourceTitleSQL is the title of the Artist aliased artist in its
// source locale.
const artistSourceTitleSQL = `COALESCE(NULLIF((SELECT at.title FROM artist_translation AS at JOIN artist AS source ON source.id = at.entity_id AND source.source_locale = at.locale WHERE at.entity_id = artist.id LIMIT 1), ''), '')`

// LabelRelations reads the Artist side of Artist-Label links for Label.
type LabelRelations struct{}

// NewLabelRelations returns the Artist reader of Label links.
func NewLabelRelations() LabelRelations { return LabelRelations{} }

// ArtistsOfLabel lists the Artists linked to the Label with their source
// titles.
func (LabelRelations) ArtistsOfLabel(ctx context.Context, db *gorm.DB, labelID string) ([]entityparticipant.Related, error) {
	var rows []entityparticipant.Related
	err := db.WithContext(ctx).Raw(`
		SELECT artist.id::text AS id, `+artistSourceTitleSQL+` AS display_name
		FROM artist_label AS relation
		JOIN artist ON artist.id = relation.artist_id
		WHERE relation.label_id = ?::uuid
		ORDER BY artist.id::text
	`, labelID).Scan(&rows).Error
	return rows, err
}

// CountArtistsByLabel counts the Artists linked to each Label.
func (LabelRelations) CountArtistsByLabel(ctx context.Context, db *gorm.DB, labelIDs []string) (map[string]int32, error) {
	counts := make(map[string]int32)
	if len(labelIDs) == 0 {
		return counts, nil
	}
	var stats []struct {
		LabelID string
		Count   int32
	}
	if err := db.WithContext(ctx).
		Table("artist_label").
		Select("label_id, COUNT(*) as count").
		Where("label_id IN ?", labelIDs).
		Group("label_id").
		Scan(&stats).Error; err != nil {
		return nil, err
	}
	for _, stat := range stats {
		counts[stat.LabelID] = stat.Count
	}
	return counts, nil
}
