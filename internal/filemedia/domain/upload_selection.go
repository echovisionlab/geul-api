package domain

import (
	"context"
	"time"
)

type UploadTargetMode uint8

const (
	UploadTargetUnknown UploadTargetMode = iota
	UploadTargetGeneral
	UploadTargetEditorFile
	UploadTargetTrackProjection
)

type UploadSearch struct {
	UploadType                           string
	EntityID                             string
	EntityType                           *string
	Mode                                 UploadTargetMode
	SlotID                               string
	ExpectedCurrentFileID                *string
	FileID, UploadID, FileName, MimeType string
	FileSize, FileLastModified           *int64
	ActiveAfter                          time.Time
	ChunkSize                            int32
}
type UploadSelectionStore interface {
	ActiveSurfaceUploads(context.Context, UploadSearch) ([]UploadSession, error)
	FindUploadCandidate(context.Context, UploadSearch) (UploadSession, bool, error)
}
