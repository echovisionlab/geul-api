package domain

import (
	"context"
	"errors"
	"time"
)

var ErrIngestBindingNotFound = errors.New("file ingest binding not found")

type IngestReceiptStore interface {
	HasFileUploadSessions(context.Context, string) (bool, error)
	IngestFile(context.Context, string) (File, error)
	IngestBinding(context.Context, string) (IngestBinding, error)
	FailFinalizingUpload(context.Context, string, time.Time) error
}
