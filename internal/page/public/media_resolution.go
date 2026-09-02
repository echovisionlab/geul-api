package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"gorm.io/gorm"
)

// DraftAccessChecker owns the authenticated Page draft-view decision.
type DraftAccessChecker interface {
	CanViewPageDraft(context.Context, string) (bool, error)
}

// ShareLinkAccessChecker owns validation of Page draft share-link credentials.
// Implementations must preserve the public not-found behavior for missing,
// invalid, expired, or mismatched links.
type ShareLinkAccessChecker interface {
	RequirePageShareLinkAccess(context.Context, *gorm.DB, string, string, string) (*model.ShareLink, error)
}

// MediaResolver is the narrow public File projection required after Page
// visibility and exact attachment selectors have been authorized.
type MediaResolver interface {
	HydrateAuthorizedContentBlockMedia(context.Context, []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error)
	ResolvePageFeaturedImageDelivery(context.Context, *string) (*commonv1.MediaDelivery, error)
	ResolvePageOGAsset(context.Context, *string, *string) (*commonv1.AssetRef, error)
}
