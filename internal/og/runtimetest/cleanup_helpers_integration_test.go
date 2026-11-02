//go:build integration

package ogruntime_test

import (
	"net/http"
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gorm.io/gorm"

	mediaassetadapter "github.com/echovisionlab/geul-api/internal/adapters/mediaasset"
	"github.com/echovisionlab/geul-api/internal/adapters/ogprojection"
	"github.com/echovisionlab/geul-api/internal/config"
	mediaassetdomain "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/og"
)

const (
	unreadyAssetRetention   = mediaassetdomain.UnreadyPublicAssetRetention
	unboundOgAssetRetention = og.UnboundAssetRetention
)

type cloudflarePurgeRequest = mediaassetadapter.PurgeRequest
type cleanupFixture struct {
	db           *gorm.DB
	config       *config.Config
	s3Client     *s3.Client
	httpClient   *http.Client
	ogCleanup    *og.Cleanup
	publicAssets *mediaassetdomain.PublicAssetCleanup
}

func ogCleanupForIntegration(h *cleanupFixture) *og.Cleanup {
	if h.ogCleanup == nil {
		h.ogCleanup = og.NewCleanup(newCleanupOGUnits(h.db, ""))
	}
	return h.ogCleanup
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
			mediaassetpostgres.NewCleanupUnits(h.db),
			storage,
			cache,
			ogpostgres.NewAssetProtector(h.db, ogprojection.AssetReferences()...),
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
func newCleanupPostgres(t *testing.T) *pgtest.AppPostgres {
	t.Helper()

	return pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	})
}
func newCleanupDB(t *testing.T) *gorm.DB {
	t.Helper()

	return newCleanupPostgres(t).DB
}
