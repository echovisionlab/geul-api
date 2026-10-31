package artist

import (
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// artistStatusToProto maps a stored Artist status to the API enum. A value
// the API does not know is UNSPECIFIED.
func artistStatusToProto(status model.ArtistStatus) managev1.ArtistStatus {
	switch status {
	case model.ArtistStatusDraft:
		return managev1.ArtistStatus_ARTIST_STATUS_DRAFT
	case model.ArtistStatusPublished:
		return managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED
	default:
		return managev1.ArtistStatus_ARTIST_STATUS_UNSPECIFIED
	}
}
