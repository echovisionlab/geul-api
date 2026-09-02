package filemedia

import (
	"context"
	"crypto/sha256"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

type staticFaviconProcessor struct {
	outputs []favicon.Output
	err     error
}

func (p staticFaviconProcessor) Process(context.Context, []byte, string) ([]favicon.Output, error) {
	return p.outputs, p.err
}

type barrierFaviconProcessor struct {
	outputs []favicon.Output
	entered chan struct{}
	release <-chan struct{}
}

func (p barrierFaviconProcessor) Process(ctx context.Context, _ []byte, _ string) ([]favicon.Output, error) {
	select {
	case p.entered <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-p.release:
		return p.outputs, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type blockingFaviconProcessor struct{}

func (blockingFaviconProcessor) Process(ctx context.Context, _ []byte, _ string) ([]favicon.Output, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestFaviconLoadSetRequiresExactReadyDerivatives(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	fileID := seedFaviconSource(t, db, "image/png")
	assets := seedCompleteFaviconDerivatives(t, db, fileID)

	set, err := favicon.LoadSet(t.Context(), db, "https://cdn.example.com", fileID)
	require.NoError(t, err)
	require.NotNil(t, set)
	require.Equal(t, assets[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_PNG_32.String()], set.GetIconPng_32().GetAssetId())
	require.Equal(t, "image/vnd.microsoft.icon", set.GetIconIco().GetMimeType())
	require.Equal(t, "image/png", set.GetAppleTouchIcon_180().GetMimeType())
	require.Nil(t, set.GetIconSvg())

	require.NoError(t, db.Where("file_id = ? AND type = ?", fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_PNG_48.String()).Delete(&model.FileDerivative{}).Error)
	set, err = favicon.LoadSet(t.Context(), db, "https://cdn.example.com", fileID)
	require.Nil(t, set)
	require.ErrorContains(t, err, "has 6 rows, want 7")
}

func TestFaviconLoadSetRejectsWrongMIME(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	fileID := seedFaviconSource(t, db, "image/png")
	assets := seedCompleteFaviconDerivatives(t, db, fileID)
	assetID := assets[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_ICO.String()]
	require.NoError(t, db.Model(&model.PublicAsset{}).Where("id = ?", assetID).Update("mime_type", "image/x-icon").Error)

	set, err := favicon.LoadSet(t.Context(), db, "https://cdn.example.com", fileID)
	require.Nil(t, set)
	require.ErrorContains(t, err, "invalid ready asset metadata")
}

func TestFaviconLoadSetUsesReadySVGSourceAsset(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	fileID := seedFaviconSource(t, db, "image/svg+xml")
	seedCompleteFaviconDerivatives(t, db, fileID)
	seedReadyFaviconAsset(t, db, &fileID, "svg", "image/svg+xml")

	set, err := favicon.LoadSet(t.Context(), db, "https://cdn.example.com", fileID)
	require.NoError(t, err)
	require.NotNil(t, set.GetIconSvg())
	require.Equal(t, "image/svg+xml", set.GetIconSvg().GetMimeType())
}

func TestFaviconLoadSetReturnsNilForLegacySource(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	fileID := seedFaviconSource(t, db, "image/png")
	set, err := favicon.LoadSet(t.Context(), db, "https://cdn.example.com", fileID)
	require.NoError(t, err)
	require.Nil(t, set)
}

func TestFaviconProjectUsesPNG32ForGeneratedAndSourceForLegacy(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	generatedFileID := seedFaviconSource(t, db, "image/png")
	assets := seedCompleteFaviconDerivatives(t, db, generatedFileID)
	legacy, set := favicon.Projection(t.Context(), db, "https://cdn.example.com", generatedFileID)
	require.NotNil(t, set)
	require.Equal(t, assets[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_PNG_32.String()], legacy.GetAssetId())

	legacyFileID := seedFaviconSource(t, db, "image/png")
	legacyAssetID := seedReadyFaviconAsset(t, db, &legacyFileID, "png", "image/png")
	legacy, set = favicon.Projection(t.Context(), db, "https://cdn.example.com", legacyFileID)
	require.Nil(t, set)
	require.Equal(t, legacyAssetID, legacy.GetAssetId())
}

func TestFaviconAssetSetAssetIDsIncludesCommittedSVG(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	fileID := seedFaviconSource(t, db, "image/svg+xml")
	seedCompleteFaviconDerivatives(t, db, fileID)
	seedReadyFaviconAsset(t, db, &fileID, "svg", "image/svg+xml")
	set, err := favicon.LoadSet(t.Context(), db, "https://cdn.example.com", fileID)
	require.NoError(t, err)
	require.Len(t, favicon.AssetIDs(set), 8)
}

func TestSyncSiteSettingFaviconBindingsReplacesAndClearsWholeBundle(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	settingAssets := sitesettingsadapter.NewAssets("https://cdn.example.com")
	firstFileID := seedFaviconSource(t, db, "image/png")
	seedCompleteFaviconDerivatives(t, db, firstFileID)
	secondFileID := seedFaviconSource(t, db, "image/png")
	secondAssets := seedCompleteFaviconDerivatives(t, db, secondFileID)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return settingAssets.ReplaceFavicon(t.Context(), tx, &firstFileID)
	}))
	assertFaviconBindings(t, db, firstFileID, 7)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return settingAssets.ReplaceFavicon(t.Context(), tx, &secondFileID)
	}))
	bindings := assertFaviconBindings(t, db, secondFileID, 7)
	wantKeys := []string{"favicon", "favicon:apple180", "favicon:ico", "favicon:manifest192", "favicon:manifest512", "favicon:png16", "favicon:png48"}
	gotKeys := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		gotKeys = append(gotKeys, binding.BindingKey)
	}
	require.ElementsMatch(t, wantKeys, gotKeys)
	require.Equal(t, secondAssets[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_PNG_32.String()], assetIDForBinding(bindings, "favicon"))

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return settingAssets.ReplaceFavicon(t.Context(), tx, nil)
	}))
	var count int64
	require.NoError(t, db.Model(&model.PublicAssetBinding{}).Where("binding_key = ? OR binding_key LIKE ?", "favicon", "favicon:%").Count(&count).Error)
	require.Zero(t, count)
}

func TestSyncSiteSettingFaviconBindingsIncludesSVG(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	assets := sitesettingsadapter.NewAssets("https://cdn.example.com")
	fileID := seedFaviconSource(t, db, "image/svg+xml")
	seedCompleteFaviconDerivatives(t, db, fileID)
	seedReadyFaviconAsset(t, db, &fileID, "svg", "image/svg+xml")
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return assets.ReplaceFavicon(t.Context(), tx, &fileID)
	}))
	bindings := assertFaviconBindings(t, db, fileID, 8)
	require.NotEmpty(t, assetIDForBinding(bindings, "favicon:svg"))
}

func TestRequestFaviconAssetsDeletionProtectsActiveThenMarksAllPending(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	settingAssets := sitesettingsadapter.NewAssets("https://cdn.example.com")
	fileID := seedFaviconSource(t, db, "image/svg+xml")
	seedCompleteFaviconDerivatives(t, db, fileID)
	seedReadyFaviconAsset(t, db, &fileID, "svg", "image/svg+xml")
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return settingAssets.ReplaceFavicon(t.Context(), tx, &fileID)
	}))
	require.ErrorContains(t, db.Transaction(func(tx *gorm.DB) error {
		return favicon.RequestDeletion(t.Context(), tx, fileID)
	}), "active bindings")

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := settingAssets.ReplaceFavicon(t.Context(), tx, nil); err != nil {
			return err
		}
		return favicon.RequestDeletion(t.Context(), tx, fileID)
	}))
	var assets []model.PublicAsset
	require.NoError(t, db.Where("kind = ?", "favicon").Find(&assets).Error)
	require.Len(t, assets, 8)
	for _, asset := range assets {
		require.Equal(t, model.PublicAssetStatusDeletePending, asset.Status)
		require.NotNil(t, asset.DeleteRequestedAt)
	}
}

func TestRequestFaviconAssetsDeletionProtectsLegacyPNGAndICOSourceAssets(t *testing.T) {
	for _, mimeType := range []string{"image/png", "image/x-icon"} {
		t.Run(mimeType, func(t *testing.T) {
			db := newFaviconBundleUnitDB(t)
			fileID := seedFaviconSource(t, db, mimeType)
			assetID := seedReadyFaviconAsset(t, db, &fileID, model.GetExtensionFromMime(mimeType), mimeType)
			lifecycle := mediaasset.NewLifecycle(db, "https://cdn.example.com")
			require.NoError(t, lifecycle.BindPublicAsset(t.Context(), mediaasset.Binding{
				AssetID: assetID, OwnerType: "site_settings", OwnerID: "1", BindingKey: "favicon", SourceFileID: &fileID,
			}))
			require.ErrorContains(t, db.Transaction(func(tx *gorm.DB) error {
				return favicon.RequestDeletion(t.Context(), tx, fileID)
			}), "active bindings")

			require.NoError(t, lifecycle.ReleasePublicAssetBindings(t.Context(), "site_settings", "1", "favicon"))
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return favicon.RequestDeletion(t.Context(), tx, fileID)
			}))
			var asset model.PublicAsset
			require.NoError(t, db.First(&asset, "id = ?", assetID).Error)
			require.Equal(t, model.PublicAssetStatusDeletePending, asset.Status)
		})
	}
}

func TestDeleteFaviconSourceMarksAssetsAndFilePendingBeforeWorkerFinalization(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	createFileAttachmentReferenceTablesForServiceTests(t, db)
	fileID := seedFaviconSource(t, db, "image/png")
	seedCompleteFaviconDerivatives(t, db, fileID)
	svc := &FileService{db: db, asyncPublisher: noopAsyncPublisher{}}
	require.NoError(t, svc.deleteFileRecordByID(t.Context(), fileID))
	var file model.File
	require.NoError(t, db.First(&file, "id = ?", fileID).Error)
	require.NotNil(t, file.DeleteRequestedAt)
	var pendingCount int64
	require.NoError(t, db.Model(&model.PublicAsset{}).
		Where("kind = ? AND status = ?", "favicon", model.PublicAssetStatusDeletePending).
		Count(&pendingCount).Error)
	require.Equal(t, int64(7), pendingCount)
}

func TestGenerateFaviconBundlePersistsCleanupRecordsOnWriteFailure(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	source := faviconTestPNG(t, 151, 152, color.NRGBA{R: 200, A: 255})
	fileID := seedFaviconSourceBytes(t, db, "image/png", source)
	store := newFaviconS3Fixture(t, fileID, "png", source, 3)
	svc := &FileService{
		db:               db,
		s3Client:         store.client,
		s3Bucket:         "media-bucket",
		cdnDomain:        "https://cdn.example.com",
		faviconProcessor: staticFaviconProcessor{outputs: faviconTestGeneratedOutputs(t)},
	}

	asset, err := svc.generateFaviconBundle(t.Context(), fileID)
	require.Nil(t, asset)
	require.ErrorContains(t, err, "stage favicon derivative")

	var assets []model.PublicAsset
	require.NoError(t, db.Order("created_at ASC").Find(&assets).Error)
	require.Len(t, assets, 3)
	require.Equal(t, model.PublicAssetStatusDeletePending, assets[0].Status)
	require.Equal(t, model.PublicAssetStatusDeletePending, assets[1].Status)
	require.Equal(t, model.PublicAssetStatusAllocated, assets[2].Status)
	var derivativeCount int64
	require.NoError(t, db.Model(&model.FileDerivative{}).Count(&derivativeCount).Error)
	require.Zero(t, derivativeCount)
	require.Empty(t, store.deletedKeys(), "generation failure must use persisted cleanup state, not destructive best-effort deletes")
}

func TestGenerateFaviconBundleProcessorFailureWritesNothing(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	source := faviconTestPNG(t, 151, 152, color.NRGBA{R: 200, A: 255})
	fileID := seedFaviconSourceBytes(t, db, "image/png", source)
	store := newFaviconS3Fixture(t, fileID, "png", source, 0)
	svc := &FileService{
		db:               db,
		s3Client:         store.client,
		s3Bucket:         "media-bucket",
		cdnDomain:        "https://cdn.example.com",
		faviconProcessor: staticFaviconProcessor{err: fmt.Errorf("injected processor failure")},
	}

	asset, err := svc.generateFaviconBundle(t.Context(), fileID)
	require.Nil(t, asset)
	require.ErrorContains(t, err, "injected processor failure")
	require.Empty(t, store.writtenKeys())
	var assetCount int64
	require.NoError(t, db.Model(&model.PublicAsset{}).Count(&assetCount).Error)
	require.Zero(t, assetCount)
}

func TestGenerateFaviconBundleStoresCompleteSetBeforeSuccess(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	source := faviconTestPNG(t, 151, 152, color.NRGBA{R: 200, A: 255})
	fileID := seedFaviconSourceBytes(t, db, "image/png", source)
	store := newFaviconS3Fixture(t, fileID, "png", source, 0)
	svc := &FileService{
		db:               db,
		s3Client:         store.client,
		s3Bucket:         "media-bucket",
		cdnDomain:        "https://cdn.example.com",
		faviconProcessor: staticFaviconProcessor{outputs: faviconTestGeneratedOutputs(t)},
	}

	asset, err := svc.generateFaviconBundle(t.Context(), fileID)
	require.NoError(t, err)
	require.Equal(t, "image/png", asset.GetMimeType())

	set, err := favicon.LoadSet(t.Context(), db, svc.cdnDomain, fileID)
	require.NoError(t, err)
	require.NotNil(t, set)
	require.Len(t, store.writtenKeys(), 7)
	repeated, err := svc.generateFaviconBundle(t.Context(), fileID)
	require.NoError(t, err)
	require.Equal(t, asset.GetAssetId(), repeated.GetAssetId())
	require.Len(t, store.writtenKeys(), 7, "idempotent completion must not rewrite assets")
}

func TestGenerateFaviconBundleConcurrentCallsConvergeOnOneSet(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	source := faviconTestPNG(t, 151, 152, color.NRGBA{R: 200, A: 255})
	fileID := seedFaviconSourceBytes(t, db, "image/png", source)
	store := newFaviconS3Fixture(t, fileID, "png", source, 0)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	svc := &FileService{
		db:        db,
		s3Client:  store.client,
		s3Bucket:  "media-bucket",
		cdnDomain: "https://cdn.example.com",
		faviconProcessor: barrierFaviconProcessor{
			outputs: faviconTestGeneratedOutputs(t),
			entered: entered,
			release: release,
		},
	}
	type result struct {
		assetID string
		err     error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			asset, err := svc.generateFaviconBundle(t.Context(), fileID)
			assetID := ""
			if asset != nil {
				assetID = asset.GetAssetId()
			}
			results <- result{assetID: assetID, err: err}
		}()
	}
	<-entered
	<-entered
	close(release)
	first := <-results
	second := <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.NotEmpty(t, first.assetID)
	require.Equal(t, first.assetID, second.assetID)
	require.Len(t, store.writtenKeys(), 14)
	var pendingCount int64
	require.NoError(t, db.Model(&model.PublicAsset{}).Where("status = ?", model.PublicAssetStatusDeletePending).Count(&pendingCount).Error)
	require.Equal(t, int64(7), pendingCount, "the losing staged bundle remains durably scheduled for cleanup")
}

func TestGenerateFaviconBundleCommitAckAmbiguityPreservesTrackedAssetsAndSource(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	source := faviconTestPNG(t, 151, 152, color.NRGBA{R: 200, A: 255})
	fileID := seedFaviconSourceBytes(t, db, "image/png", source)
	store := newFaviconS3Fixture(t, fileID, "png", source, 0)
	svc := &FileService{
		db:                        db,
		s3Client:                  store.client,
		s3Bucket:                  "media-bucket",
		cdnDomain:                 "https://cdn.example.com",
		faviconProcessor:          staticFaviconProcessor{outputs: faviconTestGeneratedOutputs(t)},
		testFaviconCommitError:    fmt.Errorf("injected lost commit acknowledgement"),
		testFaviconReconcileError: fmt.Errorf("injected reconciliation read failure"),
	}

	asset, err := svc.generateFaviconBundle(t.Context(), fileID)
	require.Nil(t, asset)
	require.ErrorIs(t, err, errFaviconBundleCommitUncertain)
	require.Empty(t, store.deletedKeys())
	var sourceCount int64
	require.NoError(t, db.Model(&model.File{}).Where("id = ?", fileID).Count(&sourceCount).Error)
	require.Equal(t, int64(1), sourceCount)
	var readyCount int64
	require.NoError(t, db.Model(&model.PublicAsset{}).Where("status = ?", model.PublicAssetStatusReady).Count(&readyCount).Error)
	require.Equal(t, int64(7), readyCount)

	svc.testFaviconCommitError = nil
	svc.testFaviconReconcileError = nil
	reconciled, err := svc.generateFaviconBundle(t.Context(), fileID)
	require.NoError(t, err)
	require.NotNil(t, reconciled)
	require.Len(t, store.writtenKeys(), 7, "retry must reconcile the persisted bundle without rewriting objects")
}

func TestGenerateFaviconBundleCommitAckAmbiguityReturnsPersistedWinner(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	source := faviconTestPNG(t, 151, 152, color.NRGBA{R: 200, A: 255})
	fileID := seedFaviconSourceBytes(t, db, "image/png", source)
	store := newFaviconS3Fixture(t, fileID, "png", source, 0)
	svc := &FileService{
		db:                     db,
		s3Client:               store.client,
		s3Bucket:               "media-bucket",
		cdnDomain:              "https://cdn.example.com",
		faviconProcessor:       staticFaviconProcessor{outputs: faviconTestGeneratedOutputs(t)},
		testFaviconCommitError: fmt.Errorf("injected lost commit acknowledgement"),
	}
	asset, err := svc.generateFaviconBundle(t.Context(), fileID)
	require.NoError(t, err)
	require.NotNil(t, asset)
	require.Empty(t, store.deletedKeys())
}

func TestGenerateFaviconBundleWholeDeadline(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	source := faviconTestPNG(t, 16, 16, color.NRGBA{R: 200, A: 255})
	fileID := seedFaviconSourceBytes(t, db, "image/png", source)
	store := newFaviconS3Fixture(t, fileID, "png", source, 0)
	svc := &FileService{
		db:                       db,
		s3Client:                 store.client,
		s3Bucket:                 "media-bucket",
		cdnDomain:                "https://cdn.example.com",
		faviconProcessor:         blockingFaviconProcessor{},
		testFaviconBundleTimeout: 40 * time.Millisecond,
	}
	started := time.Now()
	_, err := svc.generateFaviconBundle(t.Context(), fileID)
	require.ErrorContains(t, err, "deadline exceeded")
	require.Less(t, time.Since(started), time.Second)
	require.Empty(t, store.writtenKeys())
}

func TestGenerateFaviconBundlePublishesVerifiedSVGSourceRef(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	source := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="151" height="152"><rect width="151" height="152" fill="red"/></svg>`)
	fileID := seedFaviconSourceBytes(t, db, "image/svg+xml", source)
	store := newFaviconS3Fixture(t, fileID, "svg", source, 0)
	svc := &FileService{
		db:               db,
		s3Client:         store.client,
		s3Bucket:         "media-bucket",
		cdnDomain:        "https://cdn.example.com",
		faviconProcessor: staticFaviconProcessor{outputs: faviconTestGeneratedOutputs(t)},
	}

	_, err := svc.generateFaviconBundle(t.Context(), fileID)
	require.NoError(t, err)
	set, err := favicon.LoadSet(t.Context(), db, svc.cdnDomain, fileID)
	require.NoError(t, err)
	require.Equal(t, "image/svg+xml", set.GetIconSvg().GetMimeType())
	require.Len(t, store.writtenKeys(), 8)
}

func TestGenerateFaviconBundleRejectsUnsafeSVGBeforePublicStorage(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	source := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><style>.x{fill:red}</style><rect class="x"/></svg>`)
	fileID := seedFaviconSourceBytes(t, db, "image/svg+xml", source)
	store := newFaviconS3Fixture(t, fileID, "svg", source, 0)
	svc := &FileService{
		db: db, s3Client: store.client, s3Bucket: "media-bucket", cdnDomain: "https://cdn.example.com",
		faviconProcessor: staticFaviconProcessor{outputs: faviconTestGeneratedOutputs(t)},
	}
	asset, err := svc.generateFaviconBundle(t.Context(), fileID)
	require.Nil(t, asset)
	require.ErrorContains(t, err, "style")
	require.Empty(t, store.writtenKeys())
	var assetCount int64
	require.NoError(t, db.Model(&model.PublicAsset{}).Count(&assetCount).Error)
	require.Zero(t, assetCount)
}

func newFaviconBundleUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newServiceUnitDB(t)
	require.NoError(t, db.Exec(`
			CREATE TABLE file_derivative (
			id text PRIMARY KEY, file_id text NOT NULL, type text NOT NULL,
			asset_id text, media_generation_id text, created_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(file_id, type)
			)
		`).Error)
	require.NoError(t, db.Exec(`
			CREATE TABLE mesh_optimization_candidate (
				id text PRIMARY KEY, source_file_id text NOT NULL, output_object_id text, output_file_id text,
				status text NOT NULL, selected_at datetime, cancelled_at datetime,
				expires_at datetime, updated_at datetime
			)
		`).Error)
	return db
}

func seedFaviconSource(t *testing.T, db *gorm.DB, mimeType string) string {
	t.Helper()
	fileID := uuid.NewString()
	extension := model.GetExtensionFromMime(mimeType)
	digest := sha256.Sum256([]byte(fileID))
	require.NoError(t, db.Create(&model.File{
		ID:        fileID,
		FileName:  "favicon." + extension,
		MimeType:  mimeType,
		FileSize:  int64(len(fileID)),
		Extension: extension,
		SHA256:    digest[:],
		CreatedAt: time.Now().UTC(),
	}).Error)
	return fileID
}

func seedFaviconSourceBytes(t *testing.T, db *gorm.DB, mimeType string, data []byte) string {
	t.Helper()
	fileID := uuid.NewString()
	extension := model.GetExtensionFromMime(mimeType)
	digest := sha256.Sum256(data)
	require.NoError(t, db.Create(&model.File{
		ID:        fileID,
		FileName:  "favicon." + extension,
		MimeType:  mimeType,
		FileSize:  int64(len(data)),
		Extension: extension,
		SHA256:    digest[:],
		CreatedAt: time.Now().UTC(),
	}).Error)
	return fileID
}

func seedCompleteFaviconDerivatives(t *testing.T, db *gorm.DB, fileID string) map[string]string {
	t.Helper()
	specs := favicon.RequiredOutputs()
	assets := make(map[string]string, len(specs))
	for _, spec := range specs {
		assetID := seedReadyFaviconAsset(t, db, nil, spec.Extension, spec.MimeType)
		require.NoError(t, db.Table("file_derivative").Create(structured.Fields{
			"id":       uuid.NewString(),
			"file_id":  fileID,
			"type":     spec.DerivativeType.String(),
			"asset_id": assetID,
		}).Error)
		assets[spec.DerivativeType.String()] = assetID
	}
	return assets
}

func seedReadyFaviconAsset(t *testing.T, db *gorm.DB, sourceFileID *string, extension string, mimeType string) string {
	t.Helper()
	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, extension)
	require.NoError(t, err)
	payload := []byte(assetID + extension)
	digest := sha256.Sum256(payload)
	size := int64(len(payload))
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.PublicAsset{
		ID:           assetID,
		SourceFileID: sourceFileID,
		Kind:         "favicon",
		ObjectKey:    objectKey,
		Extension:    extension,
		MimeType:     mimeType,
		FileSize:     &size,
		SHA256:       digest[:],
		Disposition:  "inline",
		Status:       model.PublicAssetStatusReady,
		ReadyAt:      &now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}).Error)
	return assetID
}

type faviconS3Fixture struct {
	client     *s3.Client
	mu         sync.Mutex
	puts       int
	failPutAt  int
	failDelete bool
	written    []string
	deleted    []string
}

func newFaviconS3Fixture(t *testing.T, fileID string, extension string, source []byte, failPutAt int) *faviconS3Fixture {
	t.Helper()
	fixture := &faviconS3Fixture{failPutAt: failPutAt}
	sourcePath := "/media-bucket/media/" + fileID + "." + extension
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path != sourcePath {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(source)))
			_, _ = w.Write(source)
		case http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			fixture.mu.Lock()
			fixture.puts++
			putNumber := fixture.puts
			if failPutAt == 0 || putNumber < failPutAt {
				fixture.written = append(fixture.written, strings.TrimPrefix(r.URL.Path, "/media-bucket/"))
			}
			fixture.mu.Unlock()
			if failPutAt > 0 && putNumber == failPutAt {
				http.Error(w, "injected write failure", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			fixture.mu.Lock()
			failDelete := fixture.failDelete
			fixture.mu.Unlock()
			if failDelete {
				http.Error(w, "injected delete failure", http.StatusInternalServerError)
				return
			}
			fixture.mu.Lock()
			fixture.deleted = append(fixture.deleted, strings.TrimPrefix(r.URL.Path, "/media-bucket/"))
			fixture.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	fixture.client = s3.NewFromConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider("test", "test", "")),
		HTTPClient:  server.Client(),
		Retryer: func() aws.Retryer {
			return aws.NopRetryer{}
		},
	}, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(server.URL)
		options.UsePathStyle = true
	})
	return fixture
}

func (f *faviconS3Fixture) writtenKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.written...)
}

func (f *faviconS3Fixture) deletedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

func assertFaviconBindings(t *testing.T, db *gorm.DB, sourceFileID string, want int) []model.PublicAssetBinding {
	t.Helper()
	var bindings []model.PublicAssetBinding
	require.NoError(t, db.Where("owner_type = ? AND owner_id = ?", "site_settings", "1").
		Where("binding_key = ? OR binding_key LIKE ?", "favicon", "favicon:%").
		Order("binding_key ASC").
		Find(&bindings).Error)
	require.Len(t, bindings, want)
	for _, binding := range bindings {
		require.NotNil(t, binding.SourceFileID)
		require.Equal(t, sourceFileID, *binding.SourceFileID)
	}
	return bindings
}

func assetIDForBinding(bindings []model.PublicAssetBinding, key string) string {
	for _, binding := range bindings {
		if binding.BindingKey == key {
			return binding.AssetID
		}
	}
	return ""
}
