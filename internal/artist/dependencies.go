package artist

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

// Translation is the Artist-owned port to the shared translation runtime.
type Translation interface {
	ResolveInitialSourceLocale(context.Context, *gorm.DB, string) string
	RequireDocumentContributors(context.Context, *gorm.DB, []string) error
}

// MemberProjection supplies the Member-owned summaries needed by Artist
// participant reads and mutation responses.
type MemberProjection interface {
	LoadMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
	LoadAuthorizationEligibleMemberSummary(context.Context, string) (*commonv1.MemberSummary, error)
	LoadAuthorizationEligibleIdentities(context.Context, []string) (map[string]string, error)
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

// Labels reads the Labels an Artist links; the Label adapter implements it.
type Labels interface {
	// LabelTitles maps each existing Label of labelIDs to its source title.
	LabelTitles(ctx context.Context, db *gorm.DB, labelIDs []string) (map[string]string, error)
	// DraftLabelIDs reports which of labelIDs are draft Labels.
	DraftLabelIDs(ctx context.Context, db *gorm.DB, labelIDs []string) (map[string]bool, error)
}

// ReleaseRelations reads the Releases and Tracks that credit or link an
// Artist; the Release adapter implements it.
type ReleaseRelations interface {
	ReleasesOfArtist(ctx context.Context, db *gorm.DB, artistID string) ([]entityparticipant.Related, error)
	TracksOfArtist(ctx context.Context, db *gorm.DB, artistID string) ([]entityparticipant.Related, error)
	CountReleasesByArtist(ctx context.Context, db *gorm.DB, artistIDs []string) (map[string]int32, error)
}

// ProgramEventRelations reads the Program Events that list or credit an
// Artist; the Program Event adapter implements it.
type ProgramEventRelations interface {
	ProgramEventsOfArtist(ctx context.Context, db *gorm.DB, artistID string) ([]entityparticipant.Related, error)
}

// WorkRelations reads the Works that credit an Artist; the Work adapter
// implements it.
type WorkRelations interface {
	WorksOfArtist(ctx context.Context, db *gorm.DB, artistID string) ([]entityparticipant.Related, error)
}

// ShareLinks removes a deleted Artist's share links in its transaction; the
// share link adapter implements it.
type ShareLinks interface {
	DeleteArtistShareLinks(ctx context.Context, tx *gorm.DB, artistID string) error
}
