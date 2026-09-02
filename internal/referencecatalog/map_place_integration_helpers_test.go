//go:build integration

package referencecatalog_test

import (
	"crypto/sha256"
	"testing"

	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func mapPlaceServiceForTest(
	t *testing.T,
	db *gorm.DB,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
) *referencecatalog.MapPlaceService {
	t.Helper()
	return referencecatalog.NewMapPlaceService(
		db,
		referencecatalogadapter.NewAssets(cdnDomain),
		referencecatalogadapter.NewMemberSummaries(cdnDomain),
		spiceDB,
	)
}

func auditedMapPlaceServiceForTest(
	t *testing.T,
	db *gorm.DB,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
	writer domainaudit.Appender,
) *referencecatalog.MapPlaceService {
	t.Helper()
	return referencecatalog.NewAuditedMapPlaceService(
		db,
		writer,
		referencecatalogadapter.NewAssets(cdnDomain),
		referencecatalogadapter.NewMemberSummaries(cdnDomain),
		spiceDB,
	)
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
