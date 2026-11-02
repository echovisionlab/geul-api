package domain

import (
	"time"

	"github.com/echovisionlab/geul-api/internal/fileref"
)

type MediaDeliveryResult struct {
	Delivery                                           *fileref.Delivery
	IngestSlotID, IngestAttemptID, ClientMediaBundleID *string
}

func (r *MediaDeliveryResult) Clone() *MediaDeliveryResult {
	if r == nil {
		return nil
	}
	result := *r
	result.Delivery = r.Delivery.Clone()
	result.IngestSlotID = clonePointer(r.IngestSlotID)
	result.IngestAttemptID = clonePointer(r.IngestAttemptID)
	result.ClientMediaBundleID = clonePointer(r.ClientMediaBundleID)
	return &result
}

type CompletedUpload struct {
	Delivery *fileref.Delivery
	FileID   string
}
type ImportedFile struct {
	Delivery                                       *fileref.Delivery
	FileID                                         string
	SlotID, IngestAttemptID, ExpectedCurrentFileID *string
}

type LibraryFile struct {
	ID, FileName, Extension, MimeType string
	FileSize                          int64
	DurationSeconds                   *int32
	FolderID                          *string
	UploadedByMember                  *MemberSummary
	CreatedAt, UpdatedAt              *time.Time
	Delivery                          *fileref.Delivery
	UsageCount                        int32
}

type GeneratedOutput struct {
	ID       string
	Type     DerivativeKind
	Status   fileref.ProcessingStatus
	Delivery *fileref.Delivery
}

func (o *GeneratedOutput) Clone() *GeneratedOutput {
	if o == nil {
		return nil
	}
	result := *o
	result.Delivery = o.Delivery.Clone()
	return &result
}

type FileDetails struct {
	File             *LibraryFile
	UsageSummary     []*UsageView
	GeneratedOutputs []*GeneratedOutput
}

type LibraryItemType int32

const (
	LibraryItemUnspecified LibraryItemType = iota
	LibraryItemFolder
	LibraryItemFile
)

type FolderPathSegment struct{ ID, Name string }
type LibraryItem struct {
	Type       LibraryItemType
	Folder     *FolderView
	File       *LibraryFile
	FolderPath []*FolderPathSegment
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	return new(*value)
}

type ScopedContentMediaItem struct {
	FileID, BlockID, ReferencePath string
	Delivery                       *fileref.Delivery
	DownloadAllowed                bool
}
