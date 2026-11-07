package artistpostgres

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
)

func (s *store) ChildArtists(ctx context.Context, artistID string) ([]entityparticipant.Related, error) {
	var rows []entityparticipant.Related
	err := s.tx.WithContext(ctx).Raw(`
		SELECT child.id::text AS id,
		       `+artistSourceTitleSQL("child")+` AS display_name,
		       1::integer AS relation_count
		FROM artist AS child
		WHERE child.parent_artist_id = ?::uuid
	`, artistID).Scan(&rows).Error
	return rows, err
}

func (s *store) ProgramEventsOfArtist(ctx context.Context, artistID string) ([]entityparticipant.Related, error) {
	return s.ports.ProgramEvents.ProgramEventsOfArtist(ctx, s.tx, artistID)
}

func (s *store) ReleasesOfArtist(ctx context.Context, artistID string) ([]entityparticipant.Related, error) {
	return s.ports.Releases.ReleasesOfArtist(ctx, s.tx, artistID)
}

func (s *store) TracksOfArtist(ctx context.Context, artistID string) ([]entityparticipant.Related, error) {
	return s.ports.Releases.TracksOfArtist(ctx, s.tx, artistID)
}

func (s *store) WorksOfArtist(ctx context.Context, artistID string) ([]entityparticipant.Related, error) {
	return s.ports.Works.WorksOfArtist(ctx, s.tx, artistID)
}

func (s *store) LabelTitles(ctx context.Context, labelIDs []string) (map[string]string, error) {
	return s.ports.Labels.LabelTitles(ctx, s.tx, labelIDs)
}

func (s *store) CountReleasesByArtist(ctx context.Context, artistIDs []string) (map[string]int32, error) {
	return s.ports.Releases.CountReleasesByArtist(ctx, s.tx, artistIDs)
}

func (s *store) DeleteShareLinks(ctx context.Context, artistID string) error {
	return s.ports.ShareLinks.DeleteArtistShareLinks(ctx, s.tx, artistID)
}
