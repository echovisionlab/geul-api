package domain

import (
	"errors"
	"time"
)

var ErrFileNotFound = errors.New("file not found")

type File struct {
	ClientMediaBundleID *string
	ID                  string
	FileName            string
	MimeType            string
	FileSize            int64
	Extension           string
	SHA256              []byte
	DurationSeconds     *int
	IngestSlotID        *string
	IngestAttemptID     *string
	FolderID            *string
	UploadedByMemberID  *string
	DeleteRequestedAt   *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}
