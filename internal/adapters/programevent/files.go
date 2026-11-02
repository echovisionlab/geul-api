package programevent

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type manageFiles interface {
	DeleteFileByID(context.Context, string) error
	HydrateAuthorizedProgramEventBlockMediaWithDB(
		context.Context,
		*gorm.DB,
		string,
		uuid.UUID,
		*auth.UserInfo,
		[]*fileref.BlockMediaItem,
	) ([]*fileref.BlockMediaItem, error)
}

// Files adapts the shared File runtime to Program Event manage/internal ports.
type Files struct {
	files manageFiles
}

func NewFiles(files manageFiles) *Files {
	if files == nil {
		panic("Program Event File adapter dependency is required")
	}
	return &Files{files: files}
}

func (a *Files) DeleteFileByID(ctx context.Context, fileID string) error {
	return a.files.DeleteFileByID(ctx, fileID)
}

func (a *Files) HydrateAuthorizedProgramEventBlockMediaWithDB(
	ctx context.Context,
	tx *gorm.DB,
	eventID string,
	documentID uuid.UUID,
	principal *auth.UserInfo,
	items []*fileref.BlockMediaItem,
) ([]*fileref.BlockMediaItem, error) {
	return a.files.HydrateAuthorizedProgramEventBlockMediaWithDB(
		ctx, tx, eventID, documentID, principal, items,
	)
}

type publicFiles interface {
	HydrateAuthorizedContentBlockMedia(context.Context, []*fileref.BlockMediaItem) ([]*fileref.BlockMediaItem, error)
}

// PublicFiles adapts request-scoped File delivery after Program Event has
// authorized and loaded the exact persisted Block attachment references.
type PublicFiles struct {
	files publicFiles
}

func NewPublicFiles(files publicFiles) *PublicFiles {
	if files == nil {
		panic("Program Event public File adapter dependency is required")
	}
	return &PublicFiles{files: files}
}

func (a *PublicFiles) HydrateAuthorizedContentBlockMedia(
	ctx context.Context,
	items []*fileref.BlockMediaItem,
) ([]*fileref.BlockMediaItem, error) {
	return a.files.HydrateAuthorizedContentBlockMedia(ctx, items)
}
