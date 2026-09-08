//go:build integration

package filemedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/echovisionlab/geul-api/internal/auth"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestRuntimeEditorFileUploadIsIndependentAndFileScoped(t *testing.T) {
	stack := testutil.SetupSharedRuntimeStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Author().ID())
	fileClient := managev1connect.NewFileServiceClient(&http.Client{Timeout: 30 * time.Second}, stack.BackendURL)
	audioBytes, err := os.ReadFile(testutil.RepositoryTestAudioMP3(t))
	require.NoError(t, err)
	fileID, delivery := completeRuntimeEditorMediaUploadAndWait(
		t,
		stack,
		fileClient,
		manager,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		"audio/mpeg",
		runtimeTestFileName("independent-editor-audio.mp3"),
		audioBytes,
		func(delivery *commonv1.MediaDelivery) bool {
			return delivery.GetProcessingStatus() == commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY &&
				delivery.GetPlayback().GetUrl() != "" &&
				delivery.GetSpectrogram().GetUrl() != "" &&
				delivery.GetWaveform().GetUrl() != ""
		},
	)
	require.NotEmpty(t, fileID)
	require.NotEmpty(t, delivery.GetPlayback().GetUrl())
	var binding model.FileIngestBinding
	require.NoError(t, stack.DB.Where("file_id = ?", fileID).Take(&binding).Error)
	require.Equal(t, managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO.String(), binding.UploadType)
	require.Empty(t, binding.EntityID)
	require.Nil(t, binding.EntityType)
	var usageCount int64
	require.NoError(t, stack.DB.Table("content_block_attachment").Where("file_id = ?", fileID).Count(&usageCount).Error)
	require.Zero(t, usageCount, "upload completion must not attach the independent File to a document")
}

type runtimeRemoteImportResolver struct {
	ip net.IP
}

func (r runtimeRemoteImportResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	return []net.IP{append(net.IP(nil), r.ip...)}, nil
}

func TestRuntimeEditorFileRemoteImportIsIndependent(t *testing.T) {
	stack := testutil.SetupSharedRuntimeStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Author().ID())

	body := []byte("runtime remote editor File\n")
	remoteServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "media.example.com", request.Host)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, err := w.Write(body)
		require.NoError(t, err)
	}))
	t.Cleanup(remoteServer.Close)
	remoteServerURL, err := url.Parse(remoteServer.URL)
	require.NoError(t, err)

	sqlDB, err := stack.DB.DB()
	require.NoError(t, err)
	confirmedPublisher, err := mq.NewPublisher(sqlDB)
	require.NoError(t, err)
	fileService := NewFileService(
		stack.DB,
		runtimeS3Client(t, stack),
		confirmedPublisher,
		stack.S3MediaBucket,
		stack.CDNURL,
		stack.MediaURL,
		stack.MediaSigningSecret,
		&recordingFileTranscoderPublisher{},
		stack.SpiceDBClient,
	)
	fileService.remoteImportResolver = runtimeRemoteImportResolver{ip: net.ParseIP("8.8.8.8")}
	fileService.remoteImportDialer = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, remoteServerURL.Host)
	}
	remoteTransport, ok := remoteServer.Client().Transport.(*http.Transport)
	require.True(t, ok)
	fileService.remoteImportBaseTransport = remoteTransport

	correlationID := uuid.NewString()
	request := connect.NewRequest(&managev1.DownloadFromUrlRequest{
		UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT,
		Url:           "https://media.example.com/runtime-attachment.txt",
		CorrelationId: &correlationID,
	})
	response, err := fileService.DownloadFromUrl(
		auth.WithUser(context.Background(), manager.AuthUserInfo()),
		request,
	)
	require.NoError(t, err)
	require.NotEmpty(t, response.Msg.FileId)
	require.Empty(t, response.Msg.GetSlotId())
	canonicalFileName := response.Msg.GetDelivery().GetFileName()
	require.NotEmpty(t, canonicalFileName)

	stored := requireRuntimeCanonicalFileRecord(
		t,
		stack.DB,
		response.Msg.FileId,
		response.Msg.GetDelivery().GetExtension(),
		"text/plain",
		body,
	)
	expectedSHA := sha256.Sum256(body)
	require.Equal(t, expectedSHA[:], stored.SHA256)
	var binding model.FileIngestBinding
	require.NoError(t, stack.DB.Where("file_id = ?", response.Msg.FileId).Take(&binding).Error)
	require.Equal(t, managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT.String(), binding.UploadType)
	require.Empty(t, binding.EntityID)
	require.Nil(t, binding.EntityType)
	var usageCount int64
	require.NoError(t, stack.DB.Table("content_block_attachment").Where("file_id = ?", response.Msg.FileId).Count(&usageCount).Error)
	require.Zero(t, usageCount)
}

func TestRuntimeTrackAudioUploadAPIFlows(t *testing.T) {
	stack := testutil.SetupSharedRuntimeStack(t)

	manager := stack.CreateUser(t, policyv1.Role.Admin().ID())
	staleManager := stack.CreateUser(t, policyv1.Role.Author().ID())
	outsider := stack.CreateUser(t, policyv1.Role.User().ID())
	releaseID := testutil.CreateReleaseViaAPI(t, stack.BackendURL, manager)

	fileClient := managev1connect.NewFileServiceClient(&http.Client{Timeout: 30 * time.Second}, stack.BackendURL)
	trackClient := managev1connect.NewTrackServiceClient(&http.Client{Timeout: 30 * time.Second}, stack.BackendURL)

	createReq := connect.NewRequest(&managev1.CreateTrackRequest{
		ReleaseId: releaseID,
		Title:     runtimeTestFileName("runtime-track"),
	})
	setAuthHeaders(createReq.Header(), staleManager)
	_, err := trackClient.CreateTrack(context.Background(), createReq)
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	createReq = connect.NewRequest(&managev1.CreateTrackRequest{
		ReleaseId: releaseID,
		Title:     runtimeTestFileName("runtime-track"),
	})
	setAuthHeaders(createReq.Header(), manager)

	createResp, err := trackClient.CreateTrack(context.Background(), createReq)
	require.NoError(t, err)
	trackID := createResp.Msg.Id
	require.NotEmpty(t, trackID)
	testutil.AssertManagedReleaseTrackAuthority(t, stack.DB, releaseID, trackID)

	fixturePath := testutil.RepositoryTestAudioMP3(t)
	fixtureBytes, err := os.ReadFile(fixturePath)
	require.NoError(t, err)
	audioMimeType := "audio/mpeg"

	t.Run("deny initiate without track edit permission", func(t *testing.T) {
		req := connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
			UploadType: managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
			EntityId:   trackID,
			EntityType: runtimePtr(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK),
			FileSize:   int64(len(fixtureBytes)),
			MimeType:   audioMimeType,
			FileName:   runtimeTestFileName("track-deny.mp3"),
		})
		setAuthHeaders(req.Header(), outsider)

		_, err := fileClient.InitiateMultipartUpload(context.Background(), req)
		require.Error(t, err)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})

	t.Run("initiate upload then abort and clear track resume candidate", func(t *testing.T) {
		fileName := runtimeTestFileName("track-abort.mp3")
		fileLastModified := time.Now().UnixMilli()

		initReq := connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
			UploadType:       managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
			EntityId:         trackID,
			EntityType:       runtimePtr(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK),
			FileSize:         int64(len(fixtureBytes)),
			MimeType:         audioMimeType,
			FileName:         fileName,
			FileLastModified: &fileLastModified,
		})
		setAuthHeaders(initReq.Header(), manager)

		initResp, err := fileClient.InitiateMultipartUpload(context.Background(), initReq)
		require.NoError(t, err)

		uploadPartResp := uploadMultipartPart(
			t,
			stack.BackendURL,
			manager,
			initResp.Msg.FileId,
			initResp.Msg.UploadId,
			1,
			fixtureBytes,
		)
		require.NotEmpty(t, uploadPartResp.ETag)

		candidateReq := connect.NewRequest(&managev1.FindMultipartUploadCandidateRequest{
			UploadType:       managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
			EntityId:         trackID,
			EntityType:       runtimePtr(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK),
			FileName:         &fileName,
			FileSize:         runtimePtr(int64(len(fixtureBytes))),
			MimeType:         runtimePtr(audioMimeType),
			FileLastModified: &fileLastModified,
		})
		setAuthHeaders(candidateReq.Header(), manager)

		candidateResp, err := fileClient.FindMultipartUploadCandidate(context.Background(), candidateReq)
		require.NoError(t, err)
		require.Equal(t, initResp.Msg.UploadId, runtimeDeref(candidateResp.Msg.UploadId))
		require.Equal(t, initResp.Msg.FileId, runtimeDeref(candidateResp.Msg.FileId))
		require.Equal(t, managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UPLOADING, candidateResp.Msg.Status)

		abortReq := connect.NewRequest(&managev1.AbortMultipartUploadRequest{
			FileId:   initResp.Msg.FileId,
			UploadId: initResp.Msg.UploadId,
		})
		setAuthHeaders(abortReq.Header(), manager)

		abortResp, err := fileClient.AbortMultipartUpload(context.Background(), abortReq)
		require.NoError(t, err)
		require.True(t, abortResp.Msg.Success)

		candidateReq = connect.NewRequest(&managev1.FindMultipartUploadCandidateRequest{
			UploadType:       managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
			EntityId:         trackID,
			EntityType:       runtimePtr(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK),
			FileName:         &fileName,
			FileSize:         runtimePtr(int64(len(fixtureBytes))),
			MimeType:         runtimePtr(audioMimeType),
			FileLastModified: &fileLastModified,
		})
		setAuthHeaders(candidateReq.Header(), manager)

		candidateResp, err = fileClient.FindMultipartUploadCandidate(context.Background(), candidateReq)
		require.NoError(t, err)
		require.Nil(t, candidateResp.Msg.UploadId)
	})

	t.Run("unstarted presigned parts keep confirmed parts resumable", func(t *testing.T) {
		fileName := runtimeTestFileName("track-interrupt.mp3")
		fileLastModified := time.Now().UnixMilli()
		interruptionCorrelationID := uuid.NewString()
		resumeCorrelationID := uuid.NewString()
		completionCorrelationID := uuid.NewString()
		failedReceiver := newFileIngestSignalReceiver(t, stack.PostgresDSN)
		fullPart := make([]byte, chunkSize)
		copy(fullPart, fixtureBytes)
		lastPart := fixtureBytes
		fileSize := int64((chunkSize * 9) + len(lastPart))

		initReq := connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
			UploadType:       managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
			EntityId:         trackID,
			EntityType:       runtimePtr(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK),
			FileSize:         fileSize,
			MimeType:         audioMimeType,
			FileName:         fileName,
			FileLastModified: &fileLastModified,
		})
		setAuthHeaders(initReq.Header(), manager)

		initResp, err := fileClient.InitiateMultipartUpload(context.Background(), initReq)
		require.NoError(t, err)

		uploadPartResp := uploadMultipartPartWithCorrelation(
			t,
			stack.BackendURL,
			manager,
			initResp.Msg.FileId,
			initResp.Msg.UploadId,
			1,
			fullPart,
			resumeCorrelationID,
		)
		require.NotEmpty(t, uploadPartResp.ETag)

		for _, partNumber := range []int{2, 3, 4, 5, 6} {
			presigned := requestMultipartPartPresign(
				t,
				stack.BackendURL,
				manager,
				initResp.Msg.FileId,
				initResp.Msg.UploadId,
				partNumber,
				interruptionCorrelationID,
			)
			require.NotEmpty(t, presigned.URL)
		}

		require.Eventually(t, func() bool {
			return countUploadSessions(t, stack.DB, initResp.Msg.UploadId) == 1
		}, 5*time.Second, 200*time.Millisecond)
		require.Equal(t, int64(1), countUploadParts(t, stack.DB, initResp.Msg.UploadId))
		requireNoFileIngestFailedEvent(t, failedReceiver, 2*time.Second, func(event *managev1.FileIngestFailedEvent) bool {
			return event.CorrelationId == interruptionCorrelationID && event.GetIdentity().GetFileId() == initResp.Msg.FileId
		})

		candidateReq := connect.NewRequest(&managev1.FindMultipartUploadCandidateRequest{
			UploadType:       managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
			EntityId:         trackID,
			EntityType:       runtimePtr(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK),
			FileName:         &fileName,
			FileSize:         runtimePtr(fileSize),
			MimeType:         runtimePtr(audioMimeType),
			FileLastModified: &fileLastModified,
		})
		setAuthHeaders(candidateReq.Header(), manager)

		candidateResp, err := fileClient.FindMultipartUploadCandidate(context.Background(), candidateReq)
		require.NoError(t, err)
		require.Equal(t, initResp.Msg.UploadId, runtimeDeref(candidateResp.Msg.UploadId))
		require.Equal(t, initResp.Msg.FileId, runtimeDeref(candidateResp.Msg.FileId))
		require.Equal(t, managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UPLOADING, candidateResp.Msg.Status)

		resumeReq := connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
			UploadType:       managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
			EntityId:         trackID,
			EntityType:       runtimePtr(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK),
			FileSize:         fileSize,
			MimeType:         audioMimeType,
			FileName:         fileName,
			FileLastModified: &fileLastModified,
		})
		setAuthHeaders(resumeReq.Header(), manager)
		resumeResp, err := fileClient.InitiateMultipartUpload(context.Background(), resumeReq)
		require.NoError(t, err)
		require.True(t, resumeResp.Msg.GetResumed())
		require.Equal(t, initResp.Msg.GetUploadId(), resumeResp.Msg.GetUploadId())
		require.Equal(t, initResp.Msg.GetFileId(), resumeResp.Msg.GetFileId())
		require.Equal(t, []*managev1.UploadPartInfo{{PartNumber: 1, Etag: uploadPartResp.ETag}}, resumeResp.Msg.GetUploadedParts())

		uploadedParts := resumeResp.Msg.GetUploadedParts()
		require.Len(t, uploadedParts, 1)
		require.Equal(t, int32(1), uploadedParts[0].GetPartNumber())
		require.Equal(t, uploadPartResp.ETag, uploadedParts[0].GetEtag())

		uploadReceiver := newFileIngestSignalReceiver(t, stack.PostgresDSN)
		for partNumber := 2; partNumber <= 9; partNumber++ {
			part := uploadMultipartPartWithCorrelation(
				t,
				stack.BackendURL,
				manager,
				initResp.Msg.FileId,
				initResp.Msg.UploadId,
				partNumber,
				fullPart,
				resumeCorrelationID,
			)
			require.NotEmpty(t, part.ETag)
		}
		part := uploadMultipartPartWithCorrelation(
			t,
			stack.BackendURL,
			manager,
			initResp.Msg.FileId,
			initResp.Msg.UploadId,
			10,
			lastPart,
			resumeCorrelationID,
		)
		require.NotEmpty(t, part.ETag)

		uploadEvent := waitForFileIngestUploadEvent(t, uploadReceiver, 10*time.Second, func(event *managev1.FileIngestUploadEvent) bool {
			identity := event.GetIdentity()
			return event.CorrelationId == resumeCorrelationID &&
				identity.GetUploadId() == initResp.Msg.UploadId &&
				identity.GetFileId() == initResp.Msg.FileId &&
				event.GetProgress().GetPercentage() == 100
		})
		require.Equal(t, int32(100), uploadEvent.GetProgress().GetPercentage())
		require.Equal(t, fileSize, uploadEvent.GetProgress().GetBytesTotal())
		require.Equal(t, fileSize, uploadEvent.GetProgress().GetBytesCompleted())

		uploadedParts = loadUploadPartInfosForTest(t, stack.DB, initResp.Msg.UploadId)
		require.Len(t, uploadedParts, 10)
		for index, uploadedPart := range uploadedParts {
			require.Equal(t, int32(index+1), uploadedPart.PartNumber)
			require.NotEmpty(t, uploadedPart.Etag)
		}

		finalizedReceiver := newFileIngestSignalReceiver(t, stack.PostgresDSN)
		attachedReceiver := newFileIngestSignalReceiver(t, stack.PostgresDSN)
		completeReq := connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
			FileId:        initResp.Msg.FileId,
			UploadId:      initResp.Msg.UploadId,
			CorrelationId: runtimePtr(completionCorrelationID),
		})
		setAuthHeaders(completeReq.Header(), manager)

		completeResp, err := fileClient.CompleteMultipartUpload(context.Background(), completeReq)
		require.NoError(t, err)
		require.Equal(t, initResp.Msg.FileId, completeResp.Msg.FileId)

		matchesCompletedIdentity := func(identity *managev1.FileIngestIdentity) bool {
			return identity.GetUploadId() == initResp.Msg.UploadId &&
				identity.GetFileId() == initResp.Msg.FileId &&
				identity.GetEntityId() == trackID &&
				identity.GetEntityType() == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK
		}
		finalizedEvent := waitForFileIngestFinalizedEvent(t, finalizedReceiver, 10*time.Second, func(event *managev1.FileIngestFinalizedEvent) bool {
			return event.CorrelationId == completionCorrelationID && matchesCompletedIdentity(event.GetIdentity())
		})
		require.Equal(t, int32(100), finalizedEvent.GetProgress().GetPercentage())
		attachedEvent := waitForFileIngestAttachedEvent(t, attachedReceiver, 10*time.Second, func(event *managev1.FileIngestAttachedEvent) bool {
			return event.CorrelationId == completionCorrelationID && matchesCompletedIdentity(event.GetIdentity())
		})
		require.Equal(t, fileName, attachedEvent.FileName)
		require.Equal(t, audioMimeType, attachedEvent.MimeType)
		require.Equal(t, fileSize, attachedEvent.FileSize)
		require.Eventually(t, func() bool {
			var attachedTrack model.Track
			if err := stack.DB.First(&attachedTrack, "id = ?", trackID).Error; err != nil {
				return false
			}
			return runtimeDeref(attachedTrack.AudioOriginalFileID) == initResp.Msg.FileId
		}, 10*time.Second, 100*time.Millisecond)
		require.Eventually(t, func() bool {
			return countUploadSessions(t, stack.DB, initResp.Msg.UploadId) == 0
		}, 5*time.Second, 200*time.Millisecond)
		require.Equal(t, int64(1), countFilesByID(t, stack.DB, initResp.Msg.FileId))
	})

	t.Run("complete track upload with invalid etag fails and leaves no durable file", func(t *testing.T) {
		fileName := runtimeTestFileName("track-bad-complete.mp3")
		fileLastModified := time.Now().UnixMilli()

		initReq := connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
			UploadType:       managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
			EntityId:         trackID,
			EntityType:       runtimePtr(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK),
			FileSize:         int64(len(fixtureBytes)),
			MimeType:         audioMimeType,
			FileName:         fileName,
			FileLastModified: &fileLastModified,
		})
		setAuthHeaders(initReq.Header(), manager)

		initResp, err := fileClient.InitiateMultipartUpload(context.Background(), initReq)
		require.NoError(t, err)

		part := uploadMultipartPart(
			t,
			stack.BackendURL,
			manager,
			initResp.Msg.FileId,
			initResp.Msg.UploadId,
			1,
			fixtureBytes,
		)
		require.NotEmpty(t, part.ETag)

		abortMultipartInStorage(t, stack, runtimeMediaObjectKey(t, initResp.Msg.FileId, initResp.Msg.Extension), initResp.Msg.UploadId)

		completeReq := connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
			FileId:        initResp.Msg.FileId,
			UploadId:      initResp.Msg.UploadId,
			CorrelationId: runtimePtr(uuid.NewString()),
		})
		setAuthHeaders(completeReq.Header(), manager)

		_, err = fileClient.CompleteMultipartUpload(context.Background(), completeReq)
		require.Error(t, err)

		require.Eventually(t, func() bool {
			return countUploadSessions(t, stack.DB, initResp.Msg.UploadId) == 1
		}, 5*time.Second, 200*time.Millisecond)
		require.Equal(t, int64(0), countFilesByID(t, stack.DB, initResp.Msg.FileId))

		requireUploadSessionStatus(t, stack.DB, initResp.Msg.UploadId, managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_FAILED)
	})

	t.Run("complete track upload attach audio and wait for processing to finish", func(t *testing.T) {
		fileName := runtimeTestFileName("track-complete.mp3")
		fileLastModified := time.Now().UnixMilli()
		var currentTrack model.Track
		require.NoError(t, stack.DB.First(&currentTrack, "id = ?", trackID).Error)
		expectedCurrentFileID := runtimeDeref(currentTrack.AudioOriginalFileID)
		require.NotEmpty(t, expectedCurrentFileID)
		require.Equal(
			t,
			expectedCurrentFileID,
			testutil.ReadReleaseTrackOriginalFileID(t, stack.DB, releaseID, trackID),
			"Release shared Yjs and Track row must start from the same CAS value",
		)

		initReq := connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
			UploadType:            managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
			EntityId:              trackID,
			EntityType:            runtimePtr(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK),
			FileSize:              int64(len(fixtureBytes)),
			MimeType:              audioMimeType,
			FileName:              fileName,
			FileLastModified:      &fileLastModified,
			ExpectedCurrentFileId: &expectedCurrentFileID,
		})
		setAuthHeaders(initReq.Header(), manager)

		initResp, err := fileClient.InitiateMultipartUpload(context.Background(), initReq)
		require.NoError(t, err)

		part := uploadMultipartPart(
			t,
			stack.BackendURL,
			manager,
			initResp.Msg.FileId,
			initResp.Msg.UploadId,
			1,
			fixtureBytes,
		)
		require.NotEmpty(t, part.ETag)

		completeReq := connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
			FileId:        initResp.Msg.FileId,
			UploadId:      initResp.Msg.UploadId,
			CorrelationId: runtimePtr(uuid.NewString()),
		})
		setAuthHeaders(completeReq.Header(), manager)

		completeResp, err := fileClient.CompleteMultipartUpload(context.Background(), completeReq)
		require.NoError(t, err)
		require.Equal(t, initResp.Msg.FileId, completeResp.Msg.FileId)
		stored := requireRuntimeCanonicalFileRecord(t, stack.DB, initResp.Msg.FileId, initResp.Msg.Extension, audioMimeType, fixtureBytes)
		require.Empty(t, stored.SHA256)

		var track model.Track
		require.Eventually(t, func() bool {
			if err := stack.DB.First(&track, "id = ?", trackID).Error; err != nil {
				return false
			}
			return runtimeDeref(track.AudioOriginalFileID) == initResp.Msg.FileId &&
				runtimeDeref(track.ProcessingStatus) == managev1.TrackProcessingStatus_TRACK_PROCESSING_STATUS_PROCESSING.String()
		}, 10*time.Second, 100*time.Millisecond)
		require.Equal(
			t,
			initResp.Msg.FileId,
			testutil.ReadReleaseTrackOriginalFileID(t, stack.DB, releaseID, trackID),
			"editor-collab must persist the projected Track file in Release shared Yjs before the API finalizer updates Track",
		)

		waitForRuntimeMediaDelivery(t, fileClient, manager, initResp.Msg.FileId, func(delivery *commonv1.MediaDelivery) bool {
			return delivery.GetProcessingStatus() == commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY &&
				delivery.GetPlayback().GetUrl() != "" &&
				delivery.GetSpectrogram().GetUrl() != "" &&
				delivery.GetWaveform().GetUrl() != ""
		})

		require.NoError(t, stack.DB.First(&track, "id = ?", trackID).Error)
		require.Equal(t, initResp.Msg.FileId, runtimeDeref(track.AudioOriginalFileID))
		require.Equal(t, managev1.TrackProcessingStatus_TRACK_PROCESSING_STATUS_COMPLETED.String(), runtimeDeref(track.ProcessingStatus))

		deleteRuntimeTrackAndAssertFilePreserved(t, stack, trackClient, manager, trackID, initResp.Msg.FileId)
	})
}
func uploadMultipartPart(
	t *testing.T,
	baseURL string,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	partNumber int,
	body []byte,
) uploadPartResponse {
	t.Helper()
	return uploadMultipartPartWithCorrelation(t, baseURL, user, fileID, uploadID, partNumber, body, "")
}

func uploadMultipartBody(
	t *testing.T,
	baseURL string,
	user *testutil.OryUser,
	session *managev1.InitiateMultipartUploadResponse,
	body []byte,
) {
	t.Helper()

	chunkSize := int(session.GetChunkSize())
	require.Positive(t, chunkSize)
	totalParts := int(session.GetTotalParts())
	require.Positive(t, totalParts)
	require.Equal(t, expectedUploadPartCount(len(body), chunkSize), totalParts)

	for partNumber := 1; partNumber <= totalParts; partNumber++ {
		partBody := multipartPartBody(t, session, body, partNumber)
		response := uploadMultipartPart(
			t,
			baseURL,
			user,
			session.GetFileId(),
			session.GetUploadId(),
			partNumber,
			partBody,
		)
		require.NotEmpty(t, response.ETag)
	}
}

func multipartPartBody(
	t *testing.T,
	session *managev1.InitiateMultipartUploadResponse,
	body []byte,
	partNumber int,
) []byte {
	t.Helper()

	chunkSize := int(session.GetChunkSize())
	require.Positive(t, chunkSize)
	totalParts := int(session.GetTotalParts())
	require.Positive(t, totalParts)
	require.GreaterOrEqual(t, partNumber, 1)
	require.LessOrEqual(t, partNumber, totalParts)
	require.Equal(t, expectedUploadPartCount(len(body), chunkSize), totalParts)

	start := (partNumber - 1) * chunkSize
	require.Less(t, start, len(body))
	end := start + chunkSize
	if end > len(body) {
		end = len(body)
	}
	return body[start:end]
}

func expectedUploadPartCount(bodySize int, chunkSize int) int {
	if bodySize <= 0 {
		return 1
	}
	return (bodySize + chunkSize - 1) / chunkSize
}

func waitForRuntimeMediaDelivery(
	t *testing.T,
	fileClient managev1connect.FileServiceClient,
	user *testutil.OryUser,
	fileID string,
	ready func(*commonv1.MediaDelivery) bool,
) *commonv1.MediaDelivery {
	t.Helper()

	var last *commonv1.MediaDelivery
	testutil.WaitForFileProcessingComplete(t, 3*time.Minute, func() (bool, string, error) {
		getReq := connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID})
		setAuthHeaders(getReq.Header(), user)

		getResp, err := fileClient.GetMediaDelivery(context.Background(), getReq)
		if err != nil {
			return false, "", err
		}
		last = getResp.Msg.GetDelivery()
		if last == nil {
			return false, "delivery is nil", nil
		}

		msg := fmt.Sprintf(
			"processing status=%s percentage=%v playback=%q thumbnail=%q spectrogram=%q waveform=%q",
			last.GetProcessingStatus().String(),
			last.ProcessingPercentage,
			last.GetPlayback().GetUrl(),
			last.GetThumbnail().GetUrl(),
			last.GetSpectrogram().GetUrl(),
			last.GetWaveform().GetUrl(),
		)

		return ready(last), msg, nil
	})
	require.NotNil(t, last)
	return last
}

func completeRuntimeEditorMediaUploadAndWait(
	t *testing.T,
	stack *testutil.RuntimeStack,
	fileClient managev1connect.FileServiceClient,
	manager *testutil.OryUser,
	uploadType managev1.UploadType,
	mimeType string,
	fileName string,
	body []byte,
	ready func(*commonv1.MediaDelivery) bool,
) (string, *commonv1.MediaDelivery) {
	t.Helper()

	fileLastModified := time.Now().UnixMilli()
	initReq := connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
		UploadType:       uploadType,
		FileSize:         int64(len(body)),
		MimeType:         mimeType,
		FileName:         fileName,
		FileLastModified: &fileLastModified,
	})
	setAuthHeaders(initReq.Header(), manager)

	initResp, err := fileClient.InitiateMultipartUpload(context.Background(), initReq)
	require.NoError(t, err)

	uploadMultipartBody(t, stack.BackendURL, manager, initResp.Msg, body)
	completeReq := connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		FileId:        initResp.Msg.FileId,
		UploadId:      initResp.Msg.UploadId,
		CorrelationId: runtimePtr(uuid.NewString()),
	})
	setAuthHeaders(completeReq.Header(), manager)

	completeResp, err := fileClient.CompleteMultipartUpload(context.Background(), completeReq)
	require.NoError(t, err)
	require.Equal(t, initResp.Msg.FileId, completeResp.Msg.FileId)
	require.Equal(t, initResp.Msg.Extension, completeResp.Msg.GetDelivery().GetExtension())
	requireCanonicalInlineRef(t, completeResp.Msg.GetDelivery().GetInline(), initResp.Msg.FileId, initResp.Msg.Extension, mimeType)
	requireCanonicalDownloadRef(t, completeResp.Msg.GetDelivery().GetDownload(), initResp.Msg.FileId, initResp.Msg.Extension, mimeType)
	stored := requireRuntimeCanonicalFileRecord(t, stack.DB, initResp.Msg.FileId, initResp.Msg.Extension, mimeType, body)
	require.Empty(t, stored.SHA256)

	delivery := waitForRuntimeMediaDelivery(t, fileClient, manager, initResp.Msg.FileId, ready)
	return initResp.Msg.FileId, delivery
}

type runtimeFileStorageRefs struct {
	originalKey        string
	assetKeys          []string
	generationPrefixes []string
}

func deleteRuntimeTrackAndAssertFilePreserved(
	t *testing.T,
	stack *testutil.RuntimeStack,
	trackClient managev1connect.TrackServiceClient,
	manager *testutil.OryUser,
	trackID string,
	fileID string,
) {
	t.Helper()

	storageRefs := loadRuntimeFileStorageRefs(t, stack.DB, fileID)
	require.NotEmpty(t, storageRefs.originalKey)

	deleteReq := connect.NewRequest(&managev1.DeleteTrackRequest{Id: trackID})
	setAuthHeaders(deleteReq.Header(), manager)
	deleteResp, err := trackClient.DeleteTrack(context.Background(), deleteReq)
	require.NoError(t, err)
	require.True(t, deleteResp.Msg.Success)

	require.Eventually(t, func() bool {
		return countTracksByID(t, stack.DB, trackID) == 0
	}, 5*time.Second, 200*time.Millisecond)
	require.EqualValues(t, 1, countFilesByID(t, stack.DB, fileID))
	require.Positive(t, countFileDerivatives(t, stack.DB, fileID))

	s3Client := runtimeS3Client(t, stack)
	require.True(t, runtimeS3ObjectExists(t, s3Client, stack.S3MediaBucket, storageRefs.originalKey))
	for _, key := range storageRefs.assetKeys {
		require.True(t, runtimeS3ObjectExists(t, s3Client, stack.S3MediaBucket, key))
	}
	for _, prefix := range storageRefs.generationPrefixes {
		require.Eventually(t, func() bool {
			return runtimeS3PrefixHasObjects(t, s3Client, stack.S3MediaBucket, prefix)
		}, 5*time.Second, 200*time.Millisecond)
	}
}

func uploadMultipartPartWithCorrelation(
	t *testing.T,
	baseURL string,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	partNumber int,
	body []byte,
	correlationID string,
) uploadPartResponse {
	t.Helper()

	query := url.Values{}
	query.Set("fileId", fileID)
	query.Set("uploadId", uploadID)
	query.Set("partNumber", fmt.Sprintf("%d", partNumber))
	if correlationID != "" {
		query.Set("correlationId", correlationID)
	}
	client := &http.Client{Timeout: 30 * time.Second}

	if partNumber == 1 {
		prefixQuery := url.Values{}
		prefixQuery.Set("fileId", fileID)
		prefixQuery.Set("uploadId", uploadID)
		if correlationID != "" {
			prefixQuery.Set("correlationId", correlationID)
		}
		prefixSize := min(len(body), multipartSniffBytes)
		prefixReq, err := http.NewRequest(
			http.MethodPost,
			fmt.Sprintf("%s/upload/prefix?%s", baseURL, prefixQuery.Encode()),
			bytes.NewReader(body[:prefixSize]),
		)
		require.NoError(t, err)
		setAuthHeaders(prefixReq.Header, user)
		prefixReq.ContentLength = int64(prefixSize)
		prefixResp, err := client.Do(prefixReq)
		require.NoError(t, err)
		defer prefixResp.Body.Close()
		prefixResponseBody, err := io.ReadAll(prefixResp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusNoContent, prefixResp.StatusCode, string(prefixResponseBody))
	}

	presignReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/upload/part/presign?%s", baseURL, query.Encode()), nil)
	require.NoError(t, err)
	testutil.ApplyAuthHeaders(presignReq.Header, user)
	presignResp, err := client.Do(presignReq)
	require.NoError(t, err)
	defer presignResp.Body.Close()
	require.Equal(t, http.StatusOK, presignResp.StatusCode)
	var presigned multipartPresignResponse
	require.NoError(t, json.NewDecoder(presignResp.Body).Decode(&presigned))
	require.NotEmpty(t, presigned.URL)

	putReq, err := http.NewRequest(http.MethodPut, presigned.URL, bytes.NewReader(body))
	require.NoError(t, err)
	putReq.ContentLength = int64(len(body))
	putResp, err := client.Do(putReq)
	require.NoError(t, err)
	defer putResp.Body.Close()
	putResponseBody, err := io.ReadAll(putResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, putResp.StatusCode, string(putResponseBody))

	confirmReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/upload/part/confirm?%s", baseURL, query.Encode()), nil)
	require.NoError(t, err)
	testutil.ApplyAuthHeaders(confirmReq.Header, user)
	confirmResp, err := client.Do(confirmReq)
	require.NoError(t, err)
	defer confirmResp.Body.Close()
	require.Equal(t, http.StatusOK, confirmResp.StatusCode)

	var payload uploadPartResponse
	require.NoError(t, json.NewDecoder(confirmResp.Body).Decode(&payload))
	require.NotEmpty(t, payload.ETag)
	return payload
}

func requestMultipartPartPresign(
	t *testing.T,
	baseURL string,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	partNumber int,
	correlationID string,
) multipartPresignResponse {
	t.Helper()
	query := url.Values{}
	query.Set("fileId", fileID)
	query.Set("uploadId", uploadID)
	query.Set("partNumber", fmt.Sprintf("%d", partNumber))
	if correlationID != "" {
		query.Set("correlationId", correlationID)
	}
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/upload/part/presign?%s", baseURL, query.Encode()), nil)
	require.NoError(t, err)
	testutil.ApplyAuthHeaders(req.Header, user)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var payload multipartPresignResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	return payload
}

func newFileIngestSignalReceiver(t *testing.T, dsn string) *fileIngestSignalReceiver {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), dsn)
	require.NoError(t, err)
	_, err = conn.Exec(t.Context(), `LISTEN "file.ingest"`)
	require.NoError(t, err)
	receiver := &fileIngestSignalReceiver{conn: conn}
	t.Cleanup(func() {
		_ = receiver.conn.Close(context.Background())
	})
	return receiver
}

func waitForFileIngestProtoEvent[T proto.Message](
	t *testing.T,
	receiver *fileIngestSignalReceiver,
	timeout time.Duration,
	newEvent func() T,
	match func(T) bool,
) T {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	wantedType := string(newEvent().ProtoReflect().Descriptor().FullName())

	for {
		notification, err := receiver.conn.WaitForNotification(ctx)
		if err != nil {
			t.Fatalf("timed out waiting for matching file ingest event: %v", err)
			var zero T
			return zero
		}
		var envelope eventpkg.Envelope
		require.NoError(t, json.Unmarshal([]byte(notification.Payload), &envelope))
		if envelope.MessageType != wantedType {
			continue
		}
		body, err := envelope.Payload()
		require.NoError(t, err)
		event := newEvent()
		require.NoError(t, proto.Unmarshal(body, event))
		if match(event) {
			return event
		}
	}
}

func waitForFileIngestUploadEvent(
	t *testing.T,
	receiver *fileIngestSignalReceiver,
	timeout time.Duration,
	match func(*managev1.FileIngestUploadEvent) bool,
) *managev1.FileIngestUploadEvent {
	t.Helper()
	return waitForFileIngestProtoEvent(
		t,
		receiver,
		timeout,
		func() *managev1.FileIngestUploadEvent { return &managev1.FileIngestUploadEvent{} },
		match,
	)
}

func waitForFileIngestFinalizedEvent(
	t *testing.T,
	receiver *fileIngestSignalReceiver,
	timeout time.Duration,
	match func(*managev1.FileIngestFinalizedEvent) bool,
) *managev1.FileIngestFinalizedEvent {
	t.Helper()
	return waitForFileIngestProtoEvent(
		t,
		receiver,
		timeout,
		func() *managev1.FileIngestFinalizedEvent { return &managev1.FileIngestFinalizedEvent{} },
		match,
	)
}

func waitForFileIngestAttachedEvent(
	t *testing.T,
	receiver *fileIngestSignalReceiver,
	timeout time.Duration,
	match func(*managev1.FileIngestAttachedEvent) bool,
) *managev1.FileIngestAttachedEvent {
	t.Helper()
	return waitForFileIngestProtoEvent(
		t,
		receiver,
		timeout,
		func() *managev1.FileIngestAttachedEvent { return &managev1.FileIngestAttachedEvent{} },
		match,
	)
}

func requireNoFileIngestFailedEvent(
	t *testing.T,
	receiver *fileIngestSignalReceiver,
	duration time.Duration,
	match func(*managev1.FileIngestFailedEvent) bool,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), duration)
	defer cancel()
	wantedType := string((&managev1.FileIngestFailedEvent{}).ProtoReflect().Descriptor().FullName())

	for {
		notification, err := receiver.conn.WaitForNotification(ctx)
		if errors.Is(err, context.DeadlineExceeded) {
			return
		}
		require.NoError(t, err)
		var envelope eventpkg.Envelope
		require.NoError(t, json.Unmarshal([]byte(notification.Payload), &envelope))
		if envelope.MessageType != wantedType {
			continue
		}
		body, err := envelope.Payload()
		require.NoError(t, err)
		event := &managev1.FileIngestFailedEvent{}
		require.NoError(t, proto.Unmarshal(body, event))
		require.Falsef(t, match(event), "unexpected file ingest failed event: %v", event)
	}
}

func setAuthHeaders(header http.Header, user *testutil.OryUser) {
	testutil.ApplyAuthHeaders(header, user)
}

func runtimePtr[T interface{}](value T) *T {
	return &value
}

func runtimeTestFileName(base string) string {
	return uuid.NewString() + "-" + base
}

func runtimeDeref[T interface{}](value *T) T {
	var zero T
	if value == nil {
		return zero
	}
	return *value
}

func countUploadSessions(t *testing.T, db *gorm.DB, uploadID string) int64 {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("upload_session").Where("upload_id = ?", uploadID).Count(&count).Error)
	return count
}

func countUploadParts(t *testing.T, db *gorm.DB, uploadID string) int64 {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("upload_part").Where("upload_id = ?", uploadID).Count(&count).Error)
	return count
}

func loadUploadPartInfosForTest(t *testing.T, db *gorm.DB, uploadID string) []*managev1.UploadPartInfo {
	t.Helper()

	var parts []model.UploadPart
	require.NoError(t, db.Where("upload_id = ?", uploadID).Order("part_number ASC").Find(&parts).Error)
	return uploadPartInfos(parts)
}

func requireUploadSessionStatus(t *testing.T, db *gorm.DB, uploadID string, expected managev1.UploadSessionStatus) {
	t.Helper()

	var session model.UploadSession
	require.NoError(t, db.Where("upload_id = ?", uploadID).First(&session).Error)
	require.Equal(t, expected, uploadSessionStatusToProto(session.Status))
}

func countFilesByID(t *testing.T, db *gorm.DB, fileID string) int64 {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("file").Where("id = ?", fileID).Count(&count).Error)
	return count
}

func requireRuntimeCanonicalFileRecord(
	t *testing.T,
	db *gorm.DB,
	fileID string,
	extension string,
	mimeType string,
	body []byte,
) model.File {
	t.Helper()

	var file model.File
	require.NoError(t, db.Where("id = ?", fileID).Take(&file).Error)
	require.Equal(t, extension, file.Extension)
	require.Equal(t, mimeType, file.MimeType)
	require.EqualValues(t, len(body), file.FileSize)
	return file
}

func countTracksByID(t *testing.T, db *gorm.DB, trackID string) int64 {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("track").Where("id = ?", trackID).Count(&count).Error)
	return count
}

func countFileDerivatives(t *testing.T, db *gorm.DB, fileID string) int64 {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("file_derivative").Where("file_id = ?", fileID).Count(&count).Error)
	return count
}

func abortMultipartInStorage(
	t *testing.T,
	stack *testutil.RuntimeStack,
	fileKey string,
	uploadID string,
) {
	t.Helper()

	s3Client := runtimeS3Client(t, stack)

	_, err := s3Client.AbortMultipartUpload(context.Background(), &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(stack.S3MediaBucket),
		Key:      aws.String(fileKey),
		UploadId: aws.String(uploadID),
	})
	require.NoError(t, err)
}

func loadRuntimeFileStorageRefs(t *testing.T, db *gorm.DB, fileID string) runtimeFileStorageRefs {
	t.Helper()

	var file struct {
		Extension string `gorm:"column:extension"`
	}
	require.NoError(t, db.Table("file").Select("extension").Where("id = ?", fileID).First(&file).Error)
	originalKey := runtimeMediaObjectKey(t, fileID, file.Extension)

	var derivatives []struct {
		Type                   string  `gorm:"column:type"`
		AssetObjectKey         *string `gorm:"column:asset_object_key"`
		GenerationObjectPrefix *string `gorm:"column:generation_object_prefix"`
	}
	require.NoError(t, db.Table("file_derivative fd").
		Select("fd.type, pa.object_key AS asset_object_key, mg.object_prefix AS generation_object_prefix").
		Joins("LEFT JOIN public_asset pa ON pa.id = fd.asset_id").
		Joins("LEFT JOIN media_generation mg ON mg.id = fd.media_generation_id").
		Where("fd.file_id = ?", fileID).
		Find(&derivatives).Error)

	refs := runtimeFileStorageRefs{originalKey: originalKey}
	for _, derivative := range derivatives {
		if derivative.Type == managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String() {
			require.NotNil(t, derivative.GenerationObjectPrefix)
			require.Nil(t, derivative.AssetObjectKey)
			refs.generationPrefixes = append(refs.generationPrefixes, *derivative.GenerationObjectPrefix)
			continue
		}
		require.NotNil(t, derivative.AssetObjectKey)
		require.Nil(t, derivative.GenerationObjectPrefix)
		refs.assetKeys = append(refs.assetKeys, *derivative.AssetObjectKey)
	}
	return refs
}

func runtimeMediaObjectKey(t *testing.T, fileID string, extension string) string {
	t.Helper()
	key, err := mediaauth.MediaObjectKey(fileID, extension)
	require.NoError(t, err)
	return key
}

func runtimeS3Client(t *testing.T, stack *testutil.RuntimeStack) *s3.Client {
	t.Helper()

	awsCfg, err := awsconfig.LoadDefaultConfig(
		context.Background(),
		awsconfig.WithRegion(stack.S3Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(stack.S3AccessKeyID, stack.S3SecretAccessKey, ""),
		),
	)
	require.NoError(t, err)

	s3Client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(stack.S3Endpoint)
		o.UsePathStyle = stack.S3ForcePathStyle
	})
	return s3Client
}

func runtimeS3ObjectExists(t *testing.T, s3Client *s3.Client, bucket string, key string) bool {
	t.Helper()

	_, err := s3Client.HeadObject(context.Background(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return true
	}
	if isRuntimeS3NotFound(err) {
		return false
	}
	require.NoError(t, err)
	return false
}

func runtimeS3PrefixHasObjects(t *testing.T, s3Client *s3.Client, bucket string, prefix string) bool {
	t.Helper()

	out, err := s3Client.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(1),
	})
	require.NoError(t, err)
	return len(out.Contents) > 0
}

func isRuntimeS3NotFound(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "NotFound", "NoSuchKey", "404":
		return true
	default:
		return false
	}
}
