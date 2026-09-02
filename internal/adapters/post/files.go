package post

import (
	"context"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	postpublic "github.com/echovisionlab/geul-api/internal/post/public"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

type manageFiles interface {
	DeleteFileByID(context.Context, string) error
	ResolveAuthorizedPostFeaturedImage(context.Context, string, string) (*commonv1.MediaDelivery, error)
}

type displayFiles interface {
	ResolvePublicDisplayMedia(context.Context, []string) (map[string]*commonv1.MediaDelivery, error)
}

// Files adapts File Media's authorized delivery surface to Post's manage port.
type Files struct {
	files   manageFiles
	display displayFiles
}

func NewFiles(files manageFiles, display displayFiles) *Files {
	if files == nil || display == nil {
		panic("post files adapter: manage and public display files are required")
	}
	return &Files{files: files, display: display}
}

func (a *Files) DeleteFileByID(ctx context.Context, fileID string) error {
	return a.files.DeleteFileByID(ctx, fileID)
}

func (a *Files) ResolveAuthorizedPostFeaturedImage(
	ctx context.Context,
	postID string,
	expectedFileID string,
) (*commonv1.MediaDelivery, error) {
	return a.files.ResolveAuthorizedPostFeaturedImage(ctx, postID, expectedFileID)
}

func (a *Files) ResolvePublicDisplayMedia(
	ctx context.Context,
	fileIDs []string,
) (map[string]*commonv1.MediaDelivery, error) {
	return a.display.ResolvePublicDisplayMedia(ctx, fileIDs)
}

type publicFiles interface {
	ResolvePublicDisplayMedia(context.Context, []string) (map[string]*commonv1.MediaDelivery, error)
	HydrateAuthorizedContentBlockMedia(
		context.Context,
		[]*contentv1.ContentBlockMediaItem,
	) ([]*contentv1.ContentBlockMediaItem, error)
}

// PublicFiles keeps Post's authorization decision ahead of File Media lookup:
// Post supplies the authorized document ID, this adapter loads its exact active
// references, and the public File service adds request-scoped delivery.
type PublicFiles struct {
	db    *gorm.DB
	files publicFiles
}

func NewPublicFiles(db *gorm.DB, files publicFiles) *PublicFiles {
	if db == nil || files == nil {
		panic("post public files adapter: db and files are required")
	}
	return &PublicFiles{db: db, files: files}
}

func (a *PublicFiles) ResolvePublicDisplayMedia(
	ctx context.Context,
	fileIDs []string,
) (map[string]*commonv1.MediaDelivery, error) {
	return a.files.ResolvePublicDisplayMedia(ctx, fileIDs)
}

func (a *PublicFiles) ResolveAuthorizedContentBlockMedia(
	ctx context.Context,
	documentID uuid.UUID,
) ([]*contentv1.ContentBlockMediaItem, error) {
	items, err := filemedia.LoadContentBlockMediaReferences(ctx, a.db, documentID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return a.files.HydrateAuthorizedContentBlockMedia(ctx, items)
}

var (
	_ postdomain.FileService = (*Files)(nil)
	_ postpublic.FileService = (*PublicFiles)(nil)
)
