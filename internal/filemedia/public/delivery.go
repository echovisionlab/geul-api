package public

import (
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"

	"github.com/echovisionlab/geul-api/internal/mediaauth"
)

type MediaEntityType int32

const (
	EntityUnspecified MediaEntityType = iota
	EntityPost
	EntityPage
	EntityWork
	EntityProgramEvent
	EntityRelease
)

type DownloadAvailability int32

const (
	DownloadAvailabilityUnspecified DownloadAvailability = iota
	DownloadAvailable
	DownloadUnavailable
)

type DownloadAction int32

const (
	ActionUnspecified DownloadAction = iota
	ActionDownload
	ActionSignIn
	ActionNone
)

type DownloadAccess struct {
	Availability DownloadAvailability
	Action       DownloadAction
}
type DownloadResult struct {
	Access   *DownloadAccess
	Download *mediaauth.Reference
}
type HLSReference = domain.HLSReference
type Delivery = domain.Delivery
type ContentMediaItem struct {
	FileID               string
	Selector             *ContentBlockFileSelector
	Delivery             *Delivery
	DownloadAvailability DownloadAvailability
	DownloadAction       DownloadAction
}

func downloadAction(access *DownloadAccess) DownloadAction {
	if access == nil {
		return ActionUnspecified
	}
	return access.Action
}

const (
	ownerPostPublished         = "POST_STATUS_PUBLISHED"
	ownerPostArchived          = "POST_STATUS_ARCHIVED"
	ownerPagePublished         = "PAGE_STATUS_PUBLISHED"
	ownerWorkPublished         = "WORK_STATUS_PUBLISHED"
	ownerWorkArchived          = "WORK_STATUS_ARCHIVED"
	ownerProgramEventPublished = "PROGRAM_EVENT_STATUS_PUBLISHED"
	ownerProgramEventArchived  = "PROGRAM_EVENT_STATUS_ARCHIVED"
	ownerReleasePublished      = "RELEASE_STATUS_PUBLISHED"
	derivativeThumbnail        = "FILE_DERIVATIVE_TYPE_THUMBNAIL"
	derivativeSpectrogram      = "FILE_DERIVATIVE_TYPE_SPECTROGRAM"
	derivativeWaveform         = "FILE_DERIVATIVE_TYPE_WAVEFORM"
	derivativeHls              = "FILE_DERIVATIVE_TYPE_HLS"
)
