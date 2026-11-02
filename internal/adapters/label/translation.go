package label

import (
	entityparticipantadapter "github.com/echovisionlab/geul-api/internal/adapters/entityparticipant"
	"gorm.io/gorm"
)

// NewTranslation adapts shared translation authority to the Label domain.
func NewTranslation() *entityparticipantadapter.Translation {
	return entityparticipantadapter.NewTranslation("Label")
}

// NewMemberProjection adapts Member-owned summaries to Label participant reads
// and mutation responses.
func NewMemberProjection(db *gorm.DB, cdnDomain string) *entityparticipantadapter.MemberProjection {
	return entityparticipantadapter.NewMemberProjection("label", db, cdnDomain)
}
