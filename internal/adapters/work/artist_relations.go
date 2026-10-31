package work

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"gorm.io/gorm"
)

// workSourceTitleSQL is the title of the Work aliased work in its source
// locale.
const workSourceTitleSQL = `COALESCE((SELECT wt.title FROM work_translation AS wt JOIN work AS source ON source.id = wt.entity_id AND source.source_locale = wt.locale WHERE wt.entity_id = work.id LIMIT 1), '')`

// ArtistRelations reads the Works that credit an Artist, for Artist.
type ArtistRelations struct{}

// NewArtistRelations returns the Work reader of Artist credits.
func NewArtistRelations() ArtistRelations { return ArtistRelations{} }

// WorksOfArtist lists the Works that credit the Artist, with their source
// titles and credit counts.
func (ArtistRelations) WorksOfArtist(ctx context.Context, db *gorm.DB, artistID string) ([]entityparticipant.Related, error) {
	var rows []entityparticipant.Related
	err := db.WithContext(ctx).Raw(`
		SELECT work.id::text AS id,
		       `+workSourceTitleSQL+` AS display_name,
		       COUNT(*)::integer AS relation_count
		FROM work_credit
		JOIN work ON work.id = work_credit.work_id
		WHERE work_credit.artist_id = ?::uuid
		GROUP BY work.id
	`, artistID).Scan(&rows).Error
	return rows, err
}
