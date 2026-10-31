package label

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"gorm.io/gorm"
)

// Translation provides Label's initial source locale and the shared
// translation contributor check. Label's source locale is owned by
// label.source_locale.
type Translation interface {
	ResolveInitialSourceLocale(context.Context, *gorm.DB, string) string
	RequireDocumentContributors(context.Context, *gorm.DB, []string) error
}

// MemberProjection supplies the Member-owned summaries needed by Label
// participant reads and mutation responses.
type MemberProjection interface {
	LoadMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
	LoadAuthorizationEligibleMemberSummary(context.Context, string) (*commonv1.MemberSummary, error)
	LoadAuthorizationEligibleIdentities(context.Context, []string) (map[string]string, error)
}

// Runtime is Label's narrow boundary to shared File/media and OG lifecycles.
// Label owns mutation policy and binding keys; adapters own CDN projection and
// the concrete shared runtime.
type Runtime interface {
	LockAttachableFilesForUpdate(context.Context, *gorm.DB, []string) error
	BindReadyAssetForSourceFile(context.Context, *gorm.DB, string, string, string, string, string) (*commonv1.AssetRef, error)
	ReleasePublicAssetBindings(context.Context, *gorm.DB, string, string, string) error
	ReleaseExactPublicAssetBindings(context.Context, *gorm.DB, string, string, []string) error
	ResolveReadyAssetRefs(context.Context, *gorm.DB, []string) (map[string]*commonv1.AssetRef, error)
	ResolveReadySourceFileRefs(context.Context, *gorm.DB, []string, ...string) (map[string]*commonv1.AssetRef, error)
	RequestCurrentWithDB(context.Context, *gorm.DB, string, string) (string, error)
	CancelAndReleaseOGWithDB(context.Context, *gorm.DB, string) error
}

// ArtistRelations reads the Artists linked to Labels; the Artist adapter
// implements it. Lists are ordered by ID text.
type ArtistRelations interface {
	ArtistsOfLabel(ctx context.Context, db *gorm.DB, labelID string) ([]entityparticipant.Related, error)
	CountArtistsByLabel(ctx context.Context, db *gorm.DB, labelIDs []string) (map[string]int32, error)
}

// ReleaseRelations reads the Releases linked to Labels; the Release adapter
// implements it. Lists are ordered by ID text.
type ReleaseRelations interface {
	ReleasesOfLabel(ctx context.Context, db *gorm.DB, labelID string) ([]entityparticipant.Related, error)
	CountReleasesByLabel(ctx context.Context, db *gorm.DB, labelIDs []string) (map[string]int32, error)
}

// ProgramEventRelations reads the Program Events linked to a Label; the
// Program Event adapter implements it. The list is ordered by ID text.
type ProgramEventRelations interface {
	ProgramEventsOfLabel(ctx context.Context, db *gorm.DB, labelID string) ([]entityparticipant.Related, error)
}

// ShareLinks removes a deleted Label's share links in its transaction; the
// share link adapter implements it.
type ShareLinks interface {
	DeleteLabelShareLinks(ctx context.Context, tx *gorm.DB, labelID string) error
}
