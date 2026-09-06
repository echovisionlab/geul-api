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

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

func TestRemoteImportCommitAcknowledgementLossRestoresExactCommittedResultDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	releaseID := testutil.CreateReleaseFixture(t, stack.DB)
	trackID := testutil.CreateManagedReleaseTrack(t, stack.DB, releaseID, "Remote commit acknowledgement loss")

	opts := remoteFileImportOptions{
		uploadType:          managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
		entityID:            trackID,
		entityType:          managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK.String(),
		transcodeEntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
		correlationID:       uuid.NewString(),
		emitLifecycle:       true,
		triggerTranscoding:  true,
		checkPermission:     false,
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

	service := NewFileService(
		stack.DB,
		s3Client,
		&flakyAttachedConfirmPublisher{},
		stack.S3MediaBucket,
		stack.CDNURL,
		stack.MediaURL,
		stack.MediaSigningSecret,
		&recordingFileTranscoderPublisher{},
		stack.SpiceDBClient,
	)
	service.testVerifiedIngestCommitError = errors.New("injected lost commit acknowledgement")

	err = service.createOrRestoreVerifiedRemoteImportRecord(
		context.Background(),
		map[string]interface{}{
			"id":                identity.fileID,
			"file_name":         fileName,
			"mime_type":         mimeType,
			"file_size":         int64(len(body)),
			"extension":         extension,
			"sha256":            digest[:],
			"ingest_attempt_id": identity.attemptID,
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
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	correlationID := uuid.NewString()
	entityID := uuid.NewString()
	opts := remoteFileImportOptions{
		uploadType:      managev1.UploadType_UPLOAD_TYPE_CLIENT_LOGO,
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
	service := NewFileService(
		stack.DB,
		store.client,
		&hardCutAsyncPublisher{},
		"media-bucket",
		stack.CDNURL,
		stack.MediaURL,
		stack.MediaSigningSecret,
		&recordingFileTranscoderPublisher{},
		stack.SpiceDBClient,
	)

	_, err = service.importRemoteFile(context.Background(), opts)
	require.ErrorContains(t, err, "stream source to public asset")
	var preserved model.File
	require.NoError(t, stack.DB.Where("id = ?", file.ID).Take(&preserved).Error)
	require.Nil(t, preserved.DeleteRequestedAt)
	require.Empty(t, store.deletedKeys())
	var failed model.PublicAsset
	require.NoError(t, stack.DB.Where("source_file_id = ?", file.ID).Take(&failed).Error)
	require.Equal(t, model.PublicAssetStatusFailed, failed.Status)

	store.setFailTransfer(false)
	result, err := service.importRemoteFile(context.Background(), opts)
	require.NoError(t, err)
	require.Equal(t, file.ID, result.fileID)
	require.NotNil(t, result.asset)
	require.Equal(t, failed.ID, result.asset.GetAssetId())
	require.Empty(t, store.deletedKeys())
	var ready model.PublicAsset
	require.NoError(t, stack.DB.Where("id = ?", failed.ID).Take(&ready).Error)
	require.Equal(t, model.PublicAssetStatusReady, ready.Status)
}

func TestRemoteUserAvatarImportPromotionRetryUsesStableCallerCorrelationDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	userID := uuid.NewString()
	correlationID := uuid.NewString()
	opts := remoteFileImportOptions{
		uploadType:    managev1.UploadType_UPLOAD_TYPE_USER_AVATAR,
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
	service := NewFileService(
		stack.DB,
		store.client,
		&hardCutAsyncPublisher{},
		"media-bucket",
		stack.CDNURL,
		stack.MediaURL,
		stack.MediaSigningSecret,
		&recordingFileTranscoderPublisher{},
		stack.SpiceDBClient,
	)
	// The provider URL is deliberately unreachable. The successful second call
	// proves that the same caller correlation restores the verified source and
	// resumes promotion without downloading the provider image again.
	providerURL := "https://does-not-resolve.invalid/social-avatar.webp"
	opts.sourceURL = providerURL

	_, err = service.importRemoteFile(context.Background(), opts)
	require.ErrorContains(t, err, "stream source to public asset")
	var preserved model.File
	require.NoError(t, stack.DB.Where("id = ?", file.ID).Take(&preserved).Error)
	require.Nil(t, preserved.DeleteRequestedAt)
	require.Empty(t, store.deletedKeys())
	var failed model.PublicAsset
	require.NoError(t, stack.DB.Where("source_file_id = ?", file.ID).Take(&failed).Error)
	require.Equal(t, model.PublicAssetStatusFailed, failed.Status)

	store.setFailTransfer(false)
	imported, err := service.importRemoteFile(context.Background(), opts)
	require.NoError(t, err)
	require.Equal(t, file.ID, imported.fileID)
	require.Empty(t, store.deletedKeys())

	asset, err := service.PromoteUserAvatarAsset(context.Background(), imported.fileID)
	require.NoError(t, err)
	require.Equal(t, failed.ID, asset.GetAssetId())
	var ready model.PublicAsset
	require.NoError(t, stack.DB.Where("id = ?", failed.ID).Take(&ready).Error)
	require.Equal(t, model.PublicAssetStatusReady, ready.Status)
	require.Empty(t, store.deletedKeys())
}

func TestRemoteImportRetryReconfirmsCommittedTrackWinnerWithoutRedownloadDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	releaseID := testutil.CreateReleaseFixture(t, stack.DB)
	trackID := testutil.CreateManagedReleaseTrack(t, stack.DB, releaseID, "Remote retry track")

	correlationID := uuid.NewString()
	opts := remoteFileImportOptions{
		uploadType:          managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
		entityID:            trackID,
		entityType:          managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK.String(),
		transcodeEntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
		sourceURL:           "https://does-not-resolve.invalid/retry-source.wav",
		correlationID:       correlationID,
		emitLifecycle:       true,
		triggerTranscoding:  true,
		checkPermission:     false,
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
	service := NewFileService(
		stack.DB,
		s3Client,
		asyncPublisher,
		stack.S3MediaBucket,
		stack.CDNURL,
		stack.MediaURL,
		stack.MediaSigningSecret,
		&recordingFileTranscoderPublisher{},
		stack.SpiceDBClient,
	)

	_, err = service.importRemoteFile(context.Background(), opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "reconfirm durable file attachment")
	require.Empty(t, decodeHardCutRoutedMessages(t, asyncPublisher.messages, eventpkg.SignalFileIngest, "", func() *managev1.FileIngestFailedEvent {
		return &managev1.FileIngestFailedEvent{}
	}))

	opts.sourceURL = "https://another-does-not-resolve.invalid/changed-retry-source.wav"
	result, err := service.importRemoteFile(context.Background(), opts)
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

func TestRemoteImportAdvisoryLockSerializesSameOperationDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	fileID := uuid.NewString()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	firstErr := make(chan error, 1)
	secondErr := make(chan error, 1)

	go func() {
		firstErr <- withRemoteImportAdvisoryLock(context.Background(), stack.DB, fileID, func(*gorm.DB) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	select {
	case <-firstEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first advisory-lock operation did not enter")
	}

	go func() {
		secondErr <- withRemoteImportAdvisoryLock(context.Background(), stack.DB, fileID, func(*gorm.DB) error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("same durable operation entered concurrently")
	case <-time.After(250 * time.Millisecond):
	}

	close(releaseFirst)
	select {
	case err := <-firstErr:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("first advisory-lock operation did not finish")
	}
	select {
	case <-secondEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("second advisory-lock operation did not enter after release")
	}
	select {
	case err := <-secondErr:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("second advisory-lock operation did not finish")
	}
}

func TestRemoteImportAdvisoryLockUnlocksAfterRequestCancellationDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	fileID := uuid.NewString()
	ctx, cancel := context.WithCancel(context.Background())

	err := withRemoteImportAdvisoryLock(ctx, stack.DB, fileID, func(*gorm.DB) error {
		cancel()
		return ctx.Err()
	})
	require.ErrorIs(t, err, context.Canceled)

	reentered := false
	require.NoError(t, withRemoteImportAdvisoryLock(
		context.Background(),
		stack.DB,
		fileID,
		func(*gorm.DB) error {
			reentered = true
			return nil
		},
	))
	require.True(t, reentered, "cancelled request must release its session advisory lock")
}

func TestMultipartCompletionAdvisoryLockSerializesSameSessionDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	uploadID := uuid.NewString()
	fileID := uuid.NewString()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	firstErr := make(chan error, 1)
	secondErr := make(chan error, 1)

	go func() {
		firstErr <- withMultipartCompletionAdvisoryLock(
			context.Background(),
			stack.DB,
			uploadID,
			fileID,
			func(*gorm.DB) error {
				close(firstEntered)
				<-releaseFirst
				return nil
			},
		)
	}()
	select {
	case <-firstEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first multipart completion lock did not enter")
	}

	go func() {
		secondErr <- withMultipartCompletionAdvisoryLock(
			context.Background(),
			stack.DB,
			uploadID,
			fileID,
			func(*gorm.DB) error {
				close(secondEntered)
				return nil
			},
		)
	}()
	select {
	case <-secondEntered:
		t.Fatal("same multipart completion entered concurrently")
	case <-time.After(250 * time.Millisecond):
	}

	close(releaseFirst)
	select {
	case err := <-firstErr:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("first multipart completion lock did not finish")
	}
	select {
	case <-secondEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("second multipart completion lock did not enter after release")
	}
	select {
	case err := <-secondErr:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("second multipart completion lock did not finish")
	}
}
