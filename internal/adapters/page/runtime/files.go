package runtime

import (
	"context"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	filemediadomain "github.com/echovisionlab/geul-api/internal/filemedia"

	pagedomain "github.com/echovisionlab/geul-api/internal/page"
)

// ManageFiles is the shared File capability used by Page after authorization.
type ManageFiles interface {
	ResolveAuthorizedPageFeaturedImage(context.Context, string, string) (*filemediadomain.Delivery, error)
}

// Files adapts the shared File service to Page-owned manage ports.
type Files struct{ files ManageFiles }

func NewFiles(files ManageFiles) *Files {
	if files == nil {
		panic("Page File adapter dependency is required")
	}
	return &Files{files: files}
}

func (a *Files) ResolveAuthorizedPageFeaturedImage(
	ctx context.Context,
	pageID string,
	expectedFileID string,
) (*fileref.Delivery, error) {
	value, err := a.files.ResolveAuthorizedPageFeaturedImage(ctx, pageID, expectedFileID)
	return value, err
}

var _ pagedomain.FileDeliveryResolver = (*Files)(nil)
