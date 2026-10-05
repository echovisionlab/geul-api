package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type recordingDeletionAuthorization struct{ calls int }

func (a *recordingDeletionAuthorization) DeleteAndVerify(context.Context, policyv1.Resource) (func(context.Context) error, time.Time, error) {
	a.calls++
	return func(context.Context) error { return nil }, time.Now(), nil
}

func TestHandleFileDeleteKeepsFileAndAuthorizationUntilEveryObjectIsDeleted(t *testing.T) {
	db := newRuntimeFileDeleteTestDB(t)
	now := time.Now().UTC()
	file := model.File{ID: uuid.NewString(), FileName: "source.mp4", MimeType: "video/mp4", FileSize: 1, Extension: "mp4", SHA256: make([]byte, 32), DeleteRequestedAt: &now, CreatedAt: now}
	require.NoError(t, db.Create(&file).Error)
	original, err := filemedia.CanonicalMediaObjectTargetForFile(file)
	require.NoError(t, err)
	generationID := uuid.NewString()
	prefix, err := mediaauth.MediaHLSObjectPrefix(file.ID, generationID)
	require.NoError(t, err)
	key := prefix + "master.m3u8"
	require.NoError(t, db.Create(&model.MediaGeneration{ID: generationID, FileID: file.ID, Kind: "hls", ObjectPrefix: prefix, ManifestName: "master.m3u8", Status: model.MediaGenerationStatusRetired, CreatedAt: now, UpdatedAt: now}).Error)

	var storageMu sync.Mutex
	storedKeys := map[string]bool{key: true, original.GetObjectKey(): true}
	deleteBatches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		storageMu.Lock()
		defer storageMu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		switch {
		case request.Method == http.MethodDelete && request.URL.Path == "/media/"+original.GetObjectKey():
			delete(storedKeys, original.GetObjectKey())
			w.WriteHeader(http.StatusNoContent)
		case request.Method == http.MethodGet && request.URL.Query().Get("list-type") == "2" && request.URL.Query().Get("prefix") == prefix:
			fmt.Fprintf(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>media</Name><Prefix>%s</Prefix><KeyCount>1</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated><Contents><Key>%s</Key><Size>1</Size></Contents></ListBucketResult>`, prefix, key)
		case request.Method == http.MethodPost && request.URL.Query().Has("delete"):
			deleteBatches++
			if deleteBatches == 1 {
				// Object-level failure is returned with HTTP 200 even in quiet mode.
				fmt.Fprintf(w, `<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Error><Key>%s</Key><Code>AccessDenied</Code><Message>injected per-key failure</Message></Error></DeleteResult>`, key)
			} else {
				delete(storedKeys, key)
				fmt.Fprint(w, `<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`)
			}
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	client := s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider("test", "test", "")), HTTPClient: server.Client(), Retryer: func() aws.Retryer { return aws.NopRetryer{} }}, func(options *s3.Options) { options.BaseEndpoint = aws.String(server.URL); options.UsePathStyle = true })
	authorization := &recordingDeletionAuthorization{}
	runtime := New(db, nil, nil, nil, client, "media", authorization)
	event := &managev1.FileDeleteEvent{FileId: file.ID, Original: original, Generations: []*commonv1.MediaGenerationWriteTarget{{FileId: file.ID, GenerationId: generationID, ObjectPrefix: prefix}}}

	err = runtime.HandleFileDelete(t.Context(), event)
	require.ErrorContains(t, err, "AccessDenied")
	require.ErrorContains(t, err, key)
	var retained model.File
	require.NoError(t, db.First(&retained, "id = ?", file.ID).Error)
	require.NotNil(t, retained.DeleteRequestedAt)
	require.Zero(t, authorization.calls)
	storageMu.Lock()
	generationRetained := storedKeys[key]
	originalRetained := storedKeys[original.GetObjectKey()]
	storageMu.Unlock()
	require.True(t, generationRetained)
	require.False(t, originalRetained)

	require.NoError(t, runtime.HandleFileDelete(t.Context(), event))
	var count int64
	require.NoError(t, db.Table("file").Where("id = ?", file.ID).Count(&count).Error)
	require.Zero(t, count)
	require.Equal(t, 1, authorization.calls)
	storageMu.Lock()
	remaining := len(storedKeys)
	batches := deleteBatches
	storageMu.Unlock()
	require.Zero(t, remaining)
	require.Equal(t, 2, batches)
}

func newRuntimeFileDeleteTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(`
			CREATE TABLE file ( client_media_bundle_id TEXT,
			id TEXT PRIMARY KEY, file_name TEXT NOT NULL, mime_type TEXT NOT NULL,
			file_size INTEGER NOT NULL, extension TEXT NOT NULL, sha256 BLOB NOT NULL,
			duration_seconds INTEGER, ingest_slot_id TEXT, ingest_attempt_id TEXT,
			delete_requested_at DATETIME,
			created_at DATETIME NOT NULL
			)
		`).Error)
	require.NoError(t, db.Exec(`
			CREATE TABLE mesh_optimization_candidate (
				id TEXT PRIMARY KEY,
				source_file_id TEXT NOT NULL,
				output_object_id TEXT,
				output_file_id TEXT,
				status TEXT NOT NULL,
				selected_at DATETIME,
				cancelled_at DATETIME,
				expires_at DATETIME,
				updated_at DATETIME
			)
		`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE media_generation (
			id TEXT PRIMARY KEY, file_id TEXT NOT NULL, kind TEXT NOT NULL,
			object_prefix TEXT NOT NULL, manifest_name TEXT NOT NULL,
			manifest_sha256 BLOB, object_count INTEGER, total_size INTEGER,
			status TEXT NOT NULL, ready_at DATETIME, retired_at DATETIME,
			delete_after DATETIME, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL
		);
		CREATE TABLE public_asset (
			id TEXT PRIMARY KEY, source_file_id TEXT, kind TEXT NOT NULL,
			object_key TEXT NOT NULL, extension TEXT NOT NULL, mime_type TEXT NOT NULL,
			file_size INTEGER, sha256 BLOB, disposition TEXT NOT NULL,
			download_filename TEXT, status TEXT NOT NULL, ready_at DATETIME,
			delete_requested_at DATETIME, deleted_at DATETIME, failed_at DATETIME,
			failure_reason TEXT, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL
		);
		CREATE TABLE file_derivative (
			id TEXT PRIMARY KEY, file_id TEXT NOT NULL, type TEXT NOT NULL,
			asset_id TEXT, media_generation_id TEXT, created_at DATETIME
		);
		CREATE TABLE public_asset_binding (
			asset_id TEXT NOT NULL, owner_type TEXT NOT NULL, owner_id TEXT NOT NULL,
			binding_key TEXT NOT NULL, source_file_id TEXT, created_at DATETIME, updated_at DATETIME
		);
		CREATE TABLE transcode_job (
			event_id TEXT PRIMARY KEY, file_id TEXT NOT NULL, status TEXT NOT NULL
		);
		CREATE TABLE waveform_job (
			event_id TEXT PRIMARY KEY, file_id TEXT NOT NULL, status TEXT NOT NULL
		);
	`).Error)
	createRuntimeFileReferenceTestTables(t, db)
	return db
}

func createRuntimeFileReferenceTestTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS artist_file (file_id TEXT);
		CREATE TABLE IF NOT EXISTS release_file (file_id TEXT);
		CREATE TABLE IF NOT EXISTS content_block_attachment (block_id TEXT, reference_path TEXT, selector_kind TEXT, file_id TEXT, missing_kind TEXT);
		CREATE TABLE IF NOT EXISTS post (id TEXT PRIMARY KEY, featured_image_file_id TEXT);
		CREATE TABLE IF NOT EXISTS page (id TEXT PRIMARY KEY, featured_image_file_id TEXT);
		CREATE TABLE IF NOT EXISTS work (id TEXT PRIMARY KEY, featured_image_file_id TEXT);
		CREATE TABLE IF NOT EXISTS program_event_media (file_id TEXT);
		CREATE TABLE IF NOT EXISTS track (audio_original_file_id TEXT);
		CREATE TABLE IF NOT EXISTS client (logo_light_file_id TEXT, logo_dark_file_id TEXT);
		CREATE TABLE IF NOT EXISTS label (logo_light_file_id TEXT, logo_dark_file_id TEXT);
		CREATE TABLE IF NOT EXISTS series (featured_image_file_id TEXT);
		CREATE TABLE IF NOT EXISTS form (featured_image_file_id TEXT);
		CREATE TABLE IF NOT EXISTS map_place (image_file_id TEXT);
		CREATE TABLE IF NOT EXISTS program_event_series (poster_file_id TEXT);
		CREATE TABLE IF NOT EXISTS site_settings (
			logo_light_file_id TEXT, logo_dark_file_id TEXT, logo_email_file_id TEXT,
			favicon_file_id TEXT, site_og_background_file_id TEXT,
			privacy_og_background_file_id TEXT, terms_og_background_file_id TEXT
		);
		CREATE TABLE IF NOT EXISTS site_setting_loader_file (file_id TEXT);
	`).Error)
}
