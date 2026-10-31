package public

import (
	"github.com/echovisionlab/geul-api/internal/model"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// publicArtistStatus maps a stored Artist status to the public API enum. A
// value the API does not know is UNSPECIFIED.
func publicArtistStatus(status model.ArtistStatus) openv1.ArtistStatus {
	switch status {
	case model.ArtistStatusDraft:
		return openv1.ArtistStatus_ARTIST_STATUS_DRAFT
	case model.ArtistStatusPublished:
		return openv1.ArtistStatus_ARTIST_STATUS_PUBLISHED
	default:
		return openv1.ArtistStatus_ARTIST_STATUS_UNSPECIFIED
	}
}
