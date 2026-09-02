package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	filemediaruntime "github.com/echovisionlab/geul-api/internal/adapters/filemedia/runtime"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	filemediaapplication "github.com/echovisionlab/geul-api/internal/filemedia/application"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	transcodestate "github.com/echovisionlab/geul-api/internal/transcode"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/stretchr/testify/require"
)

type inMemoryFileAuthorizationDeletion struct {
	deleted  []policyv1.Resource
	restored []policyv1.Resource
}

func (f *inMemoryFileAuthorizationDeletion) DeleteAndVerify(
	_ context.Context,
	resource policyv1.Resource,
) (func(context.Context) error, time.Time, error) {
	f.deleted = append(f.deleted, resource)
	return func(context.Context) error {
		f.restored = append(f.restored, resource)
		return nil
	}, time.Now(), nil
}

func newInMemoryFileAuthorizationDeletion() *inMemoryFileAuthorizationDeletion {
	return &inMemoryFileAuthorizationDeletion{}
}

func newWorkerFileMediaRuntime(
	db *gorm.DB,
	s3Client *s3.Client,
	authorization filemediaapplication.AuthorizationDeletion,
) *filemediaruntime.Runtime {
	return filemediaruntime.New(db, nil, nil, nil, s3Client, "media", authorization)
}

func TestParseFileDeleteEventAcceptsCurrentPayloads(t *testing.T) {
	t.Parallel()

	fileID := uuid.NewString()
	generationID := uuid.NewString()
	originalKey, err := mediaauth.MediaObjectKey(fileID, "bin")
	require.NoError(t, err)
	generationPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	protoEvent := &managev1.FileDeleteEvent{
		FileId: fileID,
		Original: &commonv1.MediaObjectTarget{
			FileId:    fileID,
			ObjectKey: originalKey,
			Extension: "bin",
			MimeType:  "application/octet-stream",
		},
		Generations: []*commonv1.MediaGenerationWriteTarget{
			{GenerationId: generationID, FileId: fileID, ObjectPrefix: generationPrefix},
		},
	}
	protoBody, err := proto.Marshal(protoEvent)
	require.NoError(t, err)
	jsonBody, err := protojson.Marshal(protoEvent)
	require.NoError(t, err)

	tests := []struct {
		name    string
		message mq.Message
		want    *managev1.FileDeleteEvent
	}{
		{
			name:    "protobuf binary",
			message: mq.Message{Body: protoBody, ContentType: "application/x-protobuf"},
			want:    protoEvent,
		},
		{
			name:    "protobuf json",
			message: mq.Message{Body: jsonBody, ContentType: "application/json"},
			want:    protoEvent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseFileDeleteEvent(tt.message)
			require.NoError(t, err)
			require.True(t, proto.Equal(tt.want, got), "parsed event mismatch\nwant: %v\ngot:  %v", tt.want, got)
		})
	}
}

func TestParseFileDeleteEventRejectsMalformedPayloads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message mq.Message
	}{
		{
			name:    "empty",
			message: mq.Message{Body: []byte("   "), ContentType: "text/plain"},
		},
		{
			name:    "non json garbage",
			message: mq.Message{Body: []byte("not a file delete event"), ContentType: "text/plain"},
		},
		{
			name:    "json missing targets",
			message: mq.Message{Body: []byte(`{"fileId":"file-1"}`), ContentType: "application/json"},
		},
		{
			name:    "empty protobuf json object",
			message: mq.Message{Body: []byte(`{}`), ContentType: "application/json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseFileDeleteEvent(tt.message)
			require.Error(t, err)
			require.True(t, errors.Is(err, errInvalidFileDeletePayload))
		})
	}
}

func TestHandleFileDeleteMessageRoutesMalformedPayloadDirectlyToDLQ(t *testing.T) {
	t.Parallel()

	err := (&Handlers{}).handleFileDeleteMessage(context.Background(), mq.Message{
		Body:        []byte("private/object/key owner@example.test"),
		ContentType: "text/plain",
	})
	require.EqualError(t, err, "invalid file delete event: invalid file delete payload: unsupported encoding")
	require.NotContains(t, err.Error(), "private/object/key")
	require.NotContains(t, err.Error(), "owner@example.test")
	class, terminal := mq.TerminalDeliveryErrorClass(err)
	require.True(t, terminal)
	require.Equal(t, "invalid_file_delete_payload", class)
}

func TestHandleFileDeleteDeletesEveryTarget(t *testing.T) {
	t.Parallel()

	db := newFileDeleteWorkerUnitDB(t)
	file := seedPendingWorkerFileDeletion(t, db, "video/mp4", "mp4")
	original, err := filemedia.CanonicalMediaObjectTargetForFile(file)
	require.NoError(t, err)
	generationID := uuid.NewString()
	generationPrefix, err := mediaauth.MediaHLSObjectPrefix(file.ID, generationID)
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.MediaGeneration{
		ID: generationID, FileID: file.ID, Kind: "hls", ObjectPrefix: generationPrefix,
		ManifestName: "master.m3u8", Status: model.MediaGenerationStatusRetired,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	var requests []string
	s3Client := newFileDeleteTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>media</Name><Prefix>media/file-1/hls/generation-1/</Prefix><KeyCount>1</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated><Contents><Key>media/file-1/hls/generation-1/master.m3u8</Key><Size>1</Size></Contents></ListBucketResult>`)
		case r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/xml")
			_, _ = fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`)
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	publisher := &bulkEmailUnitPublisher{}
	handlers := &Handlers{
		publisher:        publisher,
		fileMediaRuntime: newWorkerFileMediaRuntime(db, s3Client, newInMemoryFileAuthorizationDeletion()),
	}

	err = handlers.HandleFileDelete(context.Background(), &managev1.FileDeleteEvent{
		FileId:   file.ID,
		Original: original,
		Generations: []*commonv1.MediaGenerationWriteTarget{{
			FileId:       file.ID,
			GenerationId: generationID,
			ObjectPrefix: generationPrefix,
		}},
	})
	require.NoError(t, err)
	var count int64
	require.NoError(t, db.Table("file").Where("id = ?", file.ID).Count(&count).Error)
	require.Zero(t, count)
	require.Empty(t, publisher.publishedEvents)
	require.Len(t, requests, 3)
	require.Contains(t, requests, "DELETE /media/"+original.GetObjectKey()+"?x-id=DeleteObject")
}

func TestHandleFileDeleteRejectsCleanupOnlyTargetsBeforeStorage(t *testing.T) {
	t.Parallel()
	db := newFileDeleteWorkerUnitDB(t)
	file := seedWorkerFile(t, db, "video/mp4", "mp4")
	_, assetTarget := seedWorkerCleanupAsset(t, db, file.ID)
	_, generationTarget := seedWorkerCleanupGeneration(t, db, file.ID)
	storageCalled := false
	s3Client := newFileDeleteTestS3Client(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		storageCalled = true
	}))
	authorization := newInMemoryFileAuthorizationDeletion()
	handlers := &Handlers{fileMediaRuntime: newWorkerFileMediaRuntime(db, s3Client, authorization)}

	err := handlers.HandleFileDelete(t.Context(), &managev1.FileDeleteEvent{
		FileId: file.ID, Assets: []*commonv1.AssetWriteTarget{assetTarget},
		Generations: []*commonv1.MediaGenerationWriteTarget{generationTarget},
	})
	require.ErrorIs(t, err, filemediaapplication.ErrInvalidFileDeleteTarget)
	require.False(t, storageCalled)
}

func TestHandleFileDeleteRejectsIncompleteFullGenerationSetBeforeStorage(t *testing.T) {
	t.Parallel()
	db := newFileDeleteWorkerUnitDB(t)
	file := seedPendingWorkerFileDeletion(t, db, "video/mp4", "mp4")
	_, _ = seedWorkerCleanupGeneration(t, db, file.ID)
	original, err := filemedia.CanonicalMediaObjectTargetForFile(file)
	require.NoError(t, err)
	storageCalled := false
	handlers := &Handlers{
		fileMediaRuntime: newWorkerFileMediaRuntime(db, newFileDeleteTestS3Client(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			storageCalled = true
		})), nil),
	}

	err = handlers.HandleFileDelete(t.Context(), &managev1.FileDeleteEvent{FileId: file.ID, Original: original})
	require.ErrorIs(t, err, filemediaapplication.ErrInvalidFileDeleteTarget)
	require.False(t, storageCalled)
}

func TestHandleFileDeleteMessageRoutesCleanupOnlyPayloadToTerminalDLQ(t *testing.T) {
	t.Parallel()
	db := newFileDeleteWorkerUnitDB(t)
	eventFile := seedWorkerFile(t, db, "video/mp4", "mp4")
	otherFile := seedWorkerFile(t, db, "video/mp4", "mp4")
	_, target := seedWorkerCleanupAsset(t, db, otherFile.ID)
	event := &managev1.FileDeleteEvent{FileId: eventFile.ID, Assets: []*commonv1.AssetWriteTarget{target}}
	body, err := proto.Marshal(event)
	require.NoError(t, err)
	storageCalled := false
	handlers := &Handlers{
		fileMediaRuntime: newWorkerFileMediaRuntime(db, newFileDeleteTestS3Client(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			storageCalled = true
		})), nil),
	}

	err = handlers.handleFileDeleteMessage(t.Context(), mq.Message{Body: body, MessageID: eventFile.ID})
	require.ErrorIs(t, err, errInvalidFileDeletePayload)
	class, terminal := mq.TerminalDeliveryErrorClass(err)
	require.True(t, terminal)
	require.Equal(t, "invalid_file_delete_payload", class)
	require.False(t, storageCalled)
}

func TestHandleFileDeleteReturnsStorageFailuresWithoutPublishingCompletion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		event *managev1.FileDeleteEvent
	}{
		{
			name: "original",
			event: &managev1.FileDeleteEvent{FileId: "file-1", Original: &commonv1.MediaObjectTarget{
				ObjectKey: "media/file-1.mp4",
			}},
		},
		{
			name: "asset",
			event: &managev1.FileDeleteEvent{FileId: "file-1", Assets: []*commonv1.AssetWriteTarget{{
				ObjectKey: "asset/asset-1.webp",
			}}},
		},
		{
			name: "generation",
			event: &managev1.FileDeleteEvent{FileId: "file-1", Generations: []*commonv1.MediaGenerationWriteTarget{{
				ObjectPrefix: "media/file-1/hls/generation-1/",
			}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := newFileDeleteWorkerUnitDB(t)
			if tt.name == "original" {
				file := seedPendingWorkerFileDeletion(t, db, "video/mp4", "mp4")
				original, err := filemedia.CanonicalMediaObjectTargetForFile(file)
				require.NoError(t, err)
				tt.event = &managev1.FileDeleteEvent{FileId: file.ID, Original: original}
			} else {
				file := seedWorkerFile(t, db, "video/mp4", "mp4")
				if tt.name == "asset" {
					_, target := seedWorkerCleanupAsset(t, db, file.ID)
					tt.event = &managev1.FileDeleteEvent{FileId: file.ID, Assets: []*commonv1.AssetWriteTarget{target}}
				} else {
					_, target := seedWorkerCleanupGeneration(t, db, file.ID)
					tt.event = &managev1.FileDeleteEvent{FileId: file.ID, Generations: []*commonv1.MediaGenerationWriteTarget{target}}
				}
			}
			s3Client := newFileDeleteTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = fmt.Fprint(w, `<Error><Code>InternalError</Code><Message>injected failure</Message></Error>`)
			}))
			publisher := &bulkEmailUnitPublisher{}
			handlers := &Handlers{
				publisher:        publisher,
				fileMediaRuntime: newWorkerFileMediaRuntime(db, s3Client, nil),
			}

			err := handlers.HandleFileDelete(context.Background(), tt.event)
			require.Error(t, err)
			require.Empty(t, publisher.publishedEvents)
		})
	}
}

func TestHandleFileDeleteRetriesAfterS3SuccessAndDatabaseFinalizationFailure(t *testing.T) {
	db := newFileDeleteWorkerUnitDB(t)
	file := seedPendingWorkerFileDeletion(t, db, "audio/wav", "wav")
	original, err := filemedia.CanonicalMediaObjectTargetForFile(file)
	require.NoError(t, err)
	var deleteCalls int
	s3Client := newFileDeleteTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		deleteCalls++
		w.WriteHeader(http.StatusNoContent)
	}))
	authorization := newInMemoryFileAuthorizationDeletion()
	handlers := &Handlers{fileMediaRuntime: newWorkerFileMediaRuntime(db, s3Client, authorization)}
	require.NoError(t, db.Exec(`
		CREATE TRIGGER fail_file_delete BEFORE DELETE ON file
		BEGIN SELECT RAISE(ABORT, 'injected finalization failure'); END
	`).Error)
	event := &managev1.FileDeleteEvent{FileId: file.ID, Original: original}

	require.Error(t, handlers.HandleFileDelete(t.Context(), event))
	var retained model.File
	require.NoError(t, db.First(&retained, "id = ?", file.ID).Error)
	require.NotNil(t, retained.DeleteRequestedAt)
	require.Len(t, authorization.restored, 1)
	require.NoError(t, db.Exec("DROP TRIGGER fail_file_delete").Error)
	require.NoError(t, handlers.HandleFileDelete(t.Context(), event))
	require.ErrorIs(t, db.First(&retained, "id = ?", file.ID).Error, gorm.ErrRecordNotFound)
	require.Equal(t, 2, deleteCalls, "S3 delete must be safely repeated after a finalization crash")
	require.Len(t, authorization.deleted, 2)
}

func TestHandleFileDeleteDefersFinalizationUntilLateMeshResultIsRetired(t *testing.T) {
	db := newFileDeleteWorkerUnitDB(t)
	file := seedPendingWorkerFileDeletion(t, db, "model/gltf-binary", "glb")
	original, err := filemedia.CanonicalMediaObjectTargetForFile(file)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		INSERT INTO mesh_optimization_candidate (id, source_file_id, status, selected_at)
		VALUES (?, ?, ?, NULL)
	`, uuid.NewString(), file.ID, model.MeshOptimizationCandidateStatusCancelled).Error)
	var deleteCalls int
	s3Client := newFileDeleteTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		deleteCalls++
		w.WriteHeader(http.StatusNoContent)
	}))
	handlers := &Handlers{fileMediaRuntime: newWorkerFileMediaRuntime(db, s3Client, newInMemoryFileAuthorizationDeletion())}
	event := &managev1.FileDeleteEvent{FileId: file.ID, Original: original}

	err = handlers.HandleFileDelete(t.Context(), event)
	require.ErrorContains(t, err, "pending mesh optimization result")
	var retained model.File
	require.NoError(t, db.First(&retained, "id = ?", file.ID).Error)
	require.NoError(t, db.Exec("DELETE FROM mesh_optimization_candidate WHERE source_file_id = ?", file.ID).Error)
	require.NoError(t, handlers.HandleFileDelete(t.Context(), event))
	require.ErrorIs(t, db.First(&retained, "id = ?", file.ID).Error, gorm.ErrRecordNotFound)
	require.Equal(t, 2, deleteCalls, "S3 deletion remains idempotent while source finalization waits for a late result")
}

func TestHandleFileDeleteDefersStorageUntilTranscodeWriterFinishes(t *testing.T) {
	db := newFileDeleteWorkerUnitDB(t)
	file := seedPendingWorkerFileDeletion(t, db, "video/mp4", "mp4")
	original, err := filemedia.CanonicalMediaObjectTargetForFile(file)
	require.NoError(t, err)
	eventID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO transcode_job (event_id, file_id, status)
		VALUES (?, ?, ?)
	`, eventID, file.ID, transcodestate.StatusCancelled).Error)
	var deleteCalls int
	s3Client := newFileDeleteTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		deleteCalls++
		w.WriteHeader(http.StatusNoContent)
	}))
	handlers := &Handlers{fileMediaRuntime: newWorkerFileMediaRuntime(db, s3Client, newInMemoryFileAuthorizationDeletion())}
	event := &managev1.FileDeleteEvent{FileId: file.ID, Original: original}

	err = handlers.HandleFileDelete(t.Context(), event)
	require.ErrorContains(t, err, "pending transcode")
	require.Zero(t, deleteCalls, "storage deletion must not race an allocated transcode writer")
	var retained model.File
	require.NoError(t, db.First(&retained, "id = ?", file.ID).Error)

	require.NoError(t, db.Exec("DELETE FROM transcode_job WHERE event_id = ?", eventID).Error)
	require.NoError(t, handlers.HandleFileDelete(t.Context(), event))
	require.Equal(t, 1, deleteCalls)
	require.ErrorIs(t, db.First(&retained, "id = ?", file.ID).Error, gorm.ErrRecordNotFound)
}

func newFileDeleteWorkerUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
			CREATE TABLE file (
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
	createFileAttachmentReferenceTablesForWorkerTests(t, db)
	return db
}

func createFileAttachmentReferenceTablesForWorkerTests(t *testing.T, db *gorm.DB) {
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

func seedPendingWorkerFileDeletion(t *testing.T, db *gorm.DB, mimeType, extension string) model.File {
	t.Helper()
	now := time.Now().UTC()
	file := model.File{
		ID: uuid.NewString(), FileName: "source." + extension, MimeType: mimeType,
		FileSize: 1, Extension: extension, SHA256: make([]byte, 32),
		DeleteRequestedAt: &now, CreatedAt: now,
	}
	require.NoError(t, db.Create(&file).Error)
	return file
}

func seedWorkerFile(t *testing.T, db *gorm.DB, mimeType, extension string) model.File {
	t.Helper()
	file := model.File{
		ID: uuid.NewString(), FileName: "source." + extension, MimeType: mimeType,
		FileSize: 1, Extension: extension, SHA256: make([]byte, 32), CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, db.Create(&file).Error)
	return file
}

func seedWorkerCleanupAsset(
	t *testing.T,
	db *gorm.DB,
	fileID string,
) (model.PublicAsset, *commonv1.AssetWriteTarget) {
	t.Helper()
	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, "webp")
	require.NoError(t, err)
	now := time.Now().UTC()
	asset := model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: "thumbnail", ObjectKey: objectKey,
		Extension: "webp", MimeType: "image/webp", Disposition: "inline",
		Status: model.PublicAssetStatusAllocated, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&asset).Error)
	return asset, &commonv1.AssetWriteTarget{
		AssetId: asset.ID, ObjectKey: asset.ObjectKey, Extension: asset.Extension,
		MimeType: asset.MimeType, Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}
}

func seedWorkerCleanupGeneration(
	t *testing.T,
	db *gorm.DB,
	fileID string,
) (model.MediaGeneration, *commonv1.MediaGenerationWriteTarget) {
	t.Helper()
	generationID := uuid.NewString()
	objectPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	now := time.Now().UTC()
	generation := model.MediaGeneration{
		ID: generationID, FileID: fileID, Kind: "hls", ObjectPrefix: objectPrefix,
		ManifestName: "master.m3u8", Status: model.MediaGenerationStatusAllocated,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&generation).Error)
	return generation, &commonv1.MediaGenerationWriteTarget{
		GenerationId: generation.ID, FileId: generation.FileID, ObjectPrefix: generation.ObjectPrefix,
	}
}

func newFileDeleteTestS3Client(t *testing.T, handler http.Handler) *s3.Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	awsConfig := aws.Config{
		Region:      "us-east-1",
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider("test", "test", "")),
		HTTPClient:  server.Client(),
		Retryer: func() aws.Retryer {
			return aws.NopRetryer{}
		},
	}
	return s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(server.URL)
		options.UsePathStyle = true
	})
}
