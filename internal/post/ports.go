package post

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/memberref"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// FileService is the Post-owned File capability. Singular private delivery
// re-establishes the exact Post owner and featured-image slot at issuance;
// summary projection is limited to immutable public display assets.
type FileService interface {
	DeleteFileByID(context.Context, string) error
	ResolveAuthorizedPostFeaturedImage(context.Context, string, string) (*commonv1.MediaDelivery, error)
	ResolvePublicDisplayMedia(context.Context, []string) (map[string]*commonv1.MediaDelivery, error)
}

// MemberSummaryLoader supplies Member-owned manage projections after Post has
// resolved the durable author, collaborator, or commenter identities.
type MemberSummaryLoader interface {
	LoadMemberSummaries(context.Context, []string) (map[string]*memberref.Summary, error)
}
