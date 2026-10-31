package artist

import (
	entityparticipantadapter "github.com/echovisionlab/geul-api/internal/adapters/entityparticipant"
	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	"gorm.io/gorm"
)

// NewTranslation adapts shared translation authority to the Artist domain.
func NewTranslation() *entityparticipantadapter.Translation {
	return entityparticipantadapter.NewTranslation("Artist")
}

// NewMemberProjection adapts Member-owned summaries to Artist participant reads
// and mutation responses.
func NewMemberProjection(db *gorm.DB, cdnDomain string) *entityparticipantadapter.MemberProjection {
	return entityparticipantadapter.NewMemberProjection("artist", db, cdnDomain)
}

var (
	_ artistdomain.Translation      = (*entityparticipantadapter.Translation)(nil)
	_ artistdomain.MemberProjection = (*entityparticipantadapter.MemberProjection)(nil)
)
