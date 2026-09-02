//go:build integration

package worker

import (
	"context"
	stderrors "errors"
	"time"

	mediaassetadapter "github.com/echovisionlab/geul-api/internal/adapters/mediaasset"
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

func mediaAssetCleanupForIntegration(h *Handlers) *mediaassetdomain.Cleanup {
	if h.mediaCleanup == nil {
		h.mediaCleanup = mediaassetdomain.NewCleanup(
			h.db,
			mediaassetadapter.NewCleanupStorage(h.s3Client, cleanupBucketForIntegration(h)),
		)
	}
	return h.mediaCleanup
}

func ogCleanupForIntegration(h *Handlers) *og.Cleanup {
	if h.ogCleanup == nil {
		h.ogCleanup = og.NewCleanup(h.db)
	}
	return h.ogCleanup
}

func faviconCleanupForIntegration(h *Handlers) *favicon.Cleanup {
	if h.faviconCleanup == nil {
		h.faviconCleanup = favicon.NewCleanup(h.db)
	}
	return h.faviconCleanup
}

func publicAssetCleanupForIntegration(h *Handlers) *mediaassetdomain.PublicAssetCleanup {
	if h.publicAssets == nil {
		storage := mediaassetadapter.NewCleanupStorage(h.s3Client, cleanupBucketForIntegration(h))
		cache := mediaassetadapter.NewPublicAssetCache(
			h.config.CDNURL,
			h.config.CloudflareAPIURL,
			h.config.CloudflareZoneID,
			h.config.CloudflareAPIToken,
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

func cleanupBucketForIntegration(h *Handlers) string {
	if h.config == nil {
		return ""
	}
	return h.config.S3Bucket
}

func cleanupDanglingFilesBeforeForIntegration(ctx context.Context, h *Handlers, orphanCutoff time.Time) error {
	hlsErr := mediaAssetCleanupForIntegration(h).CleanupOrphanHLSPrefixes(ctx, orphanCutoff.UTC())
	meshErr := h.handleCleanupStaleMeshOptimizationCandidates(ctx, time.Now().UTC())
	return stderrors.Join(hlsErr, meshErr)
}
