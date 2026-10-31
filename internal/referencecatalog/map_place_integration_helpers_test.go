//go:build integration

package referencecatalog_test

import (
	"context"
	"crypto/sha256"
	"testing"

	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	referencecatalogmenuadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog/menu"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// catalogForTest builds the Reference Catalog use cases with the production
// adapters; a nil writer records audit in memory.
func catalogForTest(
	t *testing.T,
	db *gorm.DB,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
	writer domainaudit.Appender,
) *referencecatalog.Service {
	t.Helper()
	if writer == nil {
		writer = &audittest.Recorder{}
	}
	service, err := referencecatalog.NewService(referencecatalog.Deps{
		DB: db, SpiceDB: spiceDB, Audit: writer,
		Assets:      referencecatalogadapter.NewAssets(cdnDomain),
		Members:     referencecatalogadapter.NewMemberSummaries(cdnDomain),
		MenuTargets: referencecatalogmenuadapter.NewTargets(writer),
		ClientWorks: workadapter.ClientWorks{},
	})
	require.NoError(t, err)
	return service
}

// withRequest adds the request context a request-attributed audit record
// needs; the user already in ctx becomes its actor.
func withRequest(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.72")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(ctx, requestContext), auth.GetUser(ctx))
}

func mapPlaceServiceForTest(
	t *testing.T,
	db *gorm.DB,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
) *referencecatalog.MapPlaceConnectHandler {
	t.Helper()
	return referencecatalog.NewMapPlaceConnectHandler(catalogForTest(t, db, cdnDomain, spiceDB, nil))
}

func auditedMapPlaceServiceForTest(
	t *testing.T,
	db *gorm.DB,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
	writer domainaudit.Appender,
) *referencecatalog.MapPlaceConnectHandler {
	t.Helper()
	return referencecatalog.NewMapPlaceConnectHandler(catalogForTest(t, db, cdnDomain, spiceDB, writer))
}

func seedMapPlaceReadyAsset(
	t *testing.T,
	db *gorm.DB,
	kind, extension, mimeType string,
	sourceFileID *string,
) *commonv1.AssetRef {
	t.Helper()
	lifecycle := mediaasset.NewLifecycle(db, "https://cdn.example.com")
	asset, _, err := lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: sourceFileID,
		Kind:         kind,
		Extension:    extension,
		MimeType:     mimeType,
		Disposition:  commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(asset.ID))
	_, err = lifecycle.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{
		AssetId:  asset.ID,
		FileSize: 1024,
		Sha256:   digest[:],
	})
	require.NoError(t, err)
	ref, err := lifecycle.ReadyAssetRef(t.Context(), asset.ID)
	require.NoError(t, err)
	return ref
}

func requireMapPlaceAssetStatus(t *testing.T, db *gorm.DB, assetID, expected string) {
	t.Helper()
	var status string
	require.NoError(t, db.Table("public_asset").Select("status").Where("id = ?", assetID).Scan(&status).Error)
	require.Equal(t, expected, status)
}
