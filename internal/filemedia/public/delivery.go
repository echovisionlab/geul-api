package public

import (
	"slices"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
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
type HLSReference struct{ FileID, GenerationID, URL string }
type Delivery struct {
	FileID, Extension, MimeType             string
	FileSize                                int64
	FileName                                *string
	DurationSeconds                         *int32
	Asset, Thumbnail, Spectrogram, Waveform *mediaasset.Reference
	Playback                                *HLSReference
	Inline, Download                        *mediaauth.Reference
}
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
func cloneAsset(value *mediaasset.Reference) *mediaasset.Reference {
	if value == nil {
		return nil
	}
	result := *value
	result.SHA256 = slices.Clone(value.SHA256)
	return &result
}
func (d *Delivery) Clone() *Delivery {
	if d == nil {
		return nil
	}
	result := *d
	if d.FileName != nil {
		v := *d.FileName
		result.FileName = &v
	}
	if d.DurationSeconds != nil {
		v := *d.DurationSeconds
		result.DurationSeconds = &v
	}
	result.Asset = cloneAsset(d.Asset)
	result.Thumbnail = cloneAsset(d.Thumbnail)
	result.Spectrogram = cloneAsset(d.Spectrogram)
	result.Waveform = cloneAsset(d.Waveform)
	if d.Playback != nil {
		v := *d.Playback
		result.Playback = &v
	}
	result.Inline = d.Inline.Clone()
	result.Download = d.Download.Clone()
	return &result
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
