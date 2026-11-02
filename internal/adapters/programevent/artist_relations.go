package programevent

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"gorm.io/gorm"
)

// ArtistRelations reads the Program Events that list or credit an Artist,
// for Artist.
type ArtistRelations struct{}

// NewArtistRelations returns the Program Event reader of Artist relations.
func NewArtistRelations() ArtistRelations { return ArtistRelations{} }

// ProgramEventsOfArtist lists the Program Events that list or credit the
// Artist, with their titles and how many relations point at the Artist.
func (ArtistRelations) ProgramEventsOfArtist(ctx context.Context, db *gorm.DB, artistID string) ([]entityparticipant.Related, error) {
	var rows []entityparticipant.Related
	err := db.WithContext(ctx).Raw(`
		SELECT program_event.id::text AS id,
		       program_event.title AS display_name,
		       COUNT(*)::integer AS relation_count
		FROM (
			SELECT event_id FROM program_event_artist WHERE artist_id = ?::uuid
			UNION ALL
			SELECT event_id FROM program_event_credit WHERE artist_id = ?::uuid
		) AS relation
		JOIN program_event ON program_event.id = relation.event_id
		GROUP BY program_event.id, program_event.title
	`, artistID, artistID).Scan(&rows).Error
	return rows, err
}
