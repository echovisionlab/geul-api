package fileref

import (
	"slices"

	"github.com/echovisionlab/geul-api/internal/optional"
)

type BlockMediaSelector struct {
	BlockID, ReferencePath string
	Extensions             []byte
}
type MissingMediaKind int32
type MissingAttachment struct {
	Extensions   []byte
	FormerFileID string
	MediaKind    MissingMediaKind
}
type AttachmentKind int32

const (
	AttachmentUnspecified AttachmentKind = iota
	AttachmentActive
	AttachmentMissing
)

type Attachment struct {
	Extensions []byte
	Kind       AttachmentKind
	FileID     string
	Missing    *MissingAttachment
}

func (attachment *Attachment) ActiveFileID() string {
	if attachment == nil || attachment.Kind != AttachmentActive {
		return ""
	}
	return attachment.FileID
}
func (attachment *Attachment) Clone() *Attachment {
	if attachment == nil {
		return nil
	}
	result := *attachment
	result.Extensions = slices.Clone(attachment.Extensions)
	result.Missing = optional.Clone(attachment.Missing)
	if result.Missing != nil {
		result.Missing.Extensions = slices.Clone(attachment.Missing.Extensions)
	}
	return &result
}

type DownloadAvailability int32

const (
	DownloadAvailabilityUnspecified DownloadAvailability = iota
	DownloadAvailable
	DownloadUnavailable
)

type DownloadAction int32

const (
	DownloadActionUnspecified DownloadAction = iota
	Download
	SignIn
	NoDownload
)

type BlockMediaItem struct {
	// Extensions preserves unrecognized reference data during hydration.
	Extensions           []byte
	Selector             *BlockMediaSelector
	Attachment           *Attachment
	Delivery             *Delivery
	DownloadAvailability DownloadAvailability
	DownloadAction       DownloadAction
}

func (item *BlockMediaItem) Clone() *BlockMediaItem {
	if item == nil {
		return nil
	}
	result := *item
	result.Extensions = slices.Clone(item.Extensions)
	result.Selector = optional.Clone(item.Selector)
	if result.Selector != nil {
		result.Selector.Extensions = slices.Clone(item.Selector.Extensions)
	}
	result.Attachment = item.Attachment.Clone()
	result.Delivery = item.Delivery.Clone()
	return &result
}
