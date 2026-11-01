package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// DownloadAccessResolver decides the download access of each requested
// Track original and signs the downloads it allows, holding the Release,
// share link and Track fences through signing.
type DownloadAccessResolver interface {
	Resolve(
		ctx context.Context,
		releaseID string,
		expectedReleaseStatus string,
		ownerAuthorization mediaasset.ContentDownloadOwnerAuthorization,
		requests []TrackDownloadAccessRequest,
		signer TrackDownloadSigner,
	) (map[string]TrackDownloadAuthorization, error)
}

// TrackDownloadSigner signs the download of one File.
type TrackDownloadSigner func(MediaFile) (*commonv1.ExpiringMediaRef, error)

// DownloadSigner builds signed delivery URLs of Track originals.
type DownloadSigner interface {
	DownloadRef(MediaFile) (*commonv1.ExpiringMediaRef, error)
}

// TrackDownloadAuthorization is the download access of one Track original
// and, when it allows the download, its signed reference.
type TrackDownloadAuthorization struct {
	Access   *DownloadAccess
	Download *commonv1.ExpiringMediaRef
}

// TrackDownloadAccessRequest asks for the download access of a Track's
// original File.
type TrackDownloadAccessRequest struct {
	TrackID string
	FileID  string
}

// MediaFile is a Track's original audio File.
type MediaFile struct {
	ID        string
	Extension string
	MimeType  string
	FileSize  int64
	FileName  *string
}

// DownloadAvailability tells whether a download is offered.
type DownloadAvailability uint8

const (
	DownloadUnavailable DownloadAvailability = iota + 1
	DownloadAvailable
)

// DownloadAction is what a viewer does to download.
type DownloadAction uint8

const (
	DownloadActionNone DownloadAction = iota + 1
	DownloadActionDownload
	DownloadActionSignIn
)

// DownloadAccess is a viewer's access to a download.
type DownloadAccess struct {
	Availability DownloadAvailability
	Action       DownloadAction
}

var unavailableDownloadAccess = DownloadAccess{Availability: DownloadUnavailable, Action: DownloadActionNone}
