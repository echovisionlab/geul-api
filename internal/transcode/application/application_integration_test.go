//go:build integration

package application

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	mediaauth "github.com/echovisionlab/geul-mediaauth"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
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

func TestHandleTranscodeCompleteRejectsPendingDeletionEditorAudioOutput(t *testing.T) {
	db := newWorkerIntegrationDB(t)
	handlers := newWorkerTranscodeIntegrationHandlers(db)
	publisher := &recordingWorkerPublisher{}
	handlers.publisher = publisher

	postID := uuid.NewString()
	fileID := uuid.NewString()
	generationID := uuid.NewString()
	spectrogramAssetID := uuid.NewString()
	jobID := uuid.NewString()
	waveformJobID := uuid.NewString()
	sourceKey, err := mediaauth.MediaObjectKey(fileID, "mp3")
	require.NoError(t, err)
	hlsPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	spectrogramKey, err := mediaauth.AssetObjectKey(spectrogramAssetID, "png")
	require.NoError(t, err)
	seedWorkerTranscodeFile(t, db, fileID, "mp3", "audio/mpeg")
	seedWorkerTranscodeJobForEntityWithEventID(
		t,
		db,
		jobID,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		postID,
		fileID,
		eventpkg.QueueTranscoderAudio,
		transcodepkg.StatusQueued,
		&managev1.TranscodeAudioEvent{
			EventId: jobID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
			EntityId: postID, FileId: fileID,
			Source: &commonv1.MediaObjectTarget{
				FileId: fileID, ObjectKey: sourceKey, Extension: "mp3", MimeType: "audio/mpeg",
			},
			HlsOutput: &commonv1.MediaGenerationWriteTarget{
				GenerationId: generationID, FileId: fileID, ObjectPrefix: hlsPrefix,
			},
			SpectrogramOutput: &commonv1.AssetWriteTarget{
				AssetId: spectrogramAssetID, ObjectKey: spectrogramKey, Extension: "png", MimeType: "image/png",
				Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
			},
		},
	)
	seedWorkerWaveformJobForEntity(
		t,
		db,
		waveformJobID,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		postID,
		fileID,
		transcodepkg.WaveformJobStatusQueued,
	)
	now := time.Now().UTC()
	require.NoError(t, db.Model(&model.File{}).Where("id = ?", fileID).Updates(structured.Fields{
		"delete_requested_at": now,
	}).Error)
	requireWorkerTranscodeJobCountByFileQueue(t, db, fileID, eventpkg.QueueTranscoderAudio, 1)
	require.Len(t, loadWorkerWaveformJobsForFile(t, db, fileID), 1)

	duration := int32(157)
	body, err := proto.Marshal(&managev1.TranscodeCompleteEvent{
		EventId:    jobID,
		FileId:     fileID,
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId:   postID,
		Success:    true,
		EventType:  managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		Outputs: &managev1.TranscodeOutputs{
			Hls:             &commonv1.MediaGenerationWriteResult{GenerationId: generationID},
			Spectrogram:     &commonv1.AssetWriteResult{AssetId: spectrogramAssetID},
			DurationSeconds: &duration,
		},
	})
	require.NoError(t, err)

	require.NoError(t, handlers.HandleTranscodeComplete(context.Background(), body))
	requireWorkerTranscodeDerivativeAbsent(t, db, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String())
	requireWorkerTranscodeDerivativeAbsent(t, db, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM.String())
	requireWorkerTranscodeJobCountByFileQueue(t, db, fileID, eventpkg.QueueTranscoderAudio, 0)
	require.Len(t, loadWorkerWaveformJobsForFile(t, db, fileID), 1,
		"one terminal result must not erase another active writer authority")
	require.Empty(t, publisher.mediaProcessingLifecycleEvents)
	require.Empty(t, publisher.waveformGenerateEvents)
	require.Empty(t, publisher.fileDeleteEvents)
}

func TestHandleTranscodeCompleteRetriesCanonicalOutputsBeforeFinishingTracker(t *testing.T) {
	db := newWorkerIntegrationDB(t)
	fileID := uuid.NewString()
	postID := uuid.NewString()
	seedWorkerTranscodeFile(t, db, fileID, "ogg", "audio/ogg")

	lifecycle := mediaasset.NewLifecycle(db, "")
	_, hlsTarget := seedWorkerAllocatedMediaGeneration(t, db, fileID)
	_, spectrogramTarget, err := lifecycle.AllocatePublicAsset(context.Background(), mediaasset.Allocation{
		SourceFileID: &fileID,
		Kind:         "spectrogram",
		Extension:    "png",
		MimeType:     "image/png",
		Disposition:  commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	source, err := filemedia.CanonicalMediaObjectTargetForFile(model.File{
		ID: fileID, Extension: "ogg", MimeType: "audio/ogg",
	})
	require.NoError(t, err)
	trackedEvent := &managev1.TranscodeAudioEvent{
		EventId: uuid.NewString(), EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId: postID, FileId: fileID, Source: source,
		HlsOutput: hlsTarget, SpectrogramOutput: spectrogramTarget,
	}
	seedWorkerTranscodeJobForEntityWithEventID(
		t, db, trackedEvent.EventId, trackedEvent.EntityType, postID, fileID,
		eventpkg.QueueTranscoderAudio, transcodepkg.StatusQueued, trackedEvent,
	)

	recorder := &recordingWorkerPublisher{}
	publisher := &controlledLifecyclePublisher{
		recordingWorkerPublisher: recorder,
		lifecycleErr:             errors.New("lifecycle unavailable"),
	}
	tracker := &recordingTranscodeJobTracker{}
	handlers := newWorkerTranscodeIntegrationHandlers(db)
	handlers.publisher = publisher
	handlers.transcodeJobs = tracker

	duration := int32(42)
	event := &managev1.TranscodeCompleteEvent{
		EventId:    trackedEvent.EventId,
		EventType:  managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId:   postID,
		FileId:     fileID,
		Success:    true,
		Outputs: &managev1.TranscodeOutputs{
			Hls: &commonv1.MediaGenerationWriteResult{
				GenerationId:   hlsTarget.GetGenerationId(),
				ManifestSha256: bytes.Repeat([]byte{0x11}, 32),
				ObjectCount:    3,
				TotalSize:      4096,
			},
			Spectrogram: &commonv1.AssetWriteResult{
				AssetId:  spectrogramTarget.GetAssetId(),
				FileSize: 1024,
				Sha256:   bytes.Repeat([]byte{0x22}, 32),
			},
			DurationSeconds: &duration,
		},
	}
	body, err := proto.Marshal(event)
	require.NoError(t, err)

	err = handlers.HandleTranscodeComplete(context.Background(), body)
	require.ErrorContains(t, err, "lifecycle unavailable")
	require.Empty(t, tracker.completeEvents)
	require.Len(t, recorder.waveformGenerateEvents, 1)
	requireWorkerDerivativeOutputID(t, db, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS, hlsTarget.GetGenerationId())
	requireWorkerDerivativeOutputID(t, db, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM, spectrogramTarget.GetAssetId())

	publisher.lifecycleErr = nil
	require.NoError(t, handlers.HandleTranscodeComplete(context.Background(), body))
	require.Len(t, tracker.completeEvents, 1)
	require.Len(t, recorder.waveformGenerateEvents, 1)
	require.Len(t, recorder.mediaProcessingLifecycleEvents, 1)

	var generation model.MediaGeneration
	require.NoError(t, db.Where("id = ?", hlsTarget.GetGenerationId()).Take(&generation).Error)
	require.Equal(t, model.MediaGenerationStatusReady, generation.Status)
	require.Nil(t, generation.RetiredAt)
}

func TestHandleTranscodeCompleteCleansSupersededAllocationWithoutSwitchingDerivative(t *testing.T) {
	db := newWorkerIntegrationDB(t)
	ctx := context.Background()
	fileID := uuid.NewString()
	postID := uuid.NewString()
	seedWorkerTranscodeFile(t, db, fileID, "mp4", "video/mp4")
	lifecycle := mediaasset.NewLifecycle(db, "")
	_, hlsTarget := seedWorkerAllocatedMediaGeneration(t, db, fileID)
	_, thumbnailTarget, err := lifecycle.AllocatePublicAsset(ctx, mediaasset.Allocation{
		SourceFileID: &fileID, Kind: "thumbnail", Extension: "webp", MimeType: "image/webp",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	source, err := filemedia.CanonicalMediaObjectTargetForFile(model.File{
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
		eventpkg.QueueTranscoderVideo, transcodepkg.StatusCancelled, trackedEvent,
	)

	recorder := &recordingWorkerPublisher{}
	tracker := &recordingTranscodeJobTracker{}
	handlers := newWorkerTranscodeIntegrationHandlers(db)
	handlers.publisher = recorder
	handlers.transcodeJobs = tracker
	body, err := proto.Marshal(&managev1.TranscodeCompleteEvent{
		EventId:    trackedEvent.EventId,
		EventType:  managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO,
		EntityType: trackedEvent.EntityType, EntityId: postID, FileId: fileID, Success: true,
		Outputs: &managev1.TranscodeOutputs{
			Hls: &commonv1.MediaGenerationWriteResult{
				GenerationId: hlsTarget.GetGenerationId(), ManifestSha256: bytes.Repeat([]byte{0x31}, 32),
				ObjectCount: 3, TotalSize: 4096,
			},
			Thumbnail: &commonv1.AssetWriteResult{
				AssetId: thumbnailTarget.GetAssetId(), FileSize: 1024, Sha256: bytes.Repeat([]byte{0x32}, 32),
			},
		},
	})
	require.NoError(t, err)

	require.NoError(t, handlers.HandleTranscodeComplete(ctx, body))
	require.Len(t, tracker.completeEvents, 1)
	require.Empty(t, recorder.fileDeleteEvents)
	var retired model.MediaGeneration
	require.NoError(t, db.Where("id = ?", hlsTarget.GetGenerationId()).Take(&retired).Error)
	require.Equal(t, model.MediaGenerationStatusRetired, retired.Status)
	require.NotNil(t, retired.DeleteAfter)
	var deletePending model.PublicAsset
	require.NoError(t, db.Where("id = ?", thumbnailTarget.GetAssetId()).Take(&deletePending).Error)
	require.Equal(t, model.PublicAssetStatusDeletePending, deletePending.Status)
	require.NotNil(t, deletePending.DeleteRequestedAt)
	requireWorkerTranscodeDerivativeAbsent(t, db, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String())
	requireWorkerTranscodeDerivativeAbsent(t, db, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL.String())
	require.Empty(t, recorder.mediaProcessingLifecycleEvents)
}

func TestHandleTranscodeCompleteRechecksJobAfterWaitingForFileLock(t *testing.T) {
	pg, err := sharedWorkerIntegrationPostgres()
	require.NoError(t, err)
	db := pg.DB
	ctx := t.Context()
	fileID := uuid.NewString()
	postID := uuid.NewString()
	seedWorkerTranscodeFile(t, db, fileID, "mp4", "video/mp4")
	lifecycle := mediaasset.NewLifecycle(db, "")
	_, hlsTarget := seedWorkerAllocatedMediaGeneration(t, db, fileID)
	_, thumbnailTarget, err := lifecycle.AllocatePublicAsset(ctx, mediaasset.Allocation{
		SourceFileID: &fileID, Kind: "thumbnail", Extension: "webp", MimeType: "image/webp",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	source, err := filemedia.CanonicalMediaObjectTargetForFile(model.File{
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
	require.NoError(t, lockTx.Clauses(clause.Locking{Strength: "UPDATE"}).
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
	pg, err := sharedWorkerIntegrationPostgres()
	require.NoError(t, err)
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
	lifecycle := mediaasset.NewLifecycle(db, "")
	_, hlsTarget := seedWorkerAllocatedMediaGeneration(t, db, fileID)
	_, spectrogramTarget, err := lifecycle.AllocatePublicAsset(ctx, mediaasset.Allocation{
		SourceFileID: &fileID, Kind: "spectrogram", Extension: "png", MimeType: "image/png",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	source, err := filemedia.CanonicalMediaObjectTargetForFile(model.File{
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
	require.NoError(t, lockTx.Clauses(clause.Locking{Strength: "UPDATE"}).
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
	pg, err := sharedWorkerIntegrationPostgres()
	require.NoError(t, err)
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
	require.NoError(t, lockTx.Clauses(clause.Locking{Strength: "UPDATE"}).
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

func TestHandleWaveformCompleteDuplicateKeepsReferencedAssetReady(t *testing.T) {
	db := newWorkerIntegrationDB(t)
	fileID := uuid.NewString()
	postID := uuid.NewString()
	seedWorkerTranscodeFile(t, db, fileID, "ogg", "audio/ogg")

	source, err := filemedia.CanonicalMediaObjectTargetForFile(model.File{
		ID: fileID, Extension: "ogg", MimeType: "audio/ogg",
	})
	require.NoError(t, err)
	lifecycle := mediaasset.NewLifecycle(db, "")
	_, output, err := lifecycle.AllocatePublicAsset(context.Background(), mediaasset.Allocation{
		SourceFileID: &fileID,
		Kind:         "waveform",
		Extension:    "json",
		MimeType:     "application/json",
		Disposition:  commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)

	recorder := &recordingWorkerPublisher{}
	handlers := newWorkerTranscodeIntegrationHandlers(db)
	handlers.publisher = recorder
	job := &managev1.WaveformGenerateEvent{
		EventId: output.GetAssetId(), EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId: postID, FileId: fileID, Source: source, Output: output,
	}
	require.NoError(t, handlers.upsertWaveformJob(context.Background(), job))

	completion := &managev1.WaveformCompleteEvent{
		EventId: job.EventId, EntityType: job.EntityType, EntityId: postID, FileId: fileID,
		Output: &commonv1.AssetWriteResult{
			AssetId: output.GetAssetId(), FileSize: 2048, Sha256: bytes.Repeat([]byte{0x33}, 32),
		},
	}
	body, err := proto.Marshal(completion)
	require.NoError(t, err)

	require.NoError(t, handlers.HandleWaveformComplete(context.Background(), body))
	require.NoError(t, handlers.HandleWaveformComplete(context.Background(), body))
	requireWorkerDerivativeOutputID(t, db, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM, output.GetAssetId())

	var asset model.PublicAsset
	require.NoError(t, db.Where("id = ?", output.GetAssetId()).Take(&asset).Error)
	require.Equal(t, model.PublicAssetStatusReady, asset.Status)
	require.Len(t, recorder.mediaProcessingLifecycleEvents, 1)
}

func TestHandleWaveformCompleteRetiresOutputWhenSourceDeletionIsPending(t *testing.T) {
	db := newWorkerIntegrationDB(t)
	fileID := uuid.NewString()
	postID := uuid.NewString()
	seedWorkerTranscodeFile(t, db, fileID, "ogg", "audio/ogg")

	source, err := filemedia.CanonicalMediaObjectTargetForFile(model.File{
		ID: fileID, Extension: "ogg", MimeType: "audio/ogg",
	})
	require.NoError(t, err)
	lifecycle := mediaasset.NewLifecycle(db, "")
	_, output, err := lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: &fileID,
		Kind:         "waveform",
		Extension:    "json",
		MimeType:     "application/json",
		Disposition:  commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)

	handlers := newWorkerTranscodeIntegrationHandlers(db)
	handlers.publisher = &recordingWorkerPublisher{}
	job := &managev1.WaveformGenerateEvent{
		EventId: output.GetAssetId(), EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId: postID, FileId: fileID, Source: source, Output: output,
	}
	require.NoError(t, handlers.upsertWaveformJob(t.Context(), job))
	now := time.Now().UTC()
	require.NoError(t, db.Model(&model.File{}).Where("id = ?", fileID).Updates(structured.Fields{
		"delete_requested_at": now,
	}).Error)
	body, err := proto.Marshal(&managev1.WaveformCompleteEvent{
		EventId: job.EventId, EntityType: job.EntityType, EntityId: postID, FileId: fileID,
		Output: &commonv1.AssetWriteResult{
			AssetId: output.GetAssetId(), FileSize: 2048, Sha256: bytes.Repeat([]byte{0x44}, 32),
		},
	})
	require.NoError(t, err)

	require.NoError(t, handlers.HandleWaveformComplete(t.Context(), body))
	requireWorkerTranscodeDerivativeAbsent(t, db, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String())
	var asset model.PublicAsset
	require.NoError(t, db.Where("id = ?", output.GetAssetId()).Take(&asset).Error)
	require.Equal(t, model.PublicAssetStatusDeletePending, asset.Status)
	require.Empty(t, loadWorkerWaveformJobsForFile(t, db, fileID))
}

func TestCompleteAssetDerivativeRetiresReplacedUnboundAsset(t *testing.T) {
	db := newWorkerIntegrationDB(t)
	ctx := context.Background()
	fileID := uuid.NewString()
	seedWorkerTranscodeFile(t, db, fileID, "ogg", "audio/ogg")
	lifecycle := mediaasset.NewLifecycle(db, "")

	_, previousTarget, err := lifecycle.AllocatePublicAsset(ctx, mediaasset.Allocation{
		SourceFileID: &fileID, Kind: "waveform", Extension: "json", MimeType: "application/json",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	previous, err := lifecycle.CompletePublicAsset(ctx, &commonv1.AssetWriteResult{
		AssetId: previousTarget.GetAssetId(), FileSize: 100, Sha256: bytes.Repeat([]byte{0x66}, 32),
	})
	require.NoError(t, err)
	previousID := previous.ID
	require.NoError(t, db.Create(&model.FileDerivative{
		ID: uuid.NewString(), FileID: fileID,
		Type: managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM, AssetID: &previousID,
	}).Error)

	_, currentTarget, err := lifecycle.AllocatePublicAsset(ctx, mediaasset.Allocation{
		SourceFileID: &fileID, Kind: "waveform", Extension: "json", MimeType: "application/json",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return completeAssetDerivative(
			ctx, tx, mediaasset.NewLifecycle(tx, ""), fileID,
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM, "waveform",
			&commonv1.AssetWriteResult{
				AssetId: currentTarget.GetAssetId(), FileSize: 200, Sha256: bytes.Repeat([]byte{0x77}, 32),
			},
		)
	}))

	requireWorkerDerivativeOutputID(t, db, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM, currentTarget.GetAssetId())
	var retired model.PublicAsset
	require.NoError(t, db.Where("id = ?", previousTarget.GetAssetId()).Take(&retired).Error)
	require.Equal(t, model.PublicAssetStatusDeletePending, retired.Status)
	require.NotNil(t, retired.DeleteRequestedAt)
}

func TestCompleteGenerationDerivativeAtomicallySwitchesAndRetainsPreviousHLS(t *testing.T) {
	db := newWorkerIntegrationDB(t)
	ctx := context.Background()
	fileID := uuid.NewString()
	seedWorkerTranscodeFile(t, db, fileID, "mp4", "video/mp4")
	lifecycle := mediaasset.NewLifecycle(db, "")

	_, previousTarget := seedWorkerAllocatedMediaGeneration(t, db, fileID)
	previousResult := &commonv1.MediaGenerationWriteResult{
		GenerationId: previousTarget.GetGenerationId(), ManifestSha256: bytes.Repeat([]byte{0x88}, 32),
		ObjectCount: 2, TotalSize: 4096,
	}
	previous, err := lifecycle.CompleteMediaGeneration(ctx, previousResult)
	require.NoError(t, err)
	previousID := previous.ID
	require.NoError(t, db.Create(&model.FileDerivative{
		ID: uuid.NewString(), FileID: fileID,
		Type: managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS, MediaGenerationID: &previousID,
	}).Error)

	_, currentTarget := seedWorkerAllocatedMediaGeneration(t, db, fileID)
	currentResult := &commonv1.MediaGenerationWriteResult{
		GenerationId: currentTarget.GetGenerationId(), ManifestSha256: bytes.Repeat([]byte{0x99}, 32),
		ObjectCount: 4, TotalSize: 8192,
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		return completeGenerationDerivative(
			ctx, tx, mediaasset.NewLifecycle(tx, ""), uuid.NewString(),
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS, currentResult,
		)
	})
	require.ErrorContains(t, err, "not allocated for source file")
	var afterRollback model.MediaGeneration
	require.NoError(t, db.Where("id = ?", currentTarget.GetGenerationId()).Take(&afterRollback).Error)
	require.Equal(t, model.MediaGenerationStatusAllocated, afterRollback.Status)
	afterRollback = model.MediaGeneration{}
	require.NoError(t, db.Where("id = ?", previousTarget.GetGenerationId()).Take(&afterRollback).Error)
	require.Equal(t, model.MediaGenerationStatusReady, afterRollback.Status)
	requireWorkerDerivativeOutputID(t, db, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS, previousTarget.GetGenerationId())

	complete := func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			return completeGenerationDerivative(
				ctx, tx, mediaasset.NewLifecycle(tx, ""), fileID,
				managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS, currentResult,
			)
		})
	}
	require.NoError(t, complete())
	requireWorkerDerivativeOutputID(t, db, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS, currentTarget.GetGenerationId())

	var retired model.MediaGeneration
	require.NoError(t, db.Where("id = ?", previousTarget.GetGenerationId()).Take(&retired).Error)
	require.Equal(t, model.MediaGenerationStatusRetired, retired.Status)
	require.NotNil(t, retired.RetiredAt)
	require.NotNil(t, retired.DeleteAfter)
	require.False(t, retired.DeleteAfter.Before(retired.RetiredAt.Add(7*time.Hour)))
	deleteAfter := *retired.DeleteAfter

	require.NoError(t, complete())
	require.NoError(t, db.Where("id = ?", previousTarget.GetGenerationId()).Take(&retired).Error)
	require.Equal(t, model.MediaGenerationStatusRetired, retired.Status)
	require.Equal(t, deleteAfter, *retired.DeleteAfter)
	var current model.MediaGeneration
	require.NoError(t, db.Where("id = ?", currentTarget.GetGenerationId()).Take(&current).Error)
	require.Equal(t, model.MediaGenerationStatusReady, current.Status)
}

func newWorkerTranscodeIntegrationHandlers(db *gorm.DB) *Handlers {
	return &Handlers{db: db}
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

func seedWorkerWaveformJobForEntity(
	t *testing.T,
	db *gorm.DB,
	eventID string,
	entityType managev1.TranscodeEntityType,
	entityID string,
	fileID string,
	status string,
) {
	t.Helper()

	require.NoError(t, db.Exec(`
		INSERT INTO waveform_job (
			event_id,
			entity_type,
			entity_id,
			file_id,
			status,
			cancel_requested
		) VALUES (?, ?, ?, ?, ?, false)
	`, eventID, entityType.String(), entityID, fileID, status).Error)
}

func requireWorkerTranscodeDerivativeAbsent(t *testing.T, db *gorm.DB, fileID string, derivativeType string) {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("file_derivative").
		Where("file_id = ? AND type = ?", fileID, derivativeType).
		Count(&count).Error)
	require.Equal(t, int64(0), count)
}

func requireWorkerDerivativeOutputID(
	t *testing.T,
	db *gorm.DB,
	fileID string,
	derivativeType managev1.FileDerivativeType,
	wantID string,
) {
	t.Helper()
	var row struct {
		AssetID           *string `gorm:"column:asset_id"`
		MediaGenerationID *string `gorm:"column:media_generation_id"`
	}
	require.NoError(t, db.Table("file_derivative").
		Select("asset_id", "media_generation_id").
		Where("file_id = ? AND type = ?", fileID, derivativeType.String()).
		Take(&row).Error)
	if derivativeType == managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS {
		require.NotNil(t, row.MediaGenerationID)
		require.Equal(t, wantID, *row.MediaGenerationID)
		require.Nil(t, row.AssetID)
		return
	}
	require.NotNil(t, row.AssetID)
	require.Equal(t, wantID, *row.AssetID)
	require.Nil(t, row.MediaGenerationID)
}

func requireWorkerTranscodeJobCountByFileQueue(t *testing.T, db *gorm.DB, fileID string, queueName string, want int64) {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("transcode_job").
		Where("file_id = ? AND queue_name = ?", fileID, queueName).
		Count(&count).Error)
	require.Equal(t, want, count)
}

func loadWorkerWaveformJobsForFile(t *testing.T, db *gorm.DB, fileID string) []workerWaveformJobRecord {
	t.Helper()

	var rows []workerWaveformJobRecord
	require.NoError(t, db.Table("waveform_job").
		Select("event_id", "entity_type", "entity_id", "file_id", "status", "progress", "last_sequence", "last_stage", "cancel_requested").
		Where("file_id = ?", fileID).
		Find(&rows).Error)
	return rows
}

type workerWaveformJobRecord struct {
	EventID         string  `gorm:"column:event_id"`
	EntityType      string  `gorm:"column:entity_type"`
	EntityID        string  `gorm:"column:entity_id"`
	FileID          string  `gorm:"column:file_id"`
	Status          string  `gorm:"column:status"`
	Progress        int     `gorm:"column:progress"`
	LastSequence    *int64  `gorm:"column:last_sequence"`
	LastStage       *string `gorm:"column:last_stage"`
	CancelRequested bool    `gorm:"column:cancel_requested"`
}

type controlledLifecyclePublisher struct {
	*recordingWorkerPublisher
	lifecycleErr error
}

func (p *controlledLifecyclePublisher) PublishMediaProcessingLifecycle(
	ctx context.Context,
	event *managev1.MediaProcessingLifecycleEvent,
) error {
	if p.lifecycleErr != nil {
		return p.lifecycleErr
	}
	return p.recordingWorkerPublisher.PublishMediaProcessingLifecycle(ctx, event)
}

type recordingTranscodeJobTracker struct {
	completeEvents []*managev1.TranscodeCompleteEvent
}

func (*recordingTranscodeJobTracker) HandleTranscodeProgress(context.Context, *managev1.TranscodeProgressEvent) error {
	return nil
}

func (t *recordingTranscodeJobTracker) HandleTranscodeComplete(_ context.Context, event *managev1.TranscodeCompleteEvent) error {
	t.completeEvents = append(t.completeEvents, event)
	return nil
}

func (*recordingTranscodeJobTracker) MarkCancelled(context.Context, string, managev1.TranscodeCancelReason) error {
	return nil
}
