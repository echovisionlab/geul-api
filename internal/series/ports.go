package series

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Authorizer is the SpiceDB port of the Series use cases: the mutation
// pipeline's checks and relationship writes, the lookups of the Series a
// principal may list, and the relationship snapshot a deletion removes.
// *auth.SpiceDBClient is the production Authorizer.
type Authorizer interface {
	mutation.Authorizer
	LookupResources(ctx context.Context, lookup policyv1.ResourceLookup, actor policyv1.Actor) ([]string, error)
	SnapshotResourceRelationshipDescriptors(ctx context.Context, plan policyv1.RelationshipSnapshotPlan) ([]auth.RelationshipSnapshot, auth.ZedToken, error)
}

// Media projects ready public assets for Series readers.
type Media interface {
	// ReadyAsset returns the first ready asset among candidateIDs.
	ReadyAsset(ctx context.Context, candidateIDs ...*string) (*Asset, error)
	// ReadyAssets returns the ready assets among candidateIDs by asset ID.
	ReadyAssets(ctx context.Context, candidateIDs ...*string) (map[string]*Asset, error)
	// ReadyAssetsForSourceFiles returns the ready asset of kind of each
	// source File by File ID.
	ReadyAssetsForSourceFiles(ctx context.Context, kind string, fileIDs []string) (map[string]*Asset, error)
	ReadyAssetForSourceFile(ctx context.Context, fileID, kind string) (*Asset, error)
}

// Reads loads the aggregate counts a Series list shows from the tables other
// domains own.
type Reads interface {
	LoadPostCounts(ctx context.Context, seriesIDs []string) (map[string]int32, error)
	LoadManagerCounts(ctx context.Context, seriesIDs []string) (map[string]int32, error)
}

// Members projects the Member summaries of Series managers.
type Members interface {
	LoadSeriesManagers(ctx context.Context, memberIDs []string) (map[string]ManagerSummary, error)
}

// Locales reads the preferences the source locale of a new Series falls
// back to, outside any transaction: the translation runtime settings are
// created on first read.
type Locales interface {
	// MemberPreferredLocale reads the preferred locale of a live Member.
	MemberPreferredLocale(ctx context.Context, memberID string) (*string, error)
	// DefaultTranslationLocale reads the translation runtime's default
	// locale.
	DefaultTranslationLocale(ctx context.Context) (string, error)
}

// SeriesListDetails are the list-only facts of one listed Series.
type SeriesListDetails struct {
	PostCount          int32
	ManagerCount       int32
	FeaturedImageAsset *Asset
}
