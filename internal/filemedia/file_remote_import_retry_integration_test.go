//go:build integration

package filemedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"

	"github.com/echovisionlab/geul-api/internal/testutil/runtimetest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func TestRemoteImportCommitAcknowledgementLossRestoresExactCommittedResultDirectIntegration(t *testing.T) {
	t.Parallel()
	stack := runtimetest.SetupDirectMediaRuntimeStack(t)
	releaseID := runtimetest.CreateReleaseFixture(t, stack.DB)
	trackID := runtimetest.CreateManagedReleaseTrack(t, stack.DB, releaseID, "Remote commit acknowledgement loss")

	opts := remoteFileImportOptions{
		uploadType: UploadTrackAudio,

		entityID:            trackID,
		entityType:          managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK.String(),
		transcodeEntityType: EntityTrack,

		correlationID:      uuid.NewString(),
		emitLifecycle:      true,
		triggerTranscoding: true,
		checkPermission:    false,
	}
	projection, err := normalizeFileIngestProjectionIdentity(
		opts.uploadType,
		opts.transcodeEntityType,
		opts.slotID,
		opts.expectedCurrentFileID,
	)
	require.NoError(t, err)
	identity, err := resolveRemoteImportOperationIdentity(opts, trackID, "", projection)
	require.NoError(t, err)

	body := validRemoteImportWAVBytes()
	digest := sha256.Sum256(body)
	mimeType := "audio/wav"
	extension := mediaExtension(&mimeType)
	fileName := canonicalRemoteImportFilename("ack-loss.wav", identity.fileID, mimeType)
	objectKey, err := mediaauth.MediaObjectKey(identity.fileID, extension)
	require.NoError(t, err)
	s3Client := runtimeS3Client(t, stack)
	_, err = s3Client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:      aws.String(stack.S3MediaBucket),
		Key:         aws.String(objectKey),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(mimeType),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = s3Client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
			Bucket: aws.String(stack.S3MediaBucket),
			Key:    aws.String(objectKey),
		})
	})

	service := newTestService(Deps{
		S3:             s3Client,
		AsyncPublisher: &flakyAttachedConfirmPublisher{},
		S3Bucket:       stack.S3MediaBucket,
		CDNDomain:      stack.CDNURL,
		MediaDomain:    stack.MediaURL,
		MediaSecret:    stack.MediaSigningSecret,

		SpiceDB: stack.SpiceDBClient,
	}, stack.DB, nativeFilePorts{Transcoder: &recordingFileTranscoderPublisher{}})
	service.testVerifiedIngestCommitError = errors.New("injected lost commit acknowledgement")

	err = service.createOrRestoreVerifiedRemoteImportRecord(
		memberRequestContext(),
		fileIngestRecord{
			id:              identity.fileID,
			fileName:        fileName,
			mimeType:        mimeType,
			fileSize:        int64(len(body)),
			extension:       extension,
			sha256:          digest[:],
			ingestAttemptID: identity.attemptID,
		},
		trackID,
		"",
		identity,
		projection,
		opts,
		objectKey,
	)
	require.NoError(t, err)

	var fileCount, bindingCount int64
	require.NoError(t, stack.DB.Model(&model.File{}).Where("id = ?", identity.fileID).Count(&fileCount).Error)
	require.NoError(t, stack.DB.Model(&model.FileIngestBinding{}).Where("file_id = ?", identity.fileID).Count(&bindingCount).Error)
	require.EqualValues(t, 1, fileCount)
	require.EqualValues(t, 1, bindingCount)

	stored, err := s3Client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(stack.S3MediaBucket),
		Key:    aws.String(objectKey),
	})
	require.NoError(t, err)
	storedBody, err := io.ReadAll(stored.Body)
	require.NoError(t, err)
	require.NoError(t, stored.Body.Close())
	require.Equal(t, body, storedBody, "commit acknowledgement loss must not delete the committed object")
}

func TestRemoteImportPublicAssetPromotionRetryReusesVerifiedSourceWithoutRedownloadDirectIntegration(t *testing.T) {
	t.Parallel()
	stack := runtimetest.SetupDirectMediaRuntimeStack(t)
	correlationID := uuid.NewString()
	entityID := uuid.NewString()
	opts := remoteFileImportOptions{
		uploadType: UploadClientLogo,

		entityID:        entityID,
		sourceURL:       "https://does-not-resolve.invalid/logo.webp",
		correlationID:   correlationID,
		emitLifecycle:   true,
		checkPermission: false,
	}
	identity, err := resolveRemoteImportOperationIdentity(opts, entityID, "", fileIngestProjectionIdentity{})
	require.NoError(t, err)
	require.True(t, identity.durable)
	body := validRemoteImportWebPBytes()
	digest := sha256.Sum256(body)
	file := model.File{
		ID: identity.fileID, FileName: storedFileBasename("logo.webp", identity.fileID, "webp"), MimeType: "image/webp",
		FileSize: int64(len(body)), Extension: "webp", SHA256: digest[:],
		IngestAttemptID: &identity.attemptID, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, stack.DB.Create(&file).Error)
	require.NoError(t, stack.DB.Create(&model.FileIngestBinding{
		FileID: file.ID, UploadType: opts.uploadType.String(), EntityID: entityID,
	}).Error)
	store := newSourceAssetPromotionS3Fixture(t, file, body)
	store.setFailTransfer(true)
	service := newTestService(Deps{
		S3:             store.client,
		AsyncPublisher: &hardCutAsyncPublisher{},
		S3Bucket:       "media-bucket",
		CDNDomain:      stack.CDNURL,
		MediaDomain:    stack.MediaURL,
		MediaSecret:    stack.MediaSigningSecret,

		SpiceDB: stack.SpiceDBClient,
	}, stack.DB, nativeFilePorts{Transcoder: &recordingFileTranscoderPublisher{}})

	_, err = service.importRemoteFile(memberRequestContext(), opts)
	require.ErrorContains(t, err, "stream source to public asset")
	var preserved model.File
	require.NoError(t, stack.DB.Where("id = ?", file.ID).Take(&preserved).Error)
	require.Nil(t, preserved.DeleteRequestedAt)
	require.Empty(t, store.deletedKeys())
	var failed model.PublicAsset
	require.NoError(t, stack.DB.Where("source_file_id = ?", file.ID).Take(&failed).Error)
	require.Equal(t, model.PublicAssetStatusFailed, failed.Status)

	store.setFailTransfer(false)
	result, err := service.importRemoteFile(memberRequestContext(), opts)
	require.NoError(t, err)
	require.Equal(t, file.ID, result.fileID)
	require.NotNil(t, result.asset)
	require.Equal(t, failed.ID, result.asset.AssetID)
	require.Empty(t, store.deletedKeys())
	var ready model.PublicAsset
	require.NoError(t, stack.DB.Where("id = ?", failed.ID).Take(&ready).Error)
	require.Equal(t, model.PublicAssetStatusReady, ready.Status)
}

func TestRemoteUserAvatarImportPromotionRetryUsesStableCallerCorrelationDirectIntegration(t *testing.T) {
	t.Parallel()
	stack := runtimetest.SetupDirectMediaRuntimeStack(t)
	userID := uuid.NewString()
	correlationID := uuid.NewString()
	opts := remoteFileImportOptions{
		uploadType: UploadUserAvatar,

		entityID:      userID,
		correlationID: correlationID,
	}
	identity, err := resolveRemoteImportOperationIdentity(opts, userID, "", fileIngestProjectionIdentity{})
	require.NoError(t, err)
	require.True(t, identity.durable)

	body := validRemoteImportWebPBytes()
	digest := sha256.Sum256(body)
	file := model.File{
		ID: identity.fileID, FileName: storedFileBasename(canonicalRemoteImportFilename("avatar.webp", identity.fileID, "image/webp"), identity.fileID, "webp"),
		MimeType: "image/webp", FileSize: int64(len(body)), Extension: "webp", SHA256: digest[:],
		IngestAttemptID: &identity.attemptID, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, stack.DB.Create(&file).Error)
	require.NoError(t, stack.DB.Create(&model.FileIngestBinding{
		FileID: file.ID, UploadType: opts.uploadType.String(), EntityID: userID,
	}).Error)
	store := newSourceAssetPromotionS3Fixture(t, file, body)
	store.setFailTransfer(true)
	service := newTestService(Deps{
		S3:             store.client,
		AsyncPublisher: &hardCutAsyncPublisher{},
		S3Bucket:       "media-bucket",
		CDNDomain:      stack.CDNURL,
		MediaDomain:    stack.MediaURL,
		MediaSecret:    stack.MediaSigningSecret,

		SpiceDB: stack.SpiceDBClient,
	}, stack.DB, nativeFilePorts{Transcoder: &recordingFileTranscoderPublisher{}})
	// The provider URL is deliberately unreachable. The successful second call
	// proves that the same caller correlation restores the verified source and
	// resumes promotion without downloading the provider image again.
	providerURL := "https://does-not-resolve.invalid/social-avatar.webp"
	opts.sourceURL = providerURL

	_, err = service.importRemoteFile(memberRequestContext(), opts)
	require.ErrorContains(t, err, "stream source to public asset")
	var preserved model.File
	require.NoError(t, stack.DB.Where("id = ?", file.ID).Take(&preserved).Error)
	require.Nil(t, preserved.DeleteRequestedAt)
	require.Empty(t, store.deletedKeys())
	var failed model.PublicAsset
	require.NoError(t, stack.DB.Where("source_file_id = ?", file.ID).Take(&failed).Error)
	require.Equal(t, model.PublicAssetStatusFailed, failed.Status)

	store.setFailTransfer(false)
	imported, err := service.importRemoteFile(memberRequestContext(), opts)
	require.NoError(t, err)
	require.Equal(t, file.ID, imported.fileID)
	require.Empty(t, store.deletedKeys())

	asset, err := service.PromoteUserAvatarAsset(memberRequestContext(), imported.fileID)
	require.NoError(t, err)
	require.Equal(t, failed.ID, asset.AssetID)
	var ready model.PublicAsset
	require.NoError(t, stack.DB.Where("id = ?", failed.ID).Take(&ready).Error)
	require.Equal(t, model.PublicAssetStatusReady, ready.Status)
	require.Empty(t, store.deletedKeys())
}

func TestRemoteImportRetryReconfirmsCommittedTrackWinnerWithoutRedownloadDirectIntegration(t *testing.T) {
	t.Parallel()
	stack := runtimetest.SetupDirectMediaRuntimeStack(t)
	releaseID := runtimetest.CreateReleaseFixture(t, stack.DB)
	trackID := runtimetest.CreateManagedReleaseTrack(t, stack.DB, releaseID, "Remote retry track")

	correlationID := uuid.NewString()
	opts := remoteFileImportOptions{
		uploadType: UploadTrackAudio,

		entityID:            trackID,
		entityType:          managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK.String(),
		transcodeEntityType: EntityTrack,

		sourceURL:          "https://does-not-resolve.invalid/retry-source.wav",
		correlationID:      correlationID,
		emitLifecycle:      true,
		triggerTranscoding: true,
		checkPermission:    false,
	}
	projection, err := normalizeFileIngestProjectionIdentity(
		opts.uploadType,
		opts.transcodeEntityType,
		opts.slotID,
		opts.expectedCurrentFileID,
	)
	require.NoError(t, err)
	identity, err := resolveRemoteImportOperationIdentity(opts, trackID, "", projection)
	require.NoError(t, err)

	body := validRemoteImportWAVBytes()
	digest := sha256.Sum256(body)
	mimeType := "audio/wav"
	extension := mediaExtension(&mimeType)
	fileName := canonicalRemoteImportFilename("original-winner.wav", identity.fileID, mimeType)
	objectKey, err := mediaauth.MediaObjectKey(identity.fileID, extension)
	require.NoError(t, err)
	s3Client := runtimeS3Client(t, stack)
	_, err = s3Client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:      aws.String(stack.S3MediaBucket),
		Key:         aws.String(objectKey),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(mimeType),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = s3Client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
			Bucket: aws.String(stack.S3MediaBucket),
			Key:    aws.String(objectKey),
		})
	})

	require.NoError(t, stack.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("file").Create(structured.Fields{
			"id":                identity.fileID,
			"file_name":         storedFileBasename(fileName, identity.fileID, extension),
			"mime_type":         mimeType,
			"file_size":         int64(len(body)),
			"extension":         extension,
			"sha256":            digest[:],
			"ingest_attempt_id": identity.attemptID,
		}).Error; err != nil {
			return err
		}
		entityType := opts.transcodeEntityType.String()
		return tx.Create(&model.FileIngestBinding{
			FileID:     identity.fileID,
			UploadType: opts.uploadType.String(),
			EntityType: &entityType,
			EntityID:   trackID,
		}).Error
	}))

	asyncPublisher := &flakyAttachedConfirmPublisher{failuresRemaining: 1}
	service := newTestService(Deps{
		S3:             s3Client,
		AsyncPublisher: asyncPublisher,
		S3Bucket:       stack.S3MediaBucket,
		CDNDomain:      stack.CDNURL,
		MediaDomain:    stack.MediaURL,
		MediaSecret:    stack.MediaSigningSecret,

		SpiceDB: stack.SpiceDBClient,
	}, stack.DB, nativeFilePorts{Transcoder: &recordingFileTranscoderPublisher{}})

	_, err = service.importRemoteFile(memberRequestContext(), opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "reconfirm durable file attachment")
	require.Empty(t, decodeHardCutRoutedMessages(t, asyncPublisher.messages, eventpkg.SignalFileIngest, "", func() *managev1.FileIngestFailedEvent {
		return &managev1.FileIngestFailedEvent{}
	}))

	opts.sourceURL = "https://another-does-not-resolve.invalid/changed-retry-source.wav"
	result, err := service.importRemoteFile(memberRequestContext(), opts)
	require.NoError(t, err)
	require.Equal(t, identity.fileID, result.fileID)
	require.Equal(t, identity.attemptID, result.attemptID)
	require.Equal(t, fileName, result.fileName)

	attachedEvents := decodeHardCutRoutedMessages(t, asyncPublisher.messages, eventpkg.SignalFileIngest, "", func() *managev1.FileIngestAttachedEvent {
		return &managev1.FileIngestAttachedEvent{}
	})
	require.Len(t, attachedEvents, 2)
	for _, event := range attachedEvents {
		require.Equal(t, correlationID, event.GetCorrelationId())
		require.Equal(t, identity.fileID, event.GetIdentity().GetFileId())
		require.Equal(t, identity.attemptID, event.GetIdentity().GetAttemptId())
		require.Equal(t, trackID, event.GetIdentity().GetEntityId())
	}
	var fileCount, bindingCount int64
	require.NoError(t, stack.DB.Model(&model.File{}).Where("id = ?", identity.fileID).Count(&fileCount).Error)
	require.NoError(t, stack.DB.Model(&model.FileIngestBinding{}).Where("file_id = ?", identity.fileID).Count(&bindingCount).Error)
	require.EqualValues(t, 1, fileCount)
	require.EqualValues(t, 1, bindingCount)

	var track model.Track
	require.NoError(t, stack.DB.Where("id = ?", trackID).Take(&track).Error)
	require.Nil(t, track.AudioOriginalFileID, "remote ingest must leave Track projection CAS to the internal finalizer")

	stored, err := s3Client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(stack.S3MediaBucket),
		Key:    aws.String(objectKey),
	})
	require.NoError(t, err)
	storedBody, err := io.ReadAll(stored.Body)
	require.NoError(t, err)
	require.NoError(t, stored.Body.Close())
	require.Equal(t, body, storedBody, "retry must not overwrite or delete the committed winner object")
}

func validRemoteImportWAVBytes() []byte {
	return []byte{
		'R', 'I', 'F', 'F', 0x28, 0x00, 0x00, 0x00,
		'W', 'A', 'V', 'E', 'f', 'm', 't', ' ',
		0x10, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00,
		0x44, 0xac, 0x00, 0x00, 0x88, 0x58, 0x01, 0x00,
		0x02, 0x00, 0x10, 0x00, 'd', 'a', 't', 'a',
		0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
}

func validRemoteImportWebPBytes() []byte {
	return []byte{
		'R', 'I', 'F', 'F', 0x0c, 0x00, 0x00, 0x00,
		'W', 'E', 'B', 'P', 'V', 'P', '8', ' ',
		0x00, 0x00, 0x00, 0x00,
	}
}
