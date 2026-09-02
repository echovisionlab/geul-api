package page

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// AsyncPublisher is the transport capability used by Page mutations.
type AsyncPublisher interface {
	EnqueueProtobuf(context.Context, string, string, proto.Message) error
	NotifyProtobuf(context.Context, string, proto.Message) error
}

// FileDeleter removes Page-owned File bindings during Page deletion.
type FileDeleter interface {
	DeleteFileByID(context.Context, string) error
}

// FileDeliveryResolver projects exact File IDs after Page authorization.
type FileDeliveryResolver interface {
	ResolveAuthorizedPageFeaturedImage(context.Context, string, string) (*commonv1.MediaDelivery, error)
}

// OGRequests is the narrow OG lifecycle used by Page mutations.
type OGRequests interface {
	RequestCurrentWithDB(context.Context, *gorm.DB, managev1.OgEntityType, string, string, bool, string) (string, error)
	CancelAndReleaseEntityWithDB(context.Context, *gorm.DB, managev1.OgEntityType, string, string) error
}

// MediaAssets is the narrow File/media projection lifecycle used by Page.
type MediaAssets interface {
	LockAttachableFilesForUpdate(context.Context, *gorm.DB, []string) error
	ReleasePublicAssetBindings(context.Context, *gorm.DB, string, string, string) error
	ReleaseExactPublicAssetBindings(context.Context, *gorm.DB, string, string, []string) error
	ResolveReadyAssetRefs(context.Context, *gorm.DB, []string) (map[string]*commonv1.AssetRef, error)
	LoadUnavailableVersionAttachmentKinds(context.Context, *gorm.DB, proto.Message) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error)
}

// Runtime contains only shared OG and media capabilities required by Page.
type Runtime interface {
	OGRequests
	MediaAssets
}

// CollaborationPermissionChecker is the fully consistent authorization read
// required at collaboration mutation fences.
type CollaborationPermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
	CheckActorCan(context.Context, policyv1.Actor, policyv1.Can) (bool, error)
}

// ContentBlockMediaHydrator adds request-scoped delivery to authorized Page
// Block media references.
type ContentBlockMediaHydrator interface {
	HydrateAuthorizedPageBlockMediaWithDB(
		context.Context,
		*gorm.DB,
		string,
		uuid.UUID,
		*auth.UserInfo,
		[]*contentv1.ContentBlockMediaItem,
	) ([]*contentv1.ContentBlockMediaItem, error)
}

// MenuTargets owns cross-domain Menu reference rewrites triggered by Page
// slug changes and deletion.
type MenuTargets interface {
	UpdateSlug(context.Context, *gorm.DB, string, string, string, string) error
	Remove(context.Context, *gorm.DB, string, string, string) error
}
