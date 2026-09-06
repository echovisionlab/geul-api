//go:build integration

package filemedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type multipartCompletionObservingTransport struct {
	base                        http.RoundTripper
	mu                          sync.Mutex
	completeCalls               int
	returnNoSuchAfterCompletion bool
	returnedNoSuch              bool
}

func (t *multipartCompletionObservingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	if req.Method != http.MethodPost || req.URL.Query().Get("uploadId") == "" {
		return response, err
	}

	t.mu.Lock()
	t.completeCalls++
	inject := t.returnNoSuchAfterCompletion && !t.returnedNoSuch && err == nil && response.StatusCode >= 200 && response.StatusCode < 300
	if inject {
		t.returnedNoSuch = true
	}
	t.mu.Unlock()
	if !inject {
		return response, err
	}

	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	body := `<Error><Code>NoSuchUpload</Code><Message>injected ambiguous multipart completion</Message></Error>`
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Status:     "404 Not Found",
		Header:     http.Header{"Content-Type": []string{"application/xml"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (t *multipartCompletionObservingTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.completeCalls
}

func observingRuntimeS3Client(
	t *testing.T,
	stack *testutil.RuntimeStack,
	observer *multipartCompletionObservingTransport,
) *s3.Client {
	t.Helper()
	base := runtimeS3Client(t, stack)
	options := base.Options()
	observer.base = http.DefaultTransport.(*http.Transport).Clone()
	options.HTTPClient = &http.Client{Transport: observer}
	return s3.New(options)
}

type directEditorMultipartFixture struct {
	service  *FileService
	ctx      context.Context
	fileID   string
	uploadID string
}

func prepareDirectEditorImageMultipart(
	t *testing.T,
	stack *testutil.RuntimeStack,
	s3Client *s3.Client,
	asyncPublisher AsyncPublisher,
) directEditorMultipartFixture {
	t.Helper()
	manager := stack.CreateUser(t, policyv1.Role.Author().ID())
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
	ctx := auth.WithUser(context.Background(), manager.AuthUserInfo())
	body, err := os.ReadFile(testutil.RepositoryTestImageJPEG(t))
	require.NoError(t, err)
	response, err := service.InitiateMultipartUpload(ctx, connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
		UploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		FileName:   "retry-image.jpg",
		FileSize:   int64(len(body)),
		MimeType:   "image/jpeg",
	}))
	require.NoError(t, err)
	require.Nil(t, response.Msg.SlotId)

	var storedSession model.UploadSession
	require.NoError(t, stack.DB.
		Where("upload_id = ?", response.Msg.GetUploadId()).
		Take(&storedSession).Error)
	require.Nil(t, storedSession.SlotID)
	require.Empty(t, storedSession.EntityID)
	require.Nil(t, storedSession.EntityType)
	require.Nil(t, storedSession.ExpectedFileID)
	requireMultipartPartRelayRejected(
		t,
		service,
		manager,
		response.Msg.GetFileId(),
		response.Msg.GetUploadId(),
		1,
		body,
	)
	handleMultipartPartDirect(
		t,
		service,
		manager,
		response.Msg.GetFileId(),
		response.Msg.GetUploadId(),
		1,
		body,
	)

	return directEditorMultipartFixture{
		service:  service,
		ctx:      ctx,
		fileID:   response.Msg.GetFileId(),
		uploadID: response.Msg.GetUploadId(),
	}
}

func TestUploadSessionEditorFileTargetShapeDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	now := time.Now().UTC()

	valid := model.UploadSession{
		UploadID:       "editor-file-valid-" + uuid.NewString(),
		FileID:         uuid.NewString(),
		UploadType:     managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String(),
		FileName:       "independent.jpeg",
		FileSize:       128,
		RequestedMime:  "image/jpeg",
		TotalParts:     1,
		ChunkSize:      int32(chunkSize),
		Status:         model.UploadSessionStatusInitiated,
		LastActivityAt: now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	require.NoError(t, stack.DB.
		Omit("EntityID", "EntityType", "SlotID", "ExpectedFileID").
		Create(&valid).Error)
	t.Cleanup(func() {
		_ = stack.DB.Delete(&model.UploadSession{}, "upload_id = ?", valid.UploadID).Error
	})

	targeted := valid
	targeted.UploadID = "editor-file-targeted-" + uuid.NewString()
	targeted.FileID = uuid.NewString()
	targeted.EntityID = uuid.NewString()
	err := stack.DB.Create(&targeted).Error
	require.Error(t, err)
	require.ErrorContains(t, err, "chk_upload_session_semantic_target_owner")
}

func TestFinalizingMultipartSessionRejectsAbortAndReplacementDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	asyncPublisher := &hardCutAsyncPublisher{}
	fixture := prepareDirectEditorImageMultipart(
		t,
		stack,
		runtimeS3Client(t, stack),
		asyncPublisher,
	)
	require.NoError(t, stack.DB.Model(&model.UploadSession{}).
		Where("upload_id = ?", fixture.uploadID).
		Update("status", model.UploadSessionStatusFinalizing).Error)

	abortResponse, err := fixture.service.AbortMultipartUpload(fixture.ctx, connect.NewRequest(&managev1.AbortMultipartUploadRequest{
		UploadId: fixture.uploadID,
		FileId:   fixture.fileID,
	}))
	require.Error(t, err)
	require.Nil(t, abortResponse)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	replacement, err := fixture.service.InitiateMultipartUpload(fixture.ctx, connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
		UploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		FileName:   "replacement.jpg",
		FileSize:   1,
		MimeType:   "image/jpeg",
	}))
	require.NoError(t, err)
	require.NotEqual(t, fixture.uploadID, replacement.Msg.GetUploadId())
	require.NotEqual(t, fixture.fileID, replacement.Msg.GetFileId())
	requireUploadSessionStatus(
		t,
		stack.DB,
		fixture.uploadID,
		managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_FINALIZING,
	)

	completed, err := fixture.service.CompleteMultipartUpload(fixture.ctx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		FileId:        fixture.fileID,
		UploadId:      fixture.uploadID,
		CorrelationId: hardCutPtrString(uuid.NewString()),
	}))
	require.NoError(t, err)
	require.Equal(t, fixture.fileID, completed.Msg.GetFileId())
}

func TestDeleteTrackGuardsFinalizingUploadSessionDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Admin().ID())
	releaseID := testutil.CreateReleaseFixture(t, stack.DB)
	trackID := testutil.CreateManagedReleaseTrack(t, stack.DB, releaseID, "Guarded Track Delete")
	requireTrackPolicyFixture(t, stack.SpiceDBClient, trackID)
	asyncPublisher := &hardCutAsyncPublisher{}
	fileService := NewFileService(
		stack.DB,
		runtimeS3Client(t, stack),
		asyncPublisher,
		stack.S3MediaBucket,
		stack.CDNURL,
		stack.MediaURL,
		stack.MediaSigningSecret,
		&recordingFileTranscoderPublisher{},
		stack.SpiceDBClient,
		directTrackAttachmentOption(stack.DB),
	)
	ctx := auth.WithUser(context.Background(), manager.AuthUserInfo())
	entityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK
	initiated, err := fileService.InitiateMultipartUpload(ctx, connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
		UploadType: managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
		EntityId:   trackID,
		EntityType: &entityType,
		FileName:   "guarded-track.mp3",
		FileSize:   1024,
		MimeType:   "audio/mpeg",
	}))
	require.NoError(t, err)
	require.NoError(t, stack.DB.Model(&model.UploadSession{}).
		Where("upload_id = ?", initiated.Msg.GetUploadId()).
		Update("status", model.UploadSessionStatusFinalizing).Error)

	trackService := releasepkg.NewTrackService(
		stack.DB,
		releaseadapter.NewTrackTranscodes(&recordingFileTranscoderPublisher{}),
		releaseadapter.NewWaveformJobs(stack.DB, &recordingFileTranscoderPublisher{}),
		stack.SpiceDBClient,
		releaseadapter.NewTrackFiles(fileService),
		releaseadapter.NewMemberSummaries(stack.DB, stack.CDNURL),
		releaseadapter.NewArtistSummaries(stack.DB),
	)
	response, err := trackService.DeleteTrack(ctx, connect.NewRequest(&managev1.DeleteTrackRequest{Id: trackID}))
	require.Error(t, err)
	require.Nil(t, response)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	requireUploadSessionStatus(
		t,
		stack.DB,
		initiated.Msg.GetUploadId(),
		managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_FINALIZING,
	)
	var trackCount int64
	require.NoError(t, stack.DB.Model(&model.Track{}).Where("id = ?", trackID).Count(&trackCount).Error)
	require.EqualValues(t, 1, trackCount)

	require.NoError(t, stack.DB.Model(&model.UploadSession{}).
		Where("upload_id = ?", initiated.Msg.GetUploadId()).
		Update("status", model.UploadSessionStatusUploading).Error)
	response, err = trackService.DeleteTrack(ctx, connect.NewRequest(&managev1.DeleteTrackRequest{Id: trackID}))
	require.NoError(t, err)
	require.True(t, response.Msg.GetSuccess())
	require.NoError(t, stack.DB.Model(&model.Track{}).Where("id = ?", trackID).Count(&trackCount).Error)
	require.Zero(t, trackCount)
	var sessionCount int64
	require.NoError(t, stack.DB.Model(&model.UploadSession{}).
		Where("upload_id = ?", initiated.Msg.GetUploadId()).
		Count(&sessionCount).Error)
	require.Zero(t, sessionCount)
}

func TestConcurrentTrackInitiateAndDeleteLeavesNoMultipartOrSessionDirectIntegration(t *testing.T) {
	for _, testCase := range []struct {
		name              string
		pauseBeforeInsert bool
	}{
		{name: "delete locks before session visibility", pauseBeforeInsert: true},
		{name: "session is visible before delete", pauseBeforeInsert: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
			manager := stack.CreateUser(t, policyv1.Role.Admin().ID())
			releaseID := testutil.CreateReleaseFixture(t, stack.DB)
			trackID := testutil.CreateManagedReleaseTrack(t, stack.DB, releaseID, "Concurrent Track Delete")
			requireTrackPolicyFixture(t, stack.SpiceDBClient, trackID)
			asyncPublisher := &hardCutAsyncPublisher{}
			s3Client := runtimeS3Client(t, stack)
			fileService := NewFileService(
				stack.DB,
				s3Client,
				asyncPublisher,
				stack.S3MediaBucket,
				stack.CDNURL,
				stack.MediaURL,
				stack.MediaSigningSecret,
				&recordingFileTranscoderPublisher{},
				stack.SpiceDBClient,
				directTrackAttachmentOption(stack.DB),
			)
			trackService := releasepkg.NewTrackService(
				stack.DB,
				releaseadapter.NewTrackTranscodes(&recordingFileTranscoderPublisher{}),
				releaseadapter.NewWaveformJobs(stack.DB, &recordingFileTranscoderPublisher{}),
				stack.SpiceDBClient,
				releaseadapter.NewTrackFiles(fileService),
				releaseadapter.NewMemberSummaries(stack.DB, stack.CDNURL),
				releaseadapter.NewArtistSummaries(stack.DB),
			)
			ctx := auth.WithUser(context.Background(), manager.AuthUserInfo())

			paused := make(chan model.UploadSession, 1)
			resume := make(chan struct{})
			var resumeOnce sync.Once
			resumeInitiation := func() {
				resumeOnce.Do(func() { close(resume) })
			}
			t.Cleanup(resumeInitiation)
			if testCase.pauseBeforeInsert {
				fileService.testBeforeTrackSessionInsert = func(session model.UploadSession) {
					paused <- session
					<-resume
				}
			} else {
				fileService.testAfterTrackSessionInsert = func(session model.UploadSession) {
					paused <- session
					<-resume
				}
			}

			type initiateResult struct {
				response *connect.Response[managev1.InitiateMultipartUploadResponse]
				err      error
			}
			initiateDone := make(chan initiateResult, 1)
			entityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK
			go func() {
				response, err := fileService.InitiateMultipartUpload(ctx, connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
					UploadType: managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
					EntityId:   trackID, EntityType: &entityType,
					FileName: "concurrent-track.mp3", FileSize: 1024, MimeType: "audio/mpeg",
				}))
				initiateDone <- initiateResult{response: response, err: err}
			}()
			session := <-paused

			type deleteResult struct {
				response *connect.Response[managev1.DeleteResponse]
				err      error
			}
			deleteStarted := make(chan struct{})
			deleteDone := make(chan deleteResult, 1)
			go func() {
				close(deleteStarted)
				response, err := trackService.DeleteTrack(
					ctx,
					connect.NewRequest(&managev1.DeleteTrackRequest{Id: trackID}),
				)
				deleteDone <- deleteResult{response: response, err: err}
			}()
			<-deleteStarted

			var deleted deleteResult
			if testCase.pauseBeforeInsert {
				select {
				case deleted = <-deleteDone:
				case <-time.After(5 * time.Second):
					t.Fatal("Track delete did not complete before the pre-insert upload was resumed")
				}
				require.NoError(t, deleted.err)
				require.True(t, deleted.response.Msg.GetSuccess())
			}
			resumeInitiation()

			var initiated initiateResult
			select {
			case initiated = <-initiateDone:
			case <-time.After(5 * time.Second):
				t.Fatal("Track upload initiation did not resume")
			}
			if !testCase.pauseBeforeInsert {
				select {
				case deleted = <-deleteDone:
				case <-time.After(5 * time.Second):
					t.Fatal("Track delete did not complete after the visible upload session was resumed")
				}
				require.NoError(t, deleted.err)
				require.True(t, deleted.response.Msg.GetSuccess())
			}
			if testCase.pauseBeforeInsert {
				require.Error(t, initiated.err)
				require.Equal(t, connect.CodeNotFound, connect.CodeOf(initiated.err))
				require.Nil(t, initiated.response)
			} else {
				require.NoError(t, initiated.err)
				require.Equal(t, session.UploadID, initiated.response.Msg.GetUploadId())
			}

			var trackCount, sessionCount int64
			require.NoError(t, stack.DB.Model(&model.Track{}).Where("id = ?", trackID).Count(&trackCount).Error)
			require.NoError(t, stack.DB.Model(&model.UploadSession{}).Where("upload_id = ?", session.UploadID).Count(&sessionCount).Error)
			require.Zero(t, trackCount)
			require.Zero(t, sessionCount)
			fileKey, err := uploadSessionObjectKey(session)
			require.NoError(t, err)
			_, err = s3Client.ListParts(t.Context(), &s3.ListPartsInput{
				Bucket: aws.String(stack.S3MediaBucket), Key: aws.String(fileKey), UploadId: aws.String(session.UploadID),
			})
			require.Error(t, err)
			require.True(t, IsMissingMultipartUploadAbortError(err), "multipart upload survived Track delete: %v", err)
		})
	}
}

func requireTrackPolicyFixture(t *testing.T, spiceDB *auth.SpiceDBClient, trackID string) {
	t.Helper()
	mutation, err := policyv1.Track.TouchPolicy(trackID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), mutation)
	require.NoError(t, err)
}

func TestCompleteMultipartUploadRecoversAmbiguousS3CompletionDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	observer := &multipartCompletionObservingTransport{returnNoSuchAfterCompletion: true}
	asyncPublisher := &hardCutAsyncPublisher{}
	fixture := prepareDirectEditorImageMultipart(
		t,
		stack,
		observingRuntimeS3Client(t, stack, observer),
		asyncPublisher,
	)

	request := &managev1.CompleteMultipartUploadRequest{
		FileId:   fixture.fileID,
		UploadId: fixture.uploadID,
	}
	wrongUploadID := "untrusted-opaque-upload-id"
	_, err := fixture.service.CompleteMultipartUpload(fixture.ctx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		FileId:   fixture.fileID,
		UploadId: wrongUploadID,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.Equal(t, 0, observer.count(), "a wrong upload identity must not bypass an active session")

	response, err := fixture.service.CompleteMultipartUpload(fixture.ctx, connect.NewRequest(request))
	require.NoError(t, err)
	require.Equal(t, fixture.fileID, response.Msg.GetFileId())
	require.Equal(t, 1, observer.count())

	var fileCount, sessionCount int64
	require.NoError(t, stack.DB.Model(&model.File{}).Where("id = ?", fixture.fileID).Count(&fileCount).Error)
	require.NoError(t, stack.DB.Model(&model.UploadSession{}).Where("upload_id = ?", fixture.uploadID).Count(&sessionCount).Error)
	require.EqualValues(t, 1, fileCount)
	require.Zero(t, sessionCount)
	require.Empty(t, decodeHardCutRoutedMessages(t, asyncPublisher.messages, eventpkg.SignalFileIngest, "", func() *managev1.FileIngestFailedEvent {
		return &managev1.FileIngestFailedEvent{}
	}))

	recovered, err := fixture.service.CompleteMultipartUpload(fixture.ctx, connect.NewRequest(request))
	require.NoError(t, err)
	require.Equal(t, response.Msg.GetFileId(), recovered.Msg.GetFileId())
	require.Equal(t, response.Msg.GetDelivery().GetFileId(), recovered.Msg.GetDelivery().GetFileId())
	require.Equal(t, 1, observer.count(), "response recovery must not repeat multipart completion")
	require.Empty(t, decodeHardCutRoutedMessages(t, asyncPublisher.messages, eventpkg.SignalFileIngest, "", func() *managev1.FileIngestAttachedEvent {
		return &managev1.FileIngestAttachedEvent{}
	}), "independent editor Files never publish attachment projection events")

	recoveredWithWrongID, err := fixture.service.CompleteMultipartUpload(fixture.ctx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		FileId:   fixture.fileID,
		UploadId: wrongUploadID,
	}))
	require.NoError(t, err)
	require.Equal(t, response.Msg.GetDelivery().GetFileId(), recoveredWithWrongID.Msg.GetDelivery().GetFileId())
	require.Equal(t, 1, observer.count(), "an opaque upload ID must not become a post-completion credential")

	otherAuthor := stack.CreateUser(t, policyv1.Role.Author().ID())
	otherAuthorCtx := auth.WithUser(context.Background(), otherAuthor.AuthUserInfo())
	_, err = fixture.service.CompleteMultipartUpload(otherAuthorCtx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		FileId:   fixture.fileID,
		UploadId: wrongUploadID,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	adminCtx := auth.WithUser(context.Background(), admin.AuthUserInfo())
	adminRecovery, err := fixture.service.CompleteMultipartUpload(adminCtx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		FileId:   fixture.fileID,
		UploadId: wrongUploadID,
	}))
	require.NoError(t, err)
	require.Equal(t, response.Msg.GetDelivery().GetFileId(), adminRecovery.Msg.GetDelivery().GetFileId())
	require.Equal(t, 1, observer.count(), "admin response recovery must not repeat multipart completion")
}

func TestCompleteMultipartUploadRestoresDedicatedAssetAfterResponseLossDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	manager := stack.CreateUser(t, policyv1.Role.User().ID())
	asyncPublisher := &hardCutAsyncPublisher{}
	service := NewFileService(
		stack.DB,
		runtimeS3Client(t, stack),
		asyncPublisher,
		stack.S3MediaBucket,
		stack.CDNURL,
		stack.MediaURL,
		stack.MediaSigningSecret,
		&recordingFileTranscoderPublisher{},
		stack.SpiceDBClient,
	)
	ctx := auth.WithUser(context.Background(), manager.AuthUserInfo())
	body, err := base64.StdEncoding.DecodeString("UklGRiwAAABXRUJQVlA4ICAAAAAwAQCdASoBAAEAAAAGJZwAA3AA/u4KZ/9qflZ7vuAAAA==")
	require.NoError(t, err)
	initiated, err := service.InitiateMultipartUpload(ctx, connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
		UploadType: managev1.UploadType_UPLOAD_TYPE_USER_AVATAR,
		EntityId:   manager.ID,
		FileName:   "response-loss-avatar.webp",
		FileSize:   int64(len(body)),
		MimeType:   "image/webp",
	}))
	require.NoError(t, err)
	requireDirectMultipartPrefixRejected(
		t,
		service,
		manager,
		initiated.Msg.GetFileId(),
		initiated.Msg.GetUploadId(),
		body,
	)
	handleMultipartPartRelayed(t, service, manager, initiated.Msg.GetFileId(), initiated.Msg.GetUploadId(), 1, body)

	request := &managev1.CompleteMultipartUploadRequest{
		FileId:   initiated.Msg.GetFileId(),
		UploadId: initiated.Msg.GetUploadId(),
	}
	completed, err := service.CompleteMultipartUpload(ctx, connect.NewRequest(request))
	require.NoError(t, err)
	require.NotNil(t, completed.Msg.GetDelivery().GetAsset())
	finalizedBeforeRetry := len(decodeHardCutRoutedMessages(t, asyncPublisher.messages, eventpkg.SignalFileIngest, "", func() *managev1.FileIngestFinalizedEvent {
		return &managev1.FileIngestFinalizedEvent{}
	}))

	recovered, err := service.CompleteMultipartUpload(ctx, connect.NewRequest(request))
	require.NoError(t, err)
	require.Equal(t, completed.Msg.GetDelivery().GetAsset().GetAssetId(), recovered.Msg.GetDelivery().GetAsset().GetAssetId())

	var assetCount int64
	require.NoError(t, stack.DB.Model(&model.PublicAsset{}).
		Where("source_file_id = ? AND kind = ? AND status = ?", initiated.Msg.GetFileId(), "avatar", model.PublicAssetStatusReady).
		Count(&assetCount).Error)
	require.EqualValues(t, 1, assetCount, "response recovery must not repeat dedicated asset promotion")
	require.Len(t, decodeHardCutRoutedMessages(t, asyncPublisher.messages, eventpkg.SignalFileIngest, "", func() *managev1.FileIngestFinalizedEvent {
		return &managev1.FileIngestFinalizedEvent{}
	}), finalizedBeforeRetry, "response recovery must not republish finalized progress")
}

func TestFileServiceMultipartEditorImageLifecycleDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Author().ID())
	imageBody, err := os.ReadFile(testutil.RepositoryTestImageJPEG(t))
	require.NoError(t, err)
	require.NotEmpty(t, imageBody)
	asyncPublisher := &hardCutAsyncPublisher{}
	svc := NewFileService(
		stack.DB,
		runtimeS3Client(t, stack),
		asyncPublisher,
		stack.S3MediaBucket,
		stack.CDNURL,
		stack.MediaURL,
		stack.MediaSigningSecret,
		&recordingFileTranscoderPublisher{},
		stack.SpiceDBClient,
	)
	ctx := auth.WithUser(context.Background(), manager.AuthUserInfo())
	fileName := fmt.Sprintf("editor-image-%d.jpg", time.Now().UnixNano())
	lastModified := time.Now().UnixMilli()
	initResp, err := svc.InitiateMultipartUpload(ctx, connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
		UploadType:       managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		FileName:         fileName,
		FileSize:         int64(len(imageBody)),
		MimeType:         "image/jpeg",
		FileLastModified: &lastModified,
	}))
	require.NoError(t, err)
	require.NotEmpty(t, initResp.Msg.GetUploadId())
	require.NotEmpty(t, initResp.Msg.GetFileId())
	require.Equal(t, "jpg", initResp.Msg.GetExtension())
	fileKey, err := mediaauth.MediaObjectKey(initResp.Msg.GetFileId(), initResp.Msg.GetExtension())
	require.NoError(t, err)

	candidateResp, err := svc.FindMultipartUploadCandidate(ctx, connect.NewRequest(&managev1.FindMultipartUploadCandidateRequest{
		UploadType:       managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		FileId:           hardCutPtrString(initResp.Msg.GetFileId()),
		UploadId:         hardCutPtrString(initResp.Msg.GetUploadId()),
		FileName:         &fileName,
		FileSize:         ptrInt64(int64(len(imageBody))),
		MimeType:         hardCutPtrString("image/jpeg"),
		FileLastModified: &lastModified,
	}))
	require.NoError(t, err)
	require.Equal(t, initResp.Msg.GetUploadId(), candidateResp.Msg.GetUploadId())
	require.Equal(t, initResp.Msg.GetFileId(), candidateResp.Msg.GetFileId())
	require.Empty(t, candidateResp.Msg.GetUploadedParts())

	part := handleMultipartPartDirect(t, svc, manager, initResp.Msg.GetFileId(), initResp.Msg.GetUploadId(), 1, imageBody)
	require.NotEmpty(t, part.ETag)
	candidateResp, err = svc.FindMultipartUploadCandidate(ctx, connect.NewRequest(&managev1.FindMultipartUploadCandidateRequest{
		UploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		FileId:     hardCutPtrString(initResp.Msg.GetFileId()),
		UploadId:   hardCutPtrString(initResp.Msg.GetUploadId()),
	}))
	require.NoError(t, err)
	requireUploadSessionStatus(t, stack.DB, initResp.Msg.GetUploadId(), managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UPLOADING)
	require.Equal(t, []*managev1.UploadPartInfo{{PartNumber: 1, Etag: part.ETag}}, candidateResp.Msg.GetUploadedParts())

	completeResp, err := svc.CompleteMultipartUpload(ctx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		FileId:   initResp.Msg.GetFileId(),
		UploadId: initResp.Msg.GetUploadId(),
	}))
	require.NoError(t, err)
	require.Equal(t, initResp.Msg.GetFileId(), completeResp.Msg.GetFileId())
	require.Equal(t, initResp.Msg.GetFileId(), completeResp.Msg.GetDelivery().GetFileId())
	require.Equal(t, "jpg", completeResp.Msg.GetDelivery().GetExtension())
	requireCanonicalInlineRef(t, completeResp.Msg.GetDelivery().GetInline(), initResp.Msg.GetFileId(), "jpg", "image/jpeg")
	requireCanonicalDownloadRef(t, completeResp.Msg.GetDelivery().GetDownload(), initResp.Msg.GetFileId(), "jpg", "image/jpeg")
	require.NotNil(t, completeResp.Msg.GetDelivery().GetAsset())
	recoveredResp, err := svc.CompleteMultipartUpload(ctx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		FileId:   initResp.Msg.GetFileId(),
		UploadId: initResp.Msg.GetUploadId(),
	}))
	require.NoError(t, err)
	require.Equal(t, completeResp.Msg.GetDelivery().GetFileId(), recoveredResp.Msg.GetDelivery().GetFileId())
	require.Equal(t, completeResp.Msg.GetDelivery().GetAsset().GetAssetId(), recoveredResp.Msg.GetDelivery().GetAsset().GetAssetId())

	var stored model.File
	require.NoError(t, stack.DB.Where("id = ?", initResp.Msg.GetFileId()).First(&stored).Error)
	require.Equal(t, storedFileBasename(fileName, initResp.Msg.GetFileId(), "jpg"), stored.FileName)
	require.Equal(t, "image/jpeg", stored.MimeType)
	require.EqualValues(t, len(imageBody), stored.FileSize)
	require.Equal(t, "jpg", stored.Extension)
	require.Empty(t, stored.SHA256)
	expectedSHA := sha256.Sum256(imageBody)
	require.True(t, runtimeS3ObjectExists(t, runtimeS3Client(t, stack), stack.S3MediaBucket, fileKey))

	var asset model.PublicAsset
	require.NoError(t, stack.DB.Where(
		"source_file_id = ? AND status = ?",
		initResp.Msg.GetFileId(),
		model.PublicAssetStatusReady,
	).Take(&asset).Error)
	require.Equal(t, "image", asset.Kind)
	require.Equal(t, expectedSHA[:], asset.SHA256)
	require.True(t, runtimeS3ObjectExists(t, runtimeS3Client(t, stack), stack.S3MediaBucket, asset.ObjectKey))
	var assetCount int64
	require.NoError(t, stack.DB.Model(&model.PublicAsset{}).
		Where("source_file_id = ? AND status <> ?", initResp.Msg.GetFileId(), model.PublicAssetStatusDeleted).
		Count(&assetCount).Error)
	require.EqualValues(t, 1, assetCount)

	candidateResp, err = svc.FindMultipartUploadCandidate(ctx, connect.NewRequest(&managev1.FindMultipartUploadCandidateRequest{
		UploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		FileId:     hardCutPtrString(initResp.Msg.GetFileId()),
		UploadId:   hardCutPtrString(initResp.Msg.GetUploadId()),
	}))
	require.NoError(t, err)
	require.Empty(t, candidateResp.Msg.GetUploadId(), "completed uploads must not remain resumable candidates")

	uploadEvents := decodeHardCutRoutedMessages(t, asyncPublisher.messages, eventpkg.SignalFileIngest, "", func() *managev1.FileIngestUploadEvent {
		return &managev1.FileIngestUploadEvent{}
	})
	require.NotEmpty(t, uploadEvents)
	require.Equal(t, initResp.Msg.GetFileId(), uploadEvents[len(uploadEvents)-1].GetIdentity().GetFileId())
	require.EqualValues(t, 100, uploadEvents[len(uploadEvents)-1].GetProgress().GetPercentage())

	finalizedEvents := decodeHardCutRoutedMessages(t, asyncPublisher.messages, eventpkg.SignalFileIngest, "", func() *managev1.FileIngestFinalizedEvent {
		return &managev1.FileIngestFinalizedEvent{}
	})
	require.Len(t, finalizedEvents, 1)
	require.Equal(t, initResp.Msg.GetFileId(), finalizedEvents[0].GetIdentity().GetFileId())

	attachedEvents := decodeHardCutRoutedMessages(t, asyncPublisher.messages, eventpkg.SignalFileIngest, "", func() *managev1.FileIngestAttachedEvent {
		return &managev1.FileIngestAttachedEvent{}
	})
	require.Empty(t, attachedEvents, "independent editor File completion must not publish attachment events")
}

func TestFileServiceMultipartGeneralFileUsesCapabilityPairAndDateFolderDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	starter := stack.CreateUser(t, policyv1.Role.Author().ID())
	completer := stack.CreateUser(t, policyv1.Role.Author().ID())
	body, err := os.ReadFile(testutil.RepositoryTestImageJPEG(t))
	require.NoError(t, err)

	svc := NewFileService(
		stack.DB,
		runtimeS3Client(t, stack),
		&hardCutAsyncPublisher{},
		stack.S3MediaBucket,
		stack.CDNURL,
		stack.MediaURL,
		stack.MediaSigningSecret,
		&recordingFileTranscoderPublisher{},
		stack.SpiceDBClient,
	)
	starterCtx := auth.WithUser(context.Background(), starter.AuthUserInfo())
	initiated, err := svc.InitiateMultipartUpload(starterCtx, connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
		UploadType: managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE,
		FileName:   "Field Recording.jpg",
		FileSize:   int64(len(body)),
		MimeType:   "image/jpeg",
	}))
	require.NoError(t, err)
	require.NotEmpty(t, initiated.Msg.GetUploadId())
	require.NotEmpty(t, initiated.Msg.GetFileId())

	var targetIsNull bool
	require.NoError(t, stack.DB.Raw(
		`SELECT entity_id IS NULL FROM upload_session WHERE upload_id = ?`,
		initiated.Msg.GetUploadId(),
	).Scan(&targetIsNull).Error)
	require.True(t, targetIsNull)

	_, err = svc.FindMultipartUploadCandidate(starterCtx, connect.NewRequest(&managev1.FindMultipartUploadCandidateRequest{
		UploadType: managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE,
	}))
	require.Error(t, err, "general uploads cannot be rediscovered without the capability pair")

	_, err = svc.FindMultipartUploadCandidate(starterCtx, connect.NewRequest(&managev1.FindMultipartUploadCandidateRequest{
		UploadType: managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE,
		FileId:     &initiated.Msg.FileId,
	}))
	require.Error(t, err, "the file ID alone is not the upload capability")

	candidate, err := svc.FindMultipartUploadCandidate(starterCtx, connect.NewRequest(&managev1.FindMultipartUploadCandidateRequest{
		UploadType: managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE,
		FileId:     &initiated.Msg.FileId,
		UploadId:   &initiated.Msg.UploadId,
	}))
	require.NoError(t, err)
	require.Equal(t, initiated.Msg.GetUploadId(), candidate.Msg.GetUploadId())
	require.Equal(t, initiated.Msg.GetFileId(), candidate.Msg.GetFileId())

	handleMultipartPartDirect(t, svc, completer, initiated.Msg.GetFileId(), initiated.Msg.GetUploadId(), 1, body)
	completerCtx := auth.WithUser(context.Background(), completer.AuthUserInfo())
	completed, err := svc.CompleteMultipartUpload(completerCtx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		FileId: initiated.Msg.GetFileId(), UploadId: initiated.Msg.GetUploadId(),
	}))
	require.NoError(t, err)
	require.Equal(t, initiated.Msg.GetFileId(), completed.Msg.GetFileId())

	var stored struct {
		FileName           string  `gorm:"column:file_name"`
		Extension          string  `gorm:"column:extension"`
		UploadedByMemberID *string `gorm:"column:uploaded_by_member_id"`
		FolderPath         string  `gorm:"column:folder_path"`
		BindingTargetNull  bool    `gorm:"column:binding_target_null"`
	}
	require.NoError(t, stack.DB.Raw(`
		SELECT file.file_name, file.extension, file.uploaded_by_member_id,
		       year.name || '/' || month.name || '/' || day.name AS folder_path,
		       binding.entity_id IS NULL AS binding_target_null
		  FROM file
		  JOIN file_folder day ON day.id = file.folder_id
		  JOIN file_folder month ON month.id = day.parent_id
		  JOIN file_folder year ON year.id = month.parent_id
		  JOIN file_ingest_binding binding ON binding.file_id = file.id
		 WHERE file.id = ?`, initiated.Msg.GetFileId()).Scan(&stored).Error)
	require.Equal(t, "Field Recording", stored.FileName)
	require.Equal(t, "jpg", stored.Extension)
	require.NotNil(t, stored.UploadedByMemberID)
	require.Equal(t, completer.ID, *stored.UploadedByMemberID)
	require.Equal(t, time.Now().UTC().Format("2006/01/02"), stored.FolderPath)
	require.True(t, stored.BindingTargetNull)
}

func TestFileServiceMultipartEditorMeshStoresGLBAndServesCDNDirectIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStackWithCDN(t)
	manager := stack.CreateUser(t, policyv1.Role.Author().ID())

	meshBody, err := os.ReadFile(testutil.RepositoryTestMeshGLB(t))
	require.NoError(t, err)
	require.NotEmpty(t, meshBody)

	asyncPublisher := &hardCutAsyncPublisher{}
	svc := NewFileService(
		stack.DB,
		runtimeS3Client(t, stack),
		asyncPublisher,
		stack.S3MediaBucket,
		stack.CDNURL,
		stack.MediaURL,
		stack.MediaSigningSecret,
		&recordingFileTranscoderPublisher{},
		stack.SpiceDBClient,
	)
	ctx := auth.WithUser(context.Background(), manager.AuthUserInfo())
	fileName := fmt.Sprintf("editor-mesh-%d.glb", time.Now().UnixNano())
	lastModified := time.Now().UnixMilli()
	initResp, err := svc.InitiateMultipartUpload(ctx, connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
		UploadType:       managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH,
		FileName:         fileName,
		FileSize:         int64(len(meshBody)),
		MimeType:         "model/gltf-binary",
		FileLastModified: &lastModified,
	}))
	require.NoError(t, err)
	require.NotEmpty(t, initResp.Msg.GetUploadId())
	require.NotEmpty(t, initResp.Msg.GetFileId())
	require.Equal(t, "glb", initResp.Msg.GetExtension())
	fileKey, err := mediaauth.MediaObjectKey(initResp.Msg.GetFileId(), initResp.Msg.GetExtension())
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(fileKey, ".glb"))

	part := handleMultipartPartDirect(t, svc, manager, initResp.Msg.GetFileId(), initResp.Msg.GetUploadId(), 1, meshBody)
	require.NotEmpty(t, part.ETag)

	completeResp, err := svc.CompleteMultipartUpload(ctx, connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		FileId:   initResp.Msg.GetFileId(),
		UploadId: initResp.Msg.GetUploadId(),
	}))
	require.NoError(t, err)
	require.Equal(t, initResp.Msg.GetFileId(), completeResp.Msg.GetFileId())
	delivery := completeResp.Msg.GetDelivery()
	require.Equal(t, initResp.Msg.GetFileId(), delivery.GetFileId())
	require.Equal(t, "glb", delivery.GetExtension())
	requireCanonicalInlineRef(t, delivery.GetInline(), initResp.Msg.GetFileId(), "glb", "model/gltf-binary")
	downloadURL := delivery.GetDownload().GetUrl()
	requireCanonicalDownloadRef(t, delivery.GetDownload(), initResp.Msg.GetFileId(), "glb", "model/gltf-binary")
	parsedDownloadURL, err := url.Parse(downloadURL)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(parsedDownloadURL.Path, "/"+initResp.Msg.GetFileId()+".glb"))

	var stored model.File
	require.NoError(t, stack.DB.Where("id = ?", initResp.Msg.GetFileId()).First(&stored).Error)
	require.Equal(t, strings.TrimSuffix(fileName, ".glb"), stored.FileName)
	require.Equal(t, "model/gltf-binary", stored.MimeType)
	require.EqualValues(t, len(meshBody), stored.FileSize)
	require.Equal(t, "glb", stored.Extension)
	require.Empty(t, stored.SHA256)
	require.Nil(t, stored.IngestSlotID)

	s3Client := runtimeS3Client(t, stack)
	headResp, err := s3Client.HeadObject(context.Background(), &s3.HeadObjectInput{
		Bucket: aws.String(stack.S3MediaBucket),
		Key:    aws.String(fileKey),
	})
	require.NoError(t, err)
	require.Equal(t, "model/gltf-binary", aws.ToString(headResp.ContentType))

	cdnReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, downloadURL, nil)
	require.NoError(t, err)
	cdnReq.Header.Set("Origin", stack.WebURL)
	cdnResp, err := http.DefaultClient.Do(cdnReq)
	require.NoError(t, err)
	defer cdnResp.Body.Close()
	require.Equal(t, http.StatusOK, cdnResp.StatusCode)
	require.Equal(t, stack.WebURL, cdnResp.Header.Get("Access-Control-Allow-Origin"))
	require.Contains(t, cdnResp.Header.Get("Content-Type"), "model/gltf-binary")
	deliveredBody, err := io.ReadAll(cdnResp.Body)
	require.NoError(t, err)
	require.Equal(t, meshBody, deliveredBody)
}

func ptrInt64(value int64) *int64 {
	return &value
}

type uploadPartResponse struct {
	ETag string `json:"etag"`
}

func requireCanonicalDownloadRef(
	t *testing.T,
	ref *commonv1.ExpiringMediaRef,
	fileID string,
	extension string,
	mimeType string,
) {
	t.Helper()
	require.NotNil(t, ref)
	require.Equal(t, fileID, ref.GetFileId())
	require.Equal(t, commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_DOWNLOAD, ref.GetPurpose())
	require.Equal(t, extension, ref.GetExtension())
	require.Equal(t, mimeType, ref.GetMimeType())
	require.NotEmpty(t, ref.GetUrl())
	require.NotNil(t, ref.GetExpiresAt())
	require.True(t, ref.GetExpiresAt().AsTime().After(time.Now()))
}

func requireCanonicalInlineRef(
	t *testing.T,
	ref *commonv1.ExpiringMediaRef,
	fileID string,
	extension string,
	mimeType string,
) {
	t.Helper()
	require.NotNil(t, ref)
	require.Equal(t, fileID, ref.GetFileId())
	require.Equal(t, commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_INLINE, ref.GetPurpose())
	require.Equal(t, extension, ref.GetExtension())
	require.Equal(t, mimeType, ref.GetMimeType())
	require.NotEmpty(t, ref.GetUrl())
	require.NotNil(t, ref.GetExpiresAt())
	require.True(t, ref.GetExpiresAt().AsTime().After(time.Now()))
}

func handleMultipartPartDirect(
	t *testing.T,
	svc *FileService,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	partNumber int,
	body []byte,
) uploadPartResponse {
	t.Helper()

	query := url.Values{}
	query.Set("fileId", fileID)
	query.Set("uploadId", uploadID)
	query.Set("partNumber", fmt.Sprintf("%d", partNumber))
	if partNumber == 1 {
		prefixQuery := url.Values{}
		prefixQuery.Set("fileId", fileID)
		prefixQuery.Set("uploadId", uploadID)
		prefixSize := min(len(body), multipartSniffBytes)
		prefixReq := httptest.NewRequest(http.MethodPost, "/upload/prefix?"+prefixQuery.Encode(), bytes.NewReader(body[:prefixSize]))
		testutil.ApplyAuthHeaders(prefixReq.Header, user)
		prefixReq.ContentLength = int64(prefixSize)
		prefixRec := httptest.NewRecorder()
		auth.RequireGatewaySession(
			svc.db,
			http.HandlerFunc(svc.HandleVerifyUploadPrefix),
		).ServeHTTP(prefixRec, prefixReq)
		require.Equal(t, http.StatusNoContent, prefixRec.Code, prefixRec.Body.String())
	}

	presignReq := httptest.NewRequest(http.MethodPost, "/upload/part/presign?"+query.Encode(), nil)
	testutil.ApplyAuthHeaders(presignReq.Header, user)
	presignRec := httptest.NewRecorder()
	auth.RequireGatewaySession(
		svc.db,
		http.HandlerFunc(svc.HandlePresignUploadPart),
	).ServeHTTP(presignRec, presignReq)
	require.Equal(t, http.StatusOK, presignRec.Code, presignRec.Body.String())
	var presigned multipartPresignResponse
	require.NoError(t, json.NewDecoder(presignRec.Body).Decode(&presigned))
	require.NotEmpty(t, presigned.URL)

	putReq, err := http.NewRequest(http.MethodPut, presigned.URL, bytes.NewReader(body))
	require.NoError(t, err)
	putReq.ContentLength = int64(len(body))
	putResp, err := http.DefaultClient.Do(putReq)
	require.NoError(t, err)
	defer putResp.Body.Close()
	require.Equal(t, http.StatusOK, putResp.StatusCode)

	confirmReq := httptest.NewRequest(http.MethodPost, "/upload/part/confirm?"+query.Encode(), nil)
	testutil.ApplyAuthHeaders(confirmReq.Header, user)
	confirmRec := httptest.NewRecorder()
	auth.RequireGatewaySession(
		svc.db,
		http.HandlerFunc(svc.HandleConfirmUploadPart),
	).ServeHTTP(confirmRec, confirmReq)
	require.Equal(t, http.StatusOK, confirmRec.Code, confirmRec.Body.String())
	var payload uploadPartResponse
	require.NoError(t, json.NewDecoder(confirmRec.Body).Decode(&payload))
	return payload
}

func handleMultipartPartRelayed(
	t *testing.T,
	svc *FileService,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	partNumber int,
	body []byte,
) uploadPartResponse {
	t.Helper()

	query := url.Values{}
	query.Set("fileId", fileID)
	query.Set("uploadId", uploadID)
	query.Set("partNumber", fmt.Sprintf("%d", partNumber))
	req := httptest.NewRequest(http.MethodPut, "/upload/part?"+query.Encode(), bytes.NewReader(body))
	testutil.ApplyAuthHeaders(req.Header, user)
	req.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	auth.RequireGatewaySession(svc.db, http.HandlerFunc(svc.HandleUploadPart)).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var payload uploadPartResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
	require.NotEmpty(t, payload.ETag)
	return payload
}

func requireMultipartPartRelayRejected(
	t *testing.T,
	svc *FileService,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	partNumber int,
	body []byte,
) {
	t.Helper()
	query := url.Values{}
	query.Set("fileId", fileID)
	query.Set("uploadId", uploadID)
	query.Set("partNumber", fmt.Sprintf("%d", partNumber))
	req := httptest.NewRequest(http.MethodPut, "/upload/part?"+query.Encode(), bytes.NewReader(body))
	testutil.ApplyAuthHeaders(req.Header, user)
	req.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	auth.RequireGatewaySession(svc.db, http.HandlerFunc(svc.HandleUploadPart)).ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func requireDirectMultipartPrefixRejected(
	t *testing.T,
	svc *FileService,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	body []byte,
) {
	t.Helper()
	query := url.Values{}
	query.Set("fileId", fileID)
	query.Set("uploadId", uploadID)
	prefixSize := min(len(body), multipartSniffBytes)
	req := httptest.NewRequest(http.MethodPost, "/upload/prefix?"+query.Encode(), bytes.NewReader(body[:prefixSize]))
	testutil.ApplyAuthHeaders(req.Header, user)
	req.ContentLength = int64(prefixSize)
	rec := httptest.NewRecorder()
	auth.RequireGatewaySession(svc.db, http.HandlerFunc(svc.HandleVerifyUploadPrefix)).ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}
