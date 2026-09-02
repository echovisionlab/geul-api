package post

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// FileService is the Post-owned File capability. Singular private delivery
// re-establishes the exact Post owner and featured-image slot at issuance;
// summary projection is limited to immutable public display assets.
type FileService interface {
	DeleteFileByID(context.Context, string) error
	ResolveAuthorizedPostFeaturedImage(context.Context, string, string) (*commonv1.MediaDelivery, error)
	ResolvePublicDisplayMedia(context.Context, []string) (map[string]*commonv1.MediaDelivery, error)
}

// AsyncPublisher is the transport surface required by Post domain events.
type AsyncPublisher interface {
	EnqueueProtobuf(context.Context, string, string, proto.Message) error
	NotifyProtobuf(context.Context, string, proto.Message) error
}

type CollaborationPermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
}

// ShareLinkValidator keeps the shared ShareLink proof implementation outside
// Post while Post retains the decision about when a private read may use it.
type ShareLinkValidator interface {
	ValidateShareLinkForEntity(
		context.Context,
		*gorm.DB,
		string,
		string,
		managev1.ShareLinkEntityType,
		string,
	) (*model.ShareLink, error)
}

// ContentBlockMediaLoader loads persisted attachment references only after a
// Post operation has authorized the document.
type ContentBlockMediaLoader interface {
	LoadContentBlockMediaReferences(context.Context, *gorm.DB, uuid.UUID) ([]*contentv1.ContentBlockMediaItem, error)
}

// MemberSummaryLoader supplies Member-owned manage projections after Post has
// resolved the durable author, collaborator, or commenter identities.
type MemberSummaryLoader interface {
	LoadMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
}

type VersionRestoreSupport interface {
	LoadUnavailableVersionAttachmentKinds(
		context.Context,
		*gorm.DB,
		proto.Message,
	) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error)
}

type translationLocaleDocumentState struct {
	Title       *string
	Summary     *string
	ContentJSON []byte
	ContentHTML *string
	ContentText *string
	OgAssetID   *string
}

type translationLocaleDocumentSaveInput struct {
	Title   *string
	Summary *string
	Now     time.Time
}
