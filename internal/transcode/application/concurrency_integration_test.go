//go:build integration

package application_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/adapters/filemediaqueue"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"github.com/echovisionlab/geul-api/internal/transcode/application"
	applicationpostgres "github.com/echovisionlab/geul-api/internal/transcode/application/postgres"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	transcodepkg "github.com/echovisionlab/geul-api/internal/transcode"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestHandleTranscodeCompleteRechecksJobAfterWaitingForFileLock(t *testing.T) {
	t.Parallel()
	pg := newWorkerIntegrationPostgres(t)
	db := pg.DB
	ctx := t.Context()
	fileID := uuid.NewString()
	postID := uuid.NewString()
	seedWorkerTranscodeFile(t, db, fileID, "mp4", "video/mp4")
	lifecycle := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), "")
	_, hlsTarget := seedWorkerAllocatedMediaGeneration(t, db, fileID)
	_, thumbnailTarget, err := mediaasset.AllocationResultProto(lifecycle.AllocatePublicAsset(ctx, mediaasset.Allocation{
		SourceFileID: &fileID, Kind: "thumbnail", Extension: "webp", MimeType: "image/webp",
		Disposition: mediaasset.DispositionInline,
	}))
	require.NoError(t, err)
	source, err := filemediaqueue.CanonicalObjectTarget(filemedia.File{
		ID: fileID, Extension: "mp4", MimeType: "video/mp4",
	})
	require.NoError(t, err)
	trackedEvent := &managev1.TranscodeVideoEvent{
		EventId: uuid.NewString(), EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId: postID, FileId: fileID, Source: source,
		HlsOutput: hlsTarget, ThumbnailOutput: thumbnailTarget,
	}
	seedWorkerTranscodeJobForEntityWithEventID(
		t, db, trackedEvent.EventId, trackedEvent.EntityType, postID, fileID,
		eventpkg.QueueTranscoderVideo, transcodepkg.StatusQueued, trackedEvent,
	)
	t.Cleanup(func() {
		_ = db.Where("file_id = ?", fileID).Delete(&model.FileDerivative{}).Error
		_ = db.Where("event_id = ?", trackedEvent.EventId).Delete(&model.TranscodeJob{}).Error
		_ = db.Where("id = ?", thumbnailTarget.GetAssetId()).Delete(&model.PublicAsset{}).Error
		_ = db.Where("id = ?", hlsTarget.GetGenerationId()).Delete(&model.MediaGeneration{}).Error
		_ = db.Where("id = ?", fileID).Delete(&model.File{}).Error
	})

	lockTx := db.Begin()
	require.NoError(t, lockTx.Error)
	var lockedFile model.File
	require.NoError(t, lockTx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id").Where("id = ?", fileID).Take(&lockedFile).Error)

	body, err := proto.Marshal(&managev1.TranscodeCompleteEvent{
		EventId: trackedEvent.EventId, EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO,
		EntityType: trackedEvent.EntityType, EntityId: postID, FileId: fileID, Success: true,
		Outputs: &managev1.TranscodeOutputs{
			Hls: &commonv1.MediaGenerationWriteResult{
				GenerationId: hlsTarget.GetGenerationId(), ManifestSha256: bytes.Repeat([]byte{0x41}, 32),
				ObjectCount: 3, TotalSize: 4096,
			},
			Thumbnail: &commonv1.AssetWriteResult{
				AssetId: thumbnailTarget.GetAssetId(), FileSize: 1024, Sha256: bytes.Repeat([]byte{0x42}, 32),
			},
		},
	})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		done <- newWorkerTranscodeIntegrationHandlers(db).HandleTranscodeComplete(context.Background(), body)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting int64
		require.NoError(t, db.Raw(`
			SELECT COUNT(*)
			  FROM pg_stat_activity
			 WHERE datname = current_database()
			   AND wait_event_type = 'Lock'
			   AND query ILIKE '%file%'
		`).Scan(&waiting).Error)
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completion did not wait for the held File lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NoError(t, lockTx.Model(&model.TranscodeJob{}).
		Where("event_id = ?", trackedEvent.EventId).
		Update("status", transcodepkg.StatusCancelled).Error)
	require.NoError(t, lockTx.Commit().Error)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("completion did not resume after the File lock was released")
	}
	var generation model.MediaGeneration
	require.NoError(t, db.Where("id = ?", hlsTarget.GetGenerationId()).Take(&generation).Error)
	require.Equal(t, model.MediaGenerationStatusRetired, generation.Status)
	var asset model.PublicAsset
	require.NoError(t, db.Where("id = ?", thumbnailTarget.GetAssetId()).Take(&asset).Error)
	require.Equal(t, model.PublicAssetStatusDeletePending, asset.Status)
	requireWorkerTranscodeDerivativeAbsent(t, db, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String())
	requireWorkerTranscodeDerivativeAbsent(t, db, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL.String())
}

func TestHandleTrackTranscodeCompleteDoesNotMutateOwnerAfterJobCancellation(t *testing.T) {
	t.Parallel()
	pg := newWorkerIntegrationPostgres(t)
	db := pg.DB
	ctx := t.Context()
	fileID := uuid.NewString()
	releaseID := uuid.NewString()
	trackID := uuid.NewString()
	seedWorkerTranscodeFile(t, db, fileID, "ogg", "audio/ogg")
	documentID := seedWorkerTranscodeRelease(t, db, releaseID)
	require.NoError(t, db.Exec(`
		INSERT INTO track (
			id, release_id, track_number, title, duration_seconds,
			audio_original_file_id, processing_status
		) VALUES (?, ?, 1, 'Race track', 11, ?, 'before')
	`, trackID, releaseID, fileID).Error)
	lifecycle := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), "")
	_, hlsTarget := seedWorkerAllocatedMediaGeneration(t, db, fileID)
	_, spectrogramTarget, err := mediaasset.AllocationResultProto(lifecycle.AllocatePublicAsset(ctx, mediaasset.Allocation{
		SourceFileID: &fileID, Kind: "spectrogram", Extension: "png", MimeType: "image/png",
		Disposition: mediaasset.DispositionInline,
	}))
	require.NoError(t, err)
	source, err := filemediaqueue.CanonicalObjectTarget(filemedia.File{
		ID: fileID, Extension: "ogg", MimeType: "audio/ogg",
	})
	require.NoError(t, err)
	trackedEvent := &managev1.TranscodeAudioEvent{
		EventId: uuid.NewString(), EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
		EntityId: trackID, FileId: fileID, Source: source,
		HlsOutput: hlsTarget, SpectrogramOutput: spectrogramTarget,
	}
	seedWorkerTranscodeJobForEntityWithEventID(
		t, db, trackedEvent.EventId, trackedEvent.EntityType, trackID, fileID,
		eventpkg.QueueTranscoderAudio, transcodepkg.StatusQueued, trackedEvent,
	)
	t.Cleanup(func() {
		_ = db.Where("file_id = ?", fileID).Delete(&model.FileDerivative{}).Error
		_ = db.Where("event_id = ?", trackedEvent.EventId).Delete(&model.TranscodeJob{}).Error
		_ = db.Where("id = ?", spectrogramTarget.GetAssetId()).Delete(&model.PublicAsset{}).Error
		_ = db.Where("id = ?", hlsTarget.GetGenerationId()).Delete(&model.MediaGeneration{}).Error
		_ = db.Where("id = ?", trackID).Delete(&model.Track{}).Error
		_ = db.Exec("DELETE FROM release WHERE id = ?", releaseID).Error
		_ = db.Exec("DELETE FROM content_document WHERE id = ?", documentID).Error
		_ = db.Where("id = ?", fileID).Delete(&model.File{}).Error
	})

	lockTx := db.Begin()
	require.NoError(t, lockTx.Error)
	var lockedFile model.File
	require.NoError(t, lockTx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id").Where("id = ?", fileID).Take(&lockedFile).Error)
	duration := int32(99)
	body, err := proto.Marshal(&managev1.TranscodeCompleteEvent{
		EventId: trackedEvent.EventId, EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		EntityType: trackedEvent.EntityType, EntityId: trackID, FileId: fileID, Success: true,
		Outputs: &managev1.TranscodeOutputs{
			Hls: &commonv1.MediaGenerationWriteResult{
				GenerationId: hlsTarget.GetGenerationId(), ManifestSha256: bytes.Repeat([]byte{0x51}, 32),
				ObjectCount: 3, TotalSize: 4096,
			},
			Spectrogram: &commonv1.AssetWriteResult{
				AssetId: spectrogramTarget.GetAssetId(), FileSize: 1024, Sha256: bytes.Repeat([]byte{0x52}, 32),
			},
			DurationSeconds: &duration,
		},
	})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		done <- newWorkerTranscodeIntegrationHandlers(db).HandleTranscodeComplete(context.Background(), body)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting int64
		require.NoError(t, db.Raw(`
			SELECT COUNT(*)
			  FROM pg_stat_activity
			 WHERE datname = current_database()
			   AND wait_event_type = 'Lock'
			   AND query ILIKE '%file%'
		`).Scan(&waiting).Error)
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("track completion did not wait for the held File lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NoError(t, lockTx.Model(&model.TranscodeJob{}).
		Where("event_id = ?", trackedEvent.EventId).
		Update("status", transcodepkg.StatusCancelled).Error)
	require.NoError(t, lockTx.Commit().Error)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("track completion did not resume after the File lock was released")
	}
	var track model.Track
	require.NoError(t, db.Where("id = ?", trackID).Take(&track).Error)
	require.NotNil(t, track.DurationSeconds)
	require.Equal(t, 11, *track.DurationSeconds)
	require.NotNil(t, track.ProcessingStatus)
	require.Equal(t, "before", *track.ProcessingStatus)
}

func TestHandleTrackTranscodeFailureDoesNotOverrideConcurrentCancellation(t *testing.T) {
	t.Parallel()
	pg := newWorkerIntegrationPostgres(t)
	db := pg.DB
	fileID := uuid.NewString()
	releaseID := uuid.NewString()
	trackID := uuid.NewString()
	eventID := uuid.NewString()
	seedWorkerTranscodeFile(t, db, fileID, "ogg", "audio/ogg")
	documentID := seedWorkerTranscodeRelease(t, db, releaseID)
	require.NoError(t, db.Exec(`
		INSERT INTO track (
			id, release_id, track_number, title, duration_seconds,
			audio_original_file_id, processing_status
		) VALUES (?, ?, 1, 'Failure race track', 11, ?, 'before')
	`, trackID, releaseID, fileID).Error)
	seedWorkerTranscodeJobForEntityWithEventID(
		t, db, eventID, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
		trackID, fileID, eventpkg.QueueTranscoderAudio, transcodepkg.StatusQueued,
		&managev1.TranscodeAudioEvent{
			EventId: eventID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
			EntityId: trackID, FileId: fileID,
		},
	)
	t.Cleanup(func() {
		_ = db.Where("event_id = ?", eventID).Delete(&model.TranscodeJob{}).Error
		_ = db.Where("id = ?", trackID).Delete(&model.Track{}).Error
		_ = db.Exec("DELETE FROM release WHERE id = ?", releaseID).Error
		_ = db.Exec("DELETE FROM content_document WHERE id = ?", documentID).Error
		_ = db.Where("id = ?", fileID).Delete(&model.File{}).Error
	})

	lockTx := db.Begin()
	require.NoError(t, lockTx.Error)
	var lockedFile model.File
	require.NoError(t, lockTx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id").Where("id = ?", fileID).Take(&lockedFile).Error)
	failure := "provider failed"
	body, err := proto.Marshal(&managev1.TranscodeCompleteEvent{
		EventId: eventID, EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
		EntityId:   trackID, FileId: fileID, Success: false, Error: &failure,
	})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		done <- newWorkerTranscodeIntegrationHandlers(db).HandleTranscodeComplete(context.Background(), body)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting int64
		require.NoError(t, db.Raw(`
			SELECT COUNT(*)
			  FROM pg_stat_activity
			 WHERE datname = current_database()
			   AND wait_event_type = 'Lock'
			   AND query ILIKE '%file%'
		`).Scan(&waiting).Error)
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("track failure did not wait for the held File lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NoError(t, lockTx.Model(&model.TranscodeJob{}).
		Where("event_id = ?", eventID).
		Update("status", transcodepkg.StatusCancelled).Error)
	require.NoError(t, lockTx.Commit().Error)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("track failure did not resume after the File lock was released")
	}
	var job model.TranscodeJob
	require.NoError(t, db.Where("event_id = ?", eventID).Take(&job).Error)
	require.Equal(t, transcodepkg.StatusCancelled, job.Status)
	var track model.Track
	require.NoError(t, db.Where("id = ?", trackID).Take(&track).Error)
	require.NotNil(t, track.DurationSeconds)
	require.Equal(t, 11, *track.DurationSeconds)
	require.NotNil(t, track.ProcessingStatus)
	require.Equal(t, "before", *track.ProcessingStatus)
}

func seedWorkerTranscodeFile(t *testing.T, db *gorm.DB, fileID string, extension string, mimeType string) {
	t.Helper()

	require.NoError(t, db.Exec(`
		INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256)
		VALUES (?, ?, ?, 12345, ?, decode(repeat('00', 32), 'hex'))
	`, fileID, fileID, mimeType, extension).Error)
}

func seedWorkerTranscodeRelease(t *testing.T, db *gorm.DB, releaseID string) string {
	t.Helper()

	documentID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO content_document (id, profile, revision)
		VALUES (?, 'compact', ?)
	`, documentID, uuid.NewString()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO release (id, type, status, content_document_id)
		VALUES (?, 'RELEASE_TYPE_ALBUM', 'RELEASE_STATUS_DRAFT', ?)
	`, releaseID, documentID).Error)
	return documentID
}

func seedWorkerTranscodeJobForEntityWithEventID(
	t *testing.T,
	db *gorm.DB,
	eventID string,
	entityType managev1.TranscodeEntityType,
	entityID string,
	fileID string,
	queueName string,
	status string,
	payload proto.Message,
) {
	t.Helper()
	payloadBytes, err := proto.Marshal(payload)
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
		INSERT INTO transcode_job (
			event_id,
			queue_name,
			entity_type,
			entity_id,
			file_id,
			payload,
			status
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, eventID, queueName, entityType.String(), entityID, fileID, payloadBytes, status).Error)
}

func requireWorkerTranscodeDerivativeAbsent(t *testing.T, db *gorm.DB, fileID string, derivativeType string) {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("file_derivative").
		Where("file_id = ? AND type = ?", fileID, derivativeType).
		Count(&count).Error)
	require.Equal(t, int64(0), count)
}

func newWorkerIntegrationPostgres(t *testing.T) *pgtest.AppPostgres {
	t.Helper()
	return pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	})
}

func seedWorkerAllocatedMediaGeneration(
	t *testing.T,
	db *gorm.DB,
	fileID string,
) (*model.MediaGeneration, *commonv1.MediaGenerationWriteTarget) {
	t.Helper()
	generationID := uuid.NewString()
	objectPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	now := time.Now().UTC()
	generation := &model.MediaGeneration{
		ID:           generationID,
		FileID:       fileID,
		Kind:         "hls",
		ObjectPrefix: objectPrefix,
		ManifestName: "master.m3u8",
		Status:       model.MediaGenerationStatusAllocated,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	require.NoError(t, db.Create(generation).Error)
	return generation, &commonv1.MediaGenerationWriteTarget{
		GenerationId: generation.ID,
		FileId:       generation.FileID,
		ObjectPrefix: generation.ObjectPrefix,
	}
}

func newWorkerTranscodeIntegrationHandlers(db *gorm.DB) *application.Handlers {
	return application.New(applicationpostgres.NewUnits(db), nil, nil)
}
