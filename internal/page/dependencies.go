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

// Members reads the Member and user tag rows Page authorization and
// attribution need; Member owns them.
type Members interface {
	// LockMembers takes a KEY SHARE lock on each existing Member of
	// memberIDs and returns the IDs it locked.
	LockMembers(ctx context.Context, tx *gorm.DB, memberIDs []string) ([]string, error)
	// ActiveIdentities returns the linked Identity of each onboarded, live
	// Member of memberIDs whose Identity is active and not banned.
	ActiveIdentities(ctx context.Context, tx *gorm.DB, memberIDs []string) (map[string]string, error)
	// Nicknames returns the nickname of each existing Member of memberIDs.
	Nicknames(ctx context.Context, db *gorm.DB, memberIDs []string) (map[string]string, error)
	// PreferredLocale returns the preferred locale of a live Member, or nil.
	PreferredLocale(ctx context.Context, db *gorm.DB, memberID string) (*string, error)
	// LockUserTags takes a SHARE lock on each existing user tag of tagIDs
	// and returns the IDs it locked.
	LockUserTags(ctx context.Context, tx *gorm.DB, tagIDs []string) ([]string, error)
}

// ShareLinks removes a deleted Page's share links in its transaction;
// Share Link owns them.
type ShareLinks interface {
	DeletePageShareLinks(ctx context.Context, tx *gorm.DB, pageID string) error
}

// MenuTargets owns cross-domain Menu reference rewrites triggered by Page
// slug changes and deletion.
type MenuTargets interface {
	UpdateSlug(context.Context, *gorm.DB, string, string, string, string) error
	Remove(context.Context, *gorm.DB, string, string, string) error
}
