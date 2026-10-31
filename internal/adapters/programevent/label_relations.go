package programevent

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"gorm.io/gorm"
)

// LabelRelations reads the Program Event side of Program Event-Label links
// for Label.
type LabelRelations struct{}

// NewLabelRelations returns the Program Event reader of Label links.
func NewLabelRelations() LabelRelations { return LabelRelations{} }

// ProgramEventsOfLabel lists the Program Events linked to the Label with
// their titles, or slugs when untitled.
func (LabelRelations) ProgramEventsOfLabel(ctx context.Context, db *gorm.DB, labelID string) ([]entityparticipant.Related, error) {
	var rows []entityparticipant.Related
	err := db.WithContext(ctx).Raw(`
		SELECT event.id::text AS id, COALESCE(NULLIF(event.title, ''), event.slug) AS display_name
		FROM program_event_label AS relation
		JOIN program_event AS event ON event.id = relation.event_id
		WHERE relation.label_id = ?::uuid
		ORDER BY event.id::text
	`, labelID).Scan(&rows).Error
	return rows, err
}
