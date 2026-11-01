package domain

import (
	"context"
	"errors"
	"time"
)

type UploadSessionStatus string

const (
	UploadSessionStatusInitiated  UploadSessionStatus = "initiated"
	UploadSessionStatusUploading  UploadSessionStatus = "uploading"
	UploadSessionStatusFinalizing UploadSessionStatus = "finalizing"
	UploadSessionStatusFailed     UploadSessionStatus = "failed"
	UploadSessionStatusAborted    UploadSessionStatus = "aborted"
)

type UploadSession struct {
	UploadID            string
	ClientMediaBundleID *string
	ClientMediaManifest *string
	FileID              string
	UploadType          string
	EntityID            string
	EntityType          *string
	FileName            string
	FileSize            int64
	FileLastModified    *int64
	SlotID              *string
	AttemptID           *string
	ExpectedFileID      *string
	IngestSequence      int64
	RequestedMime       string
	DetectedMime        *string
	TotalParts          int32
	ChunkSize           int32
	Status              UploadSessionStatus
	VerifiedAt          *time.Time
	LastActivityAt      time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type UploadPart struct {
	UploadID   string
	PartNumber int32
	ETag       string
	Size       int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

var ErrUploadSessionNotFound = errors.New("upload session not found")

type UploadSessionStore interface {
	UploadSession(context.Context, string, string, bool) (UploadSession, error)
	UploadParts(context.Context, string) ([]UploadPart, error)
	RenewUploadActivity(context.Context, string, time.Time) error
	RenewActiveUploadActivity(context.Context, string, string, bool, time.Time) (bool, error)
	NextUploadIngestSequence(context.Context, string) (int64, error)
	ClaimUploadFinalizing(context.Context, string, string, time.Time) (bool, error)
	ClaimUploadAborted(context.Context, string, string, time.Time) (bool, error)
	DeleteFinalizingUpload(context.Context, string) (bool, error)
	DeleteAbortedUpload(context.Context, string) (bool, error)
}
