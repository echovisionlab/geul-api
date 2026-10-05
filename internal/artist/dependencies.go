package artist

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// AsyncPublisher is the signal surface used after Artist mutations.
type AsyncPublisher interface {
	EnqueueProtobuf(context.Context, string, string, proto.Message) error
	NotifyProtobuf(context.Context, string, proto.Message) error
}

// FileDeleter removes an Artist-owned File after the domain transaction commits.
type FileDeleter interface {
	DeleteFileByID(context.Context, string) error
}

// Translation is the Artist-owned port to the shared translation runtime.
type Translation interface {
	ResolveInitialSourceLocale(context.Context, *gorm.DB, auth.IdentityManager, string) string
	NormalizeInitialSourceLocale(context.Context, *gorm.DB, string) string
	RequireDocumentContributors(context.Context, *gorm.DB, []string) error
}

// MemberProjection supplies the Member-owned summaries needed by Artist
// participant reads and mutation responses.
type MemberProjection interface {
	LoadMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
	LoadAuthorizationEligibleMemberSummary(context.Context, string) (*commonv1.MemberSummary, error)
}

// Runtime is the Artist-owned boundary for shared media, OG, and route
// infrastructure. Implementations live under internal/adapters/artist.
type Runtime interface {
	LoadArtistImages(context.Context, *gorm.DB, []string) (map[string][]ArtistImageProjection, error)
	ResolveReadyAssetRefs(context.Context, *gorm.DB, []string) (map[string]*commonv1.AssetRef, error)
	ReplaceArtistImageBindings(context.Context, *gorm.DB, string, []string) error
	RequestCurrentWithDB(context.Context, *gorm.DB, managev1.OgEntityType, string, string, bool, string) (string, error)
	CancelAndReleaseEntityWithDB(context.Context, *gorm.DB, managev1.OgEntityType, string, string) error
	ReleasePublicAssetBindings(context.Context, *gorm.DB, string, string, string) error
	EnsureResourceRouteAvailable(context.Context, *gorm.DB, string, string, string) error
	EnsureResourceRouteAvailableInTx(context.Context, *gorm.DB, string, string, string) error
	IsResourceRouteAvailable(context.Context, *gorm.DB, string, string) (bool, error)
}

type Dependencies struct {
	Translation Translation
	Members     MemberProjection
	Runtime     Runtime
}
