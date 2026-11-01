//go:build integration

package worker

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gorm.io/gorm"

	mediaassetadapter "github.com/echovisionlab/geul-api/internal/adapters/mediaasset"
	"github.com/echovisionlab/geul-api/internal/adapters/ogprojection"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/favicon"
	mediaassetdomain "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/og"
)

const (
	orphanHLSPrefixRetention = mediaassetdomain.OrphanHLSPrefixRetention
	unreadyAssetRetention    = mediaassetdomain.UnreadyPublicAssetRetention
	danglingFaviconRetention = favicon.DanglingAssetRetention
	unboundOgAssetRetention  = og.UnboundAssetRetention
)

type cloudflarePurgeRequest = mediaassetadapter.PurgeRequest

// cleanupFixture builds the production cleanups of the domains the
// scheduled cleanup jobs drive over one test database and bucket. These
// tests pin the owners' SQL and lock order; they live here until
// MediaAsset, OG and Favicon take them.
type cleanupFixture struct {
	db         *gorm.DB
	config     *config.Config
	s3Client   *s3.Client
	httpClient *http.Client

	mediaCleanup   *mediaassetdomain.Cleanup
	publicAssets   *mediaassetdomain.PublicAssetCleanup
	ogCleanup      *og.Cleanup
	faviconCleanup *favicon.Cleanup
}

func mediaAssetCleanupForIntegration(h *cleanupFixture) *mediaassetdomain.Cleanup {
	if h.mediaCleanup == nil {
		h.mediaCleanup = mediaassetdomain.NewCleanup(
			h.db,
			mediaassetadapter.NewCleanupStorage(h.s3Client, cleanupBucketForIntegration(h)),
		)
	}
	return h.mediaCleanup
}

func ogCleanupForIntegration(h *cleanupFixture) *og.Cleanup {
	if h.ogCleanup == nil {
		h.ogCleanup = og.NewCleanup(h.db, ogprojection.AssetReferences()...)
	}
	return h.ogCleanup
}

func faviconCleanupForIntegration(h *cleanupFixture) *favicon.Cleanup {
	if h.faviconCleanup == nil {
		h.faviconCleanup = favicon.NewCleanup(h.db)
	}
	return h.faviconCleanup
}

func publicAssetCleanupForIntegration(h *cleanupFixture) *mediaassetdomain.PublicAssetCleanup {
	if h.publicAssets == nil {
		storage := mediaassetadapter.NewCleanupStorage(h.s3Client, cleanupBucketForIntegration(h))
		cache := mediaassetadapter.NewPublicAssetCache(
			h.config.CDNURL,
			h.config.CloudflareAPIURL,
			h.config.CloudflareZoneID,
			h.config.CloudflareAPIToken,
			h.config.CloudflareCachePurgeEnabled,
			h.httpClient,
		)
		h.publicAssets = mediaassetdomain.NewPublicAssetCleanup(
			h.db,
			storage,
			cache,
			ogCleanupForIntegration(h),
		)
	}
	return h.publicAssets
}

func cleanupBucketForIntegration(h *cleanupFixture) string {
	if h.config == nil {
		return ""
	}
	return h.config.S3Bucket
}

func cleanupDanglingFilesBeforeForIntegration(ctx context.Context, h *cleanupFixture, orphanCutoff time.Time) error {
	return mediaAssetCleanupForIntegration(h).CleanupOrphanHLSPrefixes(ctx, orphanCutoff.UTC())
}

// noMeshCandidates is a File media runtime with no stale mesh optimization
// candidate.
type noMeshCandidates struct{ unreachable }

func (noMeshCandidates) ExpireStaleMeshOptimizationCandidates(context.Context, time.Time) (int64, error) {
	return 0, nil
}

// handlers returns the worker over the fixture's cleanups.
func (h *cleanupFixture) handlers(t *testing.T) *Handlers {
	t.Helper()
	return newTestHandlers(t, Deps{
		FileMedia:      noMeshCandidates{unreachable{t: t}},
		DanglingFiles:  mediaAssetCleanupForIntegration(h),
		PublicAssets:   publicAssetCleanupForIntegration(h),
		OGCleanup:      ogCleanupForIntegration(h),
		FaviconCleanup: faviconCleanupForIntegration(h),
	})
}
