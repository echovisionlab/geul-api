package domain

import (
	"slices"
	"strconv"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaauth"
)

type ProcessingStatus int32

const (
	ProcessingUnspecified ProcessingStatus = iota
	ProcessingActive
	ProcessingReady
	ProcessingFailed
)

func (s ProcessingStatus) String() string {
	switch s {
	case ProcessingUnspecified:
		return "MEDIA_PROCESSING_STATUS_UNSPECIFIED"
	case ProcessingActive:
		return "MEDIA_PROCESSING_STATUS_PROCESSING"
	case ProcessingReady:
		return "MEDIA_PROCESSING_STATUS_READY"
	case ProcessingFailed:
		return "MEDIA_PROCESSING_STATUS_FAILED"
	default:
		return strconv.FormatInt(int64(s), 10)
	}
}

type HLSReference struct{ FileID, GenerationID, URL string }
type Delivery struct {
	ProcessingStatus                        ProcessingStatus
	ProcessingPercentage                    *int32
	FileID, Extension, MimeType             string
	FileSize                                int64
	FileName                                *string
	DurationSeconds                         *int32
	Asset, Thumbnail, Spectrogram, Waveform *mediaasset.Reference
	Playback                                *HLSReference
	Inline, Download                        *mediaauth.Reference
}

func cloneAsset(value *mediaasset.Reference) *mediaasset.Reference {
	if value == nil {
		return nil
	}
	result := *value
	result.DownloadFilename = clonePointer(value.DownloadFilename)
	result.SHA256 = slices.Clone(value.SHA256)
	return &result
}
func (d *Delivery) Clone() *Delivery {
	if d == nil {
		return nil
	}
	result := *d
	result.ProcessingPercentage = clonePointer(d.ProcessingPercentage)
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
