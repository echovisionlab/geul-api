package release

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"gorm.io/gorm"
)

// releaseSourceTitleSQL is the title of the Release aliased release in its
// source locale.
const releaseSourceTitleSQL = `COALESCE(NULLIF((SELECT rt.title FROM release_translation AS rt JOIN release AS source ON source.id = rt.entity_id AND source.source_locale = rt.locale WHERE rt.entity_id = release.id LIMIT 1), ''), '')`

// LabelRelations reads the Release side of Release-Label links for Label.
type LabelRelations struct{}

// NewLabelRelations returns the Release reader of Label links.
func NewLabelRelations() LabelRelations { return LabelRelations{} }

// ReleasesOfLabel lists the Releases linked to the Label with their source
// titles.
func (LabelRelations) ReleasesOfLabel(ctx context.Context, db *gorm.DB, labelID string) ([]entityparticipant.Related, error) {
	var rows []entityparticipant.Related
	err := db.WithContext(ctx).Raw(`
		SELECT release.id::text AS id, `+releaseSourceTitleSQL+` AS display_name
		FROM release_label AS relation
		JOIN release ON release.id = relation.release_id
		WHERE relation.label_id = ?::uuid
		ORDER BY release.id::text
	`, labelID).Scan(&rows).Error
	return rows, err
}

// CountReleasesByLabel counts the Releases linked to each Label.
func (LabelRelations) CountReleasesByLabel(ctx context.Context, db *gorm.DB, labelIDs []string) (map[string]int32, error) {
	counts := make(map[string]int32)
	if len(labelIDs) == 0 {
		return counts, nil
	}
	var stats []struct {
		LabelID string
		Count   int32
	}
	if err := db.WithContext(ctx).
		Table("release_label").
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
