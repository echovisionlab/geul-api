package postgres

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/label"
	"gorm.io/gorm"
)

func (s *store) DeletionImpacts(ctx context.Context, labelID string) ([]label.DeletionImpact, error) {
	var rows []struct {
		Kind        int32  `gorm:"column:kind"`
		EntityID    string `gorm:"column:entity_id"`
		DisplayName string `gorm:"column:display_name"`
	}
	if err := s.tx.WithContext(ctx).Raw(fmt.Sprintf(`
		SELECT 1::integer AS kind, child.id::text AS entity_id, %s AS display_name
		FROM label AS child
		WHERE child.parent_label_id = ?::uuid
		ORDER BY child.id::text
	`, labelSourceTitleSQL("child")), labelID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	impacts := make([]label.DeletionImpact, 0, len(rows))
	for _, row := range rows {
		impacts = append(impacts, label.DeletionImpact{Kind: label.DeletionImpactKind(row.Kind), EntityID: row.EntityID, DisplayName: row.DisplayName})
	}
	for _, related := range []struct {
		kind label.DeletionImpactKind
		load func(context.Context, *gorm.DB, string) ([]entityparticipant.Related, error)
	}{
		{kind: label.ImpactArtist, load: s.deps.Artists.ArtistsOfLabel},
		{kind: label.ImpactProgramEvent, load: s.deps.ProgramEvents.ProgramEventsOfLabel},
		{kind: label.ImpactRelease, load: s.deps.Releases.ReleasesOfLabel},
	} {
		entities, err := related.load(ctx, s.tx, labelID)
		if err != nil {
			return nil, err
		}
		for _, entity := range entities {
			impacts = append(impacts, label.DeletionImpact{Kind: related.kind, EntityID: entity.ID, DisplayName: entity.DisplayName})
		}
	}
	return impacts, nil
}

func (s *store) CountArtistsByLabel(ctx context.Context, labelIDs []string) (map[string]int32, error) {
	return s.deps.Artists.CountArtistsByLabel(ctx, s.tx, labelIDs)
}

func (s *store) CountReleasesByLabel(ctx context.Context, labelIDs []string) (map[string]int32, error) {
	return s.deps.Releases.CountReleasesByLabel(ctx, s.tx, labelIDs)
}

func (s *store) DeleteShareLinks(ctx context.Context, labelID string) error {
	return s.deps.ShareLinks.DeleteLabelShareLinks(ctx, s.tx, labelID)
}
