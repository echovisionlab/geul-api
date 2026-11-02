package runtime

import (
	"context"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"

	postpostgres "github.com/echovisionlab/geul-api/internal/post/postgres"

	postpublicpostgres "github.com/echovisionlab/geul-api/internal/post/public/postgres"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// ShareLinks adapts the shared proof verifier to Post's consumer-owned port.
type ShareLinks struct{}

func (ShareLinks) ValidateShareLinkForEntity(
	ctx context.Context,
	db *gorm.DB,
	token string,
	password string,
	entityType managev1.ShareLinkEntityType,
	entityID string,
) (*model.ShareLink, error) {
	return sharelinkadapter.ValidateForEntity(ctx, db, token, password, entityType, entityID)
}

// ContentBlockMedia loads persisted attachment references after Post has
// authorized the owning document.
type ContentBlockMedia struct{}

func (ContentBlockMedia) LoadContentBlockMediaReferences(
	ctx context.Context,
	db *gorm.DB,
	documentID uuid.UUID,
) ([]*fileref.BlockMediaItem, error) {
	return contentblock.LoadMediaReferences(ctx, db, documentID)
}

// VersionRestore provides the shared File attachment availability checks used
// by Post version restore. Post owns source-locale and document revision state.
type VersionRestore struct{}

func (VersionRestore) LoadUnavailableVersionAttachmentKinds(
	ctx context.Context,
	tx *gorm.DB,
	document proto.Message,
) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error) {
	return mediaasset.LoadUnavailableVersionAttachmentKinds(ctx, mediaassetpostgres.NewAttachmentStore(tx), document)
}

var (
	_ postpublicpostgres.ShareLinkValidator = ShareLinks{}
	_ postpostgres.ContentBlockMediaLoader  = ContentBlockMedia{}
	_ postpostgres.VersionRestoreSupport    = VersionRestore{}
)
