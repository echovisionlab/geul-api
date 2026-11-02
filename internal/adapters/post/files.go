package post

import (
	"context"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	filemediadomain "github.com/echovisionlab/geul-api/internal/filemedia"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	postdomain "github.com/echovisionlab/geul-api/internal/post"

	postpublic "github.com/echovisionlab/geul-api/internal/post/public"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

type manageFiles interface {
	DeleteFileByID(context.Context, string) error
	ResolveAuthorizedPostFeaturedImage(context.Context, string, string) (*filemediadomain.Delivery, error)
}

type displayFiles interface {
	ResolveDisplayMedia(context.Context, []string) (map[string]*fileref.Delivery, error)
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
) (*fileref.Delivery, error) {
	value, err := a.files.ResolveAuthorizedPostFeaturedImage(ctx, postID, expectedFileID)
	return value, err
}

func (a *Files) ResolveDisplayMedia(
	ctx context.Context,
	fileIDs []string,
) (map[string]*fileref.Delivery, error) {
	return a.display.ResolveDisplayMedia(ctx, fileIDs)
}

type publicFiles interface {
	ResolveDisplayMedia(context.Context, []string) (map[string]*fileref.Delivery, error)
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

func (a *PublicFiles) ResolveDisplayMedia(
	ctx context.Context,
	fileIDs []string,
) (map[string]*fileref.Delivery, error) {
	return a.files.ResolveDisplayMedia(ctx, fileIDs)
}

func (a *PublicFiles) ResolveAuthorizedContentBlockMedia(
	ctx context.Context,
	documentID uuid.UUID,
) ([]*contentv1.ContentBlockMediaItem, error) {
	items, err := contentblock.LoadMediaReferences(ctx, a.db, documentID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return a.files.HydrateAuthorizedContentBlockMedia(ctx, items)
}

var (
	_ postdomain.FileService = (*Files)(nil)
	_ postpublic.FileService = (*PublicFiles)(nil)
)
