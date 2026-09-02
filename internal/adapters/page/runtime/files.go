package runtime

import (
	"context"

	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// ManageFiles is the shared File capability used by Page after authorization.
type ManageFiles interface {
	DeleteFileByID(context.Context, string) error
	ResolveAuthorizedPageFeaturedImage(context.Context, string, string) (*commonv1.MediaDelivery, error)
}

// Files adapts the shared File service to Page-owned manage ports.
type Files struct{ files ManageFiles }

func NewFiles(files ManageFiles) *Files {
	if files == nil {
		panic("Page File adapter dependency is required")
	}
	return &Files{files: files}
}

func (a *Files) DeleteFileByID(ctx context.Context, fileID string) error {
	return a.files.DeleteFileByID(ctx, fileID)
}

func (a *Files) ResolveAuthorizedPageFeaturedImage(
	ctx context.Context,
	pageID string,
	expectedFileID string,
) (*commonv1.MediaDelivery, error) {
	return a.files.ResolveAuthorizedPageFeaturedImage(ctx, pageID, expectedFileID)
}

var (
	_ pagedomain.FileDeleter          = (*Files)(nil)
	_ pagedomain.FileDeliveryResolver = (*Files)(nil)
)
