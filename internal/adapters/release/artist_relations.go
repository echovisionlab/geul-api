package release

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"gorm.io/gorm"
)

// ArtistRelations reads the Releases and Tracks that link or credit an
// Artist, for Artist.
type ArtistRelations struct{}

// NewArtistRelations returns the Release reader of Artist relations.
func NewArtistRelations() ArtistRelations { return ArtistRelations{} }

// ReleasesOfArtist lists the Releases that link or credit the Artist, with
// their source titles and how many relations point at the Artist.
func (ArtistRelations) ReleasesOfArtist(ctx context.Context, db *gorm.DB, artistID string) ([]entityparticipant.Related, error) {
	var rows []entityparticipant.Related
	err := db.WithContext(ctx).Raw(`
		SELECT release.id::text AS id,
		       `+releaseSourceTitleSQL+` AS display_name,
		       COUNT(*)::integer AS relation_count
		FROM (
			SELECT release_id FROM release_artist WHERE artist_id = ?::uuid
			UNION ALL
			SELECT release_id FROM release_credit WHERE artist_id = ?::uuid
		) AS relation
		JOIN release ON release.id = relation.release_id
		GROUP BY release.id
	`, artistID, artistID).Scan(&rows).Error
	return rows, err
}

// TracksOfArtist lists the Tracks that credit the Artist, with their titles
// and credit counts.
func (ArtistRelations) TracksOfArtist(ctx context.Context, db *gorm.DB, artistID string) ([]entityparticipant.Related, error) {
	var rows []entityparticipant.Related
	err := db.WithContext(ctx).Raw(`
		SELECT track.id::text AS id,
		       track.title AS display_name,
		       COUNT(*)::integer AS relation_count
		FROM track_credit
		JOIN track ON track.id = track_credit.track_id
		WHERE track_credit.artist_id = ?::uuid
		GROUP BY track.id, track.title
	`, artistID).Scan(&rows).Error
	return rows, err
}

// CountReleasesByArtist counts the distinct Releases linked to each Artist.
func (ArtistRelations) CountReleasesByArtist(ctx context.Context, db *gorm.DB, artistIDs []string) (map[string]int32, error) {
	counts := make(map[string]int32)
	if len(artistIDs) == 0 {
		return counts, nil
	}
	var stats []struct {
		ArtistID string `gorm:"column:artist_id"`
		Count    int32  `gorm:"column:count"`
	}
	if err := db.WithContext(ctx).
		Table("release_artist").
		Select("artist_id, COUNT(DISTINCT release_id) as count").
		Where("artist_id IN ?", artistIDs).
		Group("artist_id").
		Scan(&stats).Error; err != nil {
		return nil, err
	}
	for _, stat := range stats {
		counts[stat.ArtistID] = stat.Count
	}
	return counts, nil
}
