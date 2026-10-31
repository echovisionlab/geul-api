package label

import (
	"context"

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
