package domain

import (
	"context"
	"errors"
)

var ErrUploadTrackNotFound = errors.New("upload Track not found")

type VerifiedIngestStore interface {
	// EnsureIngestDateFolder creates the current date hierarchy when absent.
	EnsureIngestDateFolder(context.Context) (*string, error)
	InsertIngestFile(context.Context, File) error
	InsertIngestBinding(context.Context, IngestBinding, bool) error
	LockIngestOwner(context.Context, string, string) error
	UploadOwnerExists(context.Context, string, string) (bool, error)
}
