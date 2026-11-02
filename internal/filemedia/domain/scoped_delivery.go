package domain

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
)

type Joined struct {
	Store Store
	Audit domainaudit.Appender
}
type ScopedAttachment struct {
	BlockID       string
	ReferencePath string
	FileID        string
}

func (r ScopedAttachment) Key() string {
	return r.BlockID + "\x00" + r.ReferencePath + "\x00" + r.FileID
}

type ScopedDeliveryStore interface {
	FeaturedFileID(context.Context, string, string) (*string, bool, error)
	LockScopedFiles(context.Context, []string) (map[string]File, error)
	LockScopedAttachments(context.Context, string, string, string, []string) (map[string]ScopedAttachment, error)
}
