package public

import (
	"context"

	"gorm.io/gorm"

	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func loadArtistReleaseOtherArtists(
	ctx context.Context,
	db *gorm.DB,
	releaseIDs []string,
	excludeArtistID string,
) (map[string][]*openv1.ArtistReleaseArtist, error) {
	result := make(map[string][]*openv1.ArtistReleaseArtist, len(releaseIDs))
	if len(releaseIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		ReleaseID  string  `gorm:"column:release_id"`
		ArtistID   string  `gorm:"column:artist_id"`
		ArtistName string  `gorm:"column:artist_name"`
		ArtistSlug *string `gorm:"column:artist_slug"`
	}
	if err := db.WithContext(ctx).
		Table("release_artist").
		Select("release_artist.release_id::text, artist.id::text AS artist_id, "+artistdomain.ArtistSourceTitleSQL("artist")+" AS artist_name, artist.slug AS artist_slug").
		Joins("JOIN artist ON artist.id = release_artist.artist_id").
		Where("release_artist.release_id IN ? AND release_artist.artist_id != ?::uuid", releaseIDs, excludeArtistID).
		Order("release_artist.release_id, release_artist.sort_order, release_artist.created_at, release_artist.artist_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		artist := &openv1.ArtistReleaseArtist{Id: row.ArtistID, Name: row.ArtistName}
		if row.ArtistSlug != nil {
			artist.Slug = row.ArtistSlug
		}
		result[row.ReleaseID] = append(result[row.ReleaseID], artist)
	}
	return result, nil
}
