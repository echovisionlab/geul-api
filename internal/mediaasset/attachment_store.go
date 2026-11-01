package mediaasset

import (
	"context"
	"time"
)

type VersionFile struct {
	ID                string
	MIMEType          string
	DeleteRequestedAt *time.Time
}
type AttachmentStore interface {
	LockVersionFiles(context.Context, []string) ([]VersionFile, error)
}
