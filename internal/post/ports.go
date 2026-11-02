package post

import (
	"context"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/memberref"
)

// FileService is the Post-owned File capability. Singular private delivery
// re-establishes the exact Post owner and featured-image slot at issuance;
// summary projection is limited to immutable public display assets.
type FileService interface {
	DeleteFileByID(context.Context, string) error
	ResolveAuthorizedPostFeaturedImage(context.Context, string, string) (*fileref.Delivery, error)
	ResolveDisplayMedia(context.Context, []string) (map[string]*fileref.Delivery, error)
}

// MemberSummaryLoader supplies Member-owned manage projections after Post has
// resolved the durable author, collaborator, or commenter identities.
type MemberSummaryLoader interface {
	LoadMemberSummaries(context.Context, []string) (map[string]*memberref.Summary, error)
}
