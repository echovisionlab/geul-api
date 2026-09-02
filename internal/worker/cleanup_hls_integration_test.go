//go:build integration

package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	mediaassetadapter "github.com/echovisionlab/geul-api/internal/adapters/mediaasset"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func randomHLSCleanupIntegrationUUID() string {
	return uuid.NewString()
}

func TestHandleCleanupDanglingFilesKeepsCommittedLibraryFiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := newWorkerIntegrationDB(t)
	s3Client, cfg, err := newWorkerIntegrationS3FromSharedLease(ctx)
	require.NoError(t, err)
	publisher := &recordingWorkerPublisher{}
	handlers := &Handlers{
		db:        db,
		config:    cfg,
		s3Client:  s3Client,
		publisher: publisher,
	}

	expiredAt := time.Now().Add(-48 * time.Hour)
	youngAt := time.Now().Add(-time.Hour)
	orphanCutoff := time.Now().Add(-24 * time.Hour)

	orphanFileID := randomHLSCleanupIntegrationUUID()
	youngFileID := randomHLSCleanupIntegrationUUID()
	referencedFileID := randomHLSCleanupIntegrationUUID()
	mapImageFileID := randomHLSCleanupIntegrationUUID()
	eventMediaFileID := randomHLSCleanupIntegrationUUID()
	require.NoError(t, insertWorkerIntegrationFileCreatedAt(db, orphanFileID, expiredAt))
	require.NoError(t, insertWorkerIntegrationFileCreatedAt(db, youngFileID, youngAt))
	require.NoError(t, insertWorkerIntegrationFileCreatedAt(db, referencedFileID, expiredAt))
	require.NoError(t, insertWorkerIntegrationFileCreatedAt(db, mapImageFileID, expiredAt))
	require.NoError(t, insertWorkerIntegrationFileCreatedAt(db, eventMediaFileID, expiredAt))
	defaultMapThemeID := randomHLSCleanupIntegrationUUID()
	defaultMapThemeVariant := model.MapThemeVariant{
		BackgroundColor: "#ffffff", WaterColor: "#ffffff", LandColor: "#ffffff", RoadColor: "#ffffff",
		BuildingFillColor: "#ffffff", BuildingStrokeColor: "#000000", CalloutLineColor: "#000000",
		CalloutTextColor: "#000000", CalloutBackgroundColor: "#ffffff", CalloutDescriptionColor: "#000000",
		AttributionColor: "#000000", LabelTextColor: "#000000", ClusterColor: "#000000",
		ClusterHoverColor: "#000000", ClusterTextColor: "#ffffff", ClusterTextHoverColor: "#ffffff",
		CalloutHoverLineColor: "#000000", CalloutHoverTextColor: "#000000",
		CalloutHoverDescriptionColor: "#000000", CalloutHoverBackgroundColor: "#ffffff",
	}
	require.NoError(t, db.Create(&model.MapTheme{
		ID: defaultMapThemeID, Name: "cleanup-default", CalloutFields: pq.StringArray{"name", "address"},
		AttributionFontSize: 11, LightVariant: defaultMapThemeVariant, DarkVariant: defaultMapThemeVariant,
	}).Error)
	orphanThumbID, _ := seedWorkerAssetDerivative(t, db, orphanFileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL, "thumbnail", "webp", "image/webp")
	seedWorkerHLSGenerationDerivative(t, db, orphanFileID)
	require.NoError(t, db.Exec(`
		INSERT INTO site_settings (id, logo_light_file_id, default_map_theme_id)
		VALUES (1, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			logo_light_file_id = EXCLUDED.logo_light_file_id,
			default_map_theme_id = EXCLUDED.default_map_theme_id
	`, referencedFileID, defaultMapThemeID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO map_place (id, name, address, lat, lng, image_file_id)
		VALUES (?, 'Protected place', 'Protected address', 37.0, 127.0, ?)
	`, uuid.NewString(), mapImageFileID).Error)
	eventTypeID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO program_event_type (id, slug)
		VALUES (?, ?)
	`, eventTypeID, "cleanup-"+uuid.NewString()).Error)
	eventID := uuid.NewString()
	eventDocumentID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO content_document (id, profile, revision)
		VALUES (?, 'program_event', ?)
	`, eventDocumentID, uuid.NewString()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO program_event (id, slug, type_id, starts_at, location_mode, content_document_id)
		VALUES (?, ?, ?, ?, 'PROGRAM_EVENT_LOCATION_MODE_TBA', ?)
	`, eventID, "cleanup-"+uuid.NewString(), eventTypeID, time.Now().UTC(), eventDocumentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO program_event_media (event_id, file_id, role)
		VALUES (?, ?, 'poster')
	`, eventID, eventMediaFileID).Error)

	require.NoError(t, cleanupDanglingFilesBeforeForIntegration(
		ctx,
		handlers,
		orphanCutoff,
	))

	for _, fileID := range []string{orphanFileID, youngFileID, referencedFileID, mapImageFileID, eventMediaFileID} {
		requireWorkerFilePresent(t, db, fileID)
	}
	require.Empty(t, publisher.fileDeleteEvents)
	require.Empty(t, publisher.transcodeCancelEvents)

	var thumbnail model.PublicAsset
	require.NoError(t, db.Where("id = ?", orphanThumbID).Take(&thumbnail).Error)
	require.Equal(t, model.PublicAssetStatusReady, thumbnail.Status)
	require.Nil(t, thumbnail.DeleteRequestedAt)
}

func TestHandleCleanupDanglingFilesDoesNotDeleteLibraryFilesByAgeOrUsage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := newWorkerIntegrationDB(t)
	s3Client, cfg, err := newWorkerIntegrationS3FromSharedLease(ctx)
	require.NoError(t, err)
	publisher := &recordingWorkerPublisher{}
	handlers := &Handlers{db: db, config: cfg, s3Client: s3Client, publisher: publisher}
	now := time.Now().UTC()

	generalOrphanID := uuid.NewString()
	structuredEditorID := uuid.NewString()
	recentRichTextID := uuid.NewString()
	expiredRichTextID := uuid.NewString()
	recentTrackID := uuid.NewString()
	expiredTrackID := uuid.NewString()

	for fileID, createdAt := range map[string]time.Time{
		generalOrphanID:    now.Add(-48 * time.Hour),
		structuredEditorID: now.Add(-48 * time.Hour),
		recentRichTextID:   now.Add(-48 * time.Hour),
		expiredRichTextID:  now.Add(-8 * 24 * time.Hour),
		recentTrackID:      now.Add(-48 * time.Hour),
		expiredTrackID:     now.Add(-8 * 24 * time.Hour),
	} {
		require.NoError(t, insertWorkerIntegrationFileCreatedAt(db, fileID, createdAt))
	}
	require.NoError(t, db.Model(&model.File{}).
		Where("id = ?", structuredEditorID).
		Update("ingest_slot_id", "page-block:scene:structured").Error)

	postEntityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST.String()
	trackEntityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK.String()
	for _, binding := range []struct {
		fileID     string
		uploadType string
		entityType string
	}{
		{structuredEditorID, managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String(), postEntityType},
		{recentRichTextID, managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO.String(), postEntityType},
		{expiredRichTextID, managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO.String(), postEntityType},
		{recentTrackID, managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(), trackEntityType},
		{expiredTrackID, managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(), trackEntityType},
	} {
		entityType := binding.entityType
		require.NoError(t, db.Create(&model.FileIngestBinding{
			FileID:     binding.fileID,
			UploadType: binding.uploadType,
			EntityType: &entityType,
			EntityID:   uuid.NewString(),
		}).Error)
	}

	require.NoError(t, cleanupDanglingFilesBeforeForIntegration(
		ctx,
		handlers,
		now.Add(-orphanHLSPrefixRetention),
	))

	for _, fileID := range []string{
		generalOrphanID,
		structuredEditorID,
		recentRichTextID,
		expiredRichTextID,
		recentTrackID,
		expiredTrackID,
	} {
		requireWorkerFilePresent(t, db, fileID)
	}
	require.Empty(t, publisher.fileDeleteEvents)
	require.Empty(t, publisher.transcodeCancelEvents)
}

func TestCleanupRetiredMediaGenerationsDeletesOnlyDueUnreferencedGeneration(t *testing.T) {
	ctx := context.Background()
	db := newWorkerIntegrationDB(t)
	s3Client, cfg, err := newWorkerIntegrationS3FromSharedLease(ctx)
	require.NoError(t, err)
	cleanupStorage := mediaassetadapter.NewCleanupStorage(s3Client, cfg.S3Bucket)
	handlers := &Handlers{
		db:           db,
		config:       cfg,
		s3Client:     s3Client,
		mediaCleanup: mediaasset.NewCleanup(db, cleanupStorage),
	}
	now := time.Now().UTC()

	due := seedWorkerRetiredMediaGeneration(t, db, now.Add(-9*time.Hour), now.Add(-time.Hour))
	notDue := seedWorkerRetiredMediaGeneration(t, db, now, now.Add(7*time.Hour))
	currentPointer := seedWorkerRetiredMediaGeneration(t, db, now.Add(-9*time.Hour), now.Add(-time.Hour))
	require.NoError(t, db.Exec(`
		INSERT INTO file_derivative (file_id, type, media_generation_id)
		VALUES (?, ?, ?)
	`, currentPointer.FileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(), currentPointer.ID).Error)
	changed := seedWorkerRetiredMediaGeneration(t, db, now.Add(-9*time.Hour), now.Add(-time.Hour))

	for _, generation := range []model.MediaGeneration{due, notDue, currentPointer, changed} {
		require.NoError(t, putWorkerIntegrationObject(
			ctx,
			s3Client,
			cfg.S3Bucket,
			generation.ObjectPrefix+"/master.m3u8",
			"manifest",
		))
		require.NoError(t, putWorkerIntegrationObject(
			ctx,
			s3Client,
			cfg.S3Bucket,
			generation.ObjectPrefix+"/segment-000.ts",
			"segment",
		))
	}

	require.NoError(t, db.Model(&model.MediaGeneration{}).
		Where("id = ?", changed.ID).
		Updates(structured.Fields{
			"status":       model.MediaGenerationStatusReady,
			"retired_at":   nil,
			"delete_after": nil,
		}).Error)

	require.NoError(t, handlers.handleCleanupDanglingFiles(ctx))

	requireWorkerMediaGenerationMissing(t, db, due.ID)
	require.Empty(t, listWorkerIntegrationKeys(t, ctx, s3Client, cfg.S3Bucket, due.ObjectPrefix))
	for _, generation := range []model.MediaGeneration{notDue, currentPointer, changed} {
		requireWorkerMediaGenerationPresent(t, db, generation.ID)
		require.Len(t, listWorkerIntegrationKeys(t, ctx, s3Client, cfg.S3Bucket, generation.ObjectPrefix), 2)
	}
}

func TestCleanupRetiredMediaGenerationsS3FailureDoesNotStarveFollowingRows(t *testing.T) {
	ctx := context.Background()
	db := newWorkerIntegrationDB(t)
	now := time.Now().UTC()
	failing := seedWorkerRetiredMediaGeneration(t, db, now.Add(-10*time.Hour), now.Add(-2*time.Hour))
	following := seedWorkerRetiredMediaGeneration(t, db, now.Add(-9*time.Hour), now.Add(-time.Hour))

	var failFirst atomic.Bool
	failFirst.Store(true)
	s3Client := newFileDeleteTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := r.URL.Query().Get("prefix")
		if prefix == failing.ObjectPrefix+"/" && failFirst.Load() {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`<Error><Code>InternalError</Code><Message>retry</Message></Error>`))
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
<Name>media</Name><Prefix>%s</Prefix><KeyCount>0</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>
</ListBucketResult>`, prefix)
	}))
	handlers := &Handlers{
		db:       db,
		config:   &config.Config{S3Bucket: "media"},
		s3Client: s3Client,
	}

	err := mediaAssetCleanupForIntegration(handlers).CleanupRetiredGenerations(ctx, now)
	require.ErrorContains(t, err, failing.ID)
	requireWorkerMediaGenerationPresent(t, db, failing.ID)
	requireWorkerMediaGenerationMissing(t, db, following.ID)

	failFirst.Store(false)
	require.NoError(t, mediaAssetCleanupForIntegration(handlers).CleanupRetiredGenerations(ctx, now))
	requireWorkerMediaGenerationMissing(t, db, failing.ID)
}

func TestHandleCleanupPublicAssetsDeletesObjectsPurgesPrefixAndFinalizesLifecycle(t *testing.T) {
	ctx := context.Background()
	db := newWorkerIntegrationDB(t)
	s3Client, cfg, err := newWorkerIntegrationS3FromSharedLease(ctx)
	require.NoError(t, err)
	now := time.Now().UTC()

	var purged []string
	var purgeAttempts int
	cloudflare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/zones/test-zone/purge_cache", r.URL.Path)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		var payload cloudflarePurgeRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		purged = append(purged, payload.Prefixes...)
		w.Header().Set("Content-Type", "application/json")
		purgeAttempts++
		if purgeAttempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, err := w.Write([]byte(`{"success":false,"errors":[{"message":"retry purge"}]}`))
			require.NoError(t, err)
			return
		}
		_, err := w.Write([]byte(`{"success":true,"errors":[]}`))
		require.NoError(t, err)
	}))
	t.Cleanup(cloudflare.Close)
	cfg.CDNURL = "https://cdn.example.com"
	cfg.CloudflareAPIURL = cloudflare.URL
	cfg.CloudflareZoneID = "test-zone"
	cfg.CloudflareAPIToken = "test-token"

	lifecycle := mediaasset.NewLifecycle(db, cfg.CDNURL)
	asset, target, err := lifecycle.AllocatePublicAsset(ctx, mediaasset.Allocation{
		Kind:        "image",
		Extension:   "webp",
		MimeType:    "image/webp",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	body := "ready asset"
	require.NoError(t, putWorkerIntegrationObject(ctx, s3Client, cfg.S3Bucket, target.GetObjectKey(), body))
	digest := sha256.Sum256([]byte(body))
	_, err = lifecycle.CompletePublicAsset(ctx, &commonv1.AssetWriteResult{
		AssetId: asset.ID, FileSize: int64(len(body)), Sha256: digest[:],
	})
	require.NoError(t, err)
	require.NoError(t, lifecycle.RequestPublicAssetDeletion(ctx, asset.ID))

	stale, staleTarget, err := lifecycle.AllocatePublicAsset(ctx, mediaasset.Allocation{
		Kind:        "image",
		Extension:   "webp",
		MimeType:    "image/webp",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	require.NoError(t, putWorkerIntegrationObject(ctx, s3Client, cfg.S3Bucket, staleTarget.GetObjectKey(), "stale"))
	require.NoError(t, db.Model(&model.PublicAsset{}).Where("id = ?", stale.ID).
		Update("created_at", now.Add(-25*time.Hour)).Error)

	handlers := &Handlers{db: db, config: cfg, s3Client: s3Client, httpClient: cloudflare.Client()}
	ogCleanupForIntegration(handlers)
	faviconCleanupForIntegration(handlers)
	publicAssetCleanupForIntegration(handlers)
	require.NoError(t, handlers.handleCleanupPublicAssets(ctx, now))
	var staleCount int64
	require.NoError(t, db.Model(&model.PublicAsset{}).Where("id = ?", stale.ID).Count(&staleCount).Error)
	require.Zero(t, staleCount, "unready cleanup must continue while a pending asset purge retries")

	var deleted model.PublicAsset
	require.NoError(t, db.First(&deleted, "id = ?", asset.ID).Error)
	require.Equal(t, model.PublicAssetStatusDeleted, deleted.Status)
	require.WithinDuration(t, now, deleted.DeletedAt.UTC(), time.Millisecond)
	require.Equal(t, []string{
		"cdn.example.com/asset/" + asset.ID + "/image.webp",
		"cdn.example.com/asset/" + asset.ID + "/image.webp",
	}, purged)
	require.Empty(t, listWorkerIntegrationKeys(t, ctx, s3Client, cfg.S3Bucket, "asset/"))
}

// newWorkerIntegrationS3 remains the shared worker-test entrypoint used by
// other integration files in this package.
func newWorkerIntegrationS3(t *testing.T) (*s3.Client, *config.Config) {
	t.Helper()
	client, cfg, err := newWorkerIntegrationS3FromSharedLease(t.Context())
	require.NoError(t, err)
	return client, cfg
}

func newWorkerIntegrationS3FromSharedLease(ctx context.Context) (*s3.Client, *config.Config, error) {
	cfg, err := sharedWorkerIntegrationS3Config()
	if err != nil {
		return nil, nil, err
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.S3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.S3AccessKeyID,
			cfg.S3SecretAccessKey,
			"",
		)),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("load shared worker integration S3 config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(cfg.S3Endpoint)
		options.UsePathStyle = cfg.S3ForcePathStyle
	})
	return client, cfg, nil
}

func sharedWorkerIntegrationS3Config() (*config.Config, error) {
	backend, err := testutil.CurrentAppIntegrationBackendLease()
	if err != nil {
		return nil, fmt.Errorf("load shared worker integration backend lease: %w", err)
	}
	cfg := &config.Config{
		S3Bucket:          backend.S3MediaBucket,
		S3Region:          backend.S3Region,
		S3Endpoint:        backend.S3Endpoint,
		S3AccessKeyID:     backend.S3AccessKeyID,
		S3SecretAccessKey: backend.S3SecretAccessKey,
		S3ForcePathStyle:  backend.S3ForcePathStyle,
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "bucket", value: cfg.S3Bucket},
		{name: "region", value: cfg.S3Region},
		{name: "endpoint", value: cfg.S3Endpoint},
		{name: "access key ID", value: cfg.S3AccessKeyID},
		{name: "secret access key", value: cfg.S3SecretAccessKey},
	} {
		if strings.TrimSpace(field.value) == "" {
			return nil, fmt.Errorf("shared worker integration S3 %s is required", field.name)
		}
	}
	return cfg, nil
}

func putWorkerIntegrationObject(ctx context.Context, s3Client *s3.Client, bucket, key, body string) error {
	_, err := s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader([]byte(body)),
	})
	return err
}

func listWorkerIntegrationKeys(t *testing.T, ctx context.Context, s3Client *s3.Client, bucket, prefix string) []string {
	t.Helper()

	paginator := s3.NewListObjectsV2Paginator(s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})

	var keys []string
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		require.NoError(t, err)
		for _, obj := range page.Contents {
			if obj.Key != nil {
				keys = append(keys, *obj.Key)
			}
		}
	}

	sort.Strings(keys)
	return keys
}

func insertWorkerIntegrationFileCreatedAt(db *gorm.DB, fileID string, createdAt time.Time) error {
	digest := sha256.Sum256([]byte(fileID))
	result := db.Exec(`
		INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256, created_at)
		VALUES (?, ?, 'audio/ogg', 12345, 'ogg', ?, ?)
	`, fileID, fileID, digest[:], createdAt)
	return result.Error
}

func seedWorkerRetiredMediaGeneration(
	t *testing.T,
	db *gorm.DB,
	retiredAt time.Time,
	deleteAfter time.Time,
) model.MediaGeneration {
	t.Helper()
	fileID := uuid.NewString()
	require.NoError(t, insertWorkerIntegrationFileCreatedAt(db, fileID, retiredAt.Add(-time.Hour)))
	generationID := uuid.NewString()
	objectPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(generationID))
	readyAt := retiredAt.Add(-time.Hour)
	require.NoError(t, db.Exec(`
		INSERT INTO media_generation (
			id, file_id, kind, object_prefix, manifest_name, manifest_sha256,
			object_count, total_size, status, ready_at, retired_at, delete_after,
			created_at, updated_at
		) VALUES (?, ?, 'hls', ?, 'master.m3u8', ?, 2, 2048, 'retired', ?, ?, ?, ?, ?)
	`, generationID, fileID, objectPrefix, digest[:], readyAt, retiredAt, deleteAfter, readyAt, retiredAt).Error)
	return model.MediaGeneration{
		ID:           generationID,
		FileID:       fileID,
		Kind:         "hls",
		ObjectPrefix: objectPrefix,
		ManifestName: "master.m3u8",
		Status:       model.MediaGenerationStatusRetired,
		ReadyAt:      &readyAt,
		RetiredAt:    &retiredAt,
		DeleteAfter:  &deleteAfter,
	}
}

func requireWorkerMediaGenerationPresent(t *testing.T, db *gorm.DB, generationID string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&model.MediaGeneration{}).Where("id = ?", generationID).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func requireWorkerMediaGenerationMissing(t *testing.T, db *gorm.DB, generationID string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&model.MediaGeneration{}).Where("id = ?", generationID).Count(&count).Error)
	require.Zero(t, count)
}
