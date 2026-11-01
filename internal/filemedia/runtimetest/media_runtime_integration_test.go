//go:build integration

package runtimetest

import (
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/model"

	"github.com/echovisionlab/geul-api/internal/testutil/runtimetest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestRuntimeEditorFileUploadIsIndependentAndFileScoped(t *testing.T) {
	t.Parallel()
	stack := runtimetest.SetupSharedRuntimeStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Author().ID())
	fileClient := managev1connect.NewFileServiceClient(&http.Client{Timeout: 30 * time.Second}, stack.BackendURL)
	// The direct browser image path normalizes pixels to WebP before upload.
	imagePath := filepath.Join(t.TempDir(), "normalized.webp")
	runRuntimeClientMediaCommand(t, "cwebp", "-q", "90", runtimetest.RepositoryTestImageJPEG(t), "-o", imagePath)
	imageBytes, err := os.ReadFile(imagePath)
	require.NoError(t, err)
	fileID, delivery := completeRuntimeEditorMediaUploadAndWait(
		t,
		stack,
		fileClient,
		manager,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		"image/webp",
		runtimeTestFileName("independent-editor-image.webp"),
		imageBytes,
		func(delivery *commonv1.MediaDelivery) bool {
			return delivery.GetProcessingStatus() == commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY && delivery.GetAsset().GetUrl() != ""
		},
	)
	require.NotEmpty(t, fileID)
	require.NotEmpty(t, delivery.GetAsset().GetUrl())
	bulkRequest := connect.NewRequest(&managev1.GetBulkMediaDeliveriesRequest{FileIds: []string{fileID}})
	setAuthHeaders(bulkRequest.Header(), manager)
	bulkResponse, err := fileClient.GetBulkMediaDeliveries(t.Context(), bulkRequest)
	require.NoError(t, err)
	require.Contains(t, bulkResponse.Msg.Files, fileID)
	require.True(t, proto.Equal(delivery.GetAsset(), bulkResponse.Msg.Files[fileID].GetDelivery().GetAsset()), "single and bulk readers must expose the same ready image Asset")
	require.Equal(t, imageBytes, getRuntimePublicMediaBytes(t, delivery.GetAsset().GetUrl()))
	var asset model.PublicAsset
	require.NoError(t, stack.DB.Where("id = ?", delivery.GetAsset().GetAssetId()).Take(&asset).Error)
	expectedSHA := sha256.Sum256(imageBytes)
	require.Equal(t, expectedSHA[:], asset.SHA256)
	require.Equal(t, model.PublicAssetStatusReady, asset.Status)
	require.Equal(t, "image/webp", asset.MimeType)
	require.NotNil(t, asset.SourceFileID)
	require.Equal(t, fileID, *asset.SourceFileID)
	var binding model.FileIngestBinding
	require.NoError(t, stack.DB.Where("file_id = ?", fileID).Take(&binding).Error)
	require.Equal(t, managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String(), binding.UploadType)
	require.Empty(t, binding.EntityID)
	require.Nil(t, binding.EntityType)
	var usageCount int64
	require.NoError(t, stack.DB.Table("content_block_attachment").Where("file_id = ?", fileID).Count(&usageCount).Error)
	require.Zero(t, usageCount, "upload completion must not attach the independent File to a document")
}

func TestRuntimeTrackAudioUploadAPIFlows(t *testing.T) {
	t.Parallel()
	stack := runtimetest.SetupRuntimeStack(t)
	stack.BackendReadTimeoutSec = 1
	stack.StartBackend(t)

	manager := stack.CreateUser(t, policyv1.Role.Admin().ID())
	staleManager := stack.CreateUser(t, policyv1.Role.Author().ID())
	outsider := stack.CreateUser(t, policyv1.Role.User().ID())
	releaseID := runtimetest.CreateReleaseViaAPI(t, stack.BackendURL, manager)

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
	runtimetest.AssertManagedReleaseTrackAuthority(t, stack.DB, releaseID, trackID)

	fixturePath := runtimetest.RepositoryTestAudioMP3(t)
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

		completeReq := connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
			FileId: initResp.Msg.FileId, UploadId: initResp.Msg.UploadId, CorrelationId: runtimePtr(completionCorrelationID),
		})
		setAuthHeaders(completeReq.Header(), manager)
		_, err = fileClient.CompleteMultipartUpload(context.Background(), completeReq)
		require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		require.Contains(t, err.Error(), "clientMediaBundleRequired")
		require.EqualValues(t, 0, countFilesByID(t, stack.DB, initResp.Msg.FileId))
		requireUploadSessionStatus(t, stack.DB, initResp.Msg.UploadId, model.UploadSessionStatusUploading)
		var untouchedTrack model.Track
		require.NoError(t, stack.DB.First(&untouchedTrack, "id = ?", trackID).Error)
		require.Nil(t, untouchedTrack.AudioOriginalFileID)

	})

	t.Run("unprepared track completion rejects before storage completion", func(t *testing.T) {
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
		require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		require.Contains(t, err.Error(), "clientMediaBundleRequired")

		require.Eventually(t, func() bool {
			return countUploadSessions(t, stack.DB, initResp.Msg.UploadId) == 1
		}, 5*time.Second, 200*time.Millisecond)
		require.Equal(t, int64(0), countFilesByID(t, stack.DB, initResp.Msg.FileId))

		requireUploadSessionStatus(t, stack.DB, initResp.Msg.UploadId, model.UploadSessionStatusUploading)
	})

}

func TestRuntimeIntegratedOGGenerationThroughAuthenticatedAPI(t *testing.T) {
	t.Parallel()
	stack := runtimetest.SetupSharedRuntimeStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	client := managev1connect.NewAdminServiceClient(http.DefaultClient, stack.BackendURL)
	request := connect.NewRequest(&managev1.RegenerateOgImageRequest{
		EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_SITE,
		Selection:  &managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_Primary{Primary: &managev1.OgPrimaryTarget{}}},
	})
	// The integrated runtime must retain the existing administrative boundary.
	_, err := client.RegenerateOgImage(t.Context(), request)
	require.Error(t, err)
	setAuthHeaders(request.Header(), admin)
	response, err := client.RegenerateOgImage(t.Context(), request)
	require.NoError(t, err)
	require.Len(t, response.Msg.GenerationIds, 1)
	var generation model.OgGeneration
	require.Eventually(t, func() bool {
		err := stack.DB.First(&generation, "id = ?", response.Msg.GenerationIds[0]).Error
		return err == nil && (generation.Status == model.OgGenerationStatusReady || generation.Status == model.OgGenerationStatusFailed)
	}, 60*time.Second, 100*time.Millisecond)
	require.Equal(t, model.OgGenerationStatusReady, generation.Status, "failure code: %v", generation.LastErrorCode)
	imageURL := stack.CDNURL + "/asset/" + generation.ID + "/image.webp"
	res, err := http.Get(imageURL)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Greater(t, len(body), 12)
	require.Equal(t, "RIFF", string(body[:4]))
	require.Equal(t, "WEBP", string(body[8:12]))
}

func TestRuntimeClientVideoAndMeshOptimization(t *testing.T) {
	t.Parallel()
	stack := runtimetest.SetupSharedRuntimeStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	client := managev1connect.NewFileServiceClient(&http.Client{Timeout: 30 * time.Second}, stack.BackendURL)
	t.Run("video", func(t *testing.T) {
		body, err := os.ReadFile(runtimetest.RepositoryTestVideoMP4(t))
		require.NoError(t, err)
		_, delivery := completeRuntimeEditorMediaUploadAndWait(t, stack, client, admin, managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO, "video/mp4", runtimeTestFileName("integrated-video.mp4"), body, func(d *commonv1.MediaDelivery) bool {
			return d.GetProcessingStatus() == commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY && d.GetPlayback().GetUrl() != ""
		})
		require.NotEmpty(t, delivery.GetThumbnail().GetUrl())
		playlist := getRuntimePublicMediaBytes(t, delivery.GetPlayback().GetUrl())
		require.Contains(t, string(playlist), "#EXTM3U")
		// ffprobe follows the same public playlist and confirms actual video/audio
		// streams; a READY row or syntactically plausible playlist is insufficient.
		streams := runRuntimeClientMediaCommand(t, "ffprobe", "-v", "error", "-show_entries", "stream=codec_type", "-of", "csv=p=0", delivery.GetPlayback().GetUrl())
		require.Contains(t, string(streams), "video")
		require.Contains(t, string(streams), "audio")
		require.True(t, strings.Contains(string(playlist), ".ts"), "client playlist must reference MPEG-TS media")
	})
	t.Run("mesh", func(t *testing.T) {
		body, err := os.ReadFile(runtimetest.RepositoryTestMeshGLB(t))
		require.NoError(t, err)
		fileID, _ := completeRuntimeEditorMediaUploadAndWait(t, stack, client, admin, managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH, "model/gltf-binary", runtimeTestFileName("integrated-mesh.glb"), body, func(d *commonv1.MediaDelivery) bool {
			return d.GetProcessingStatus() == commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY
		})
		pageID := seedHardCutPageFixture(t, stack.DB)
		attachInternalResourcePolicy(t, stack.SpiceDBClient, pageID)
		seedMeshOptimizationPageFileLink(t, stack.DB, pageID, fileID)
		request := connect.NewRequest(&managev1.GenerateMeshOptimizationCandidateRequest{SourceFileId: fileID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE, EntityId: pageID, TargetRatioPercent: 100})
		setAuthHeaders(request.Header(), admin)
		response, err := client.GenerateMeshOptimizationCandidate(t.Context(), request)
		require.NoError(t, err)
		require.True(t, response.Msg.Enqueued)
		var candidate model.MeshOptimizationCandidate
		require.Eventually(t, func() bool {
			err := stack.DB.First(&candidate, "id = ?", response.Msg.Candidate.Id).Error
			return err == nil && (candidate.Status == model.MeshOptimizationCandidateStatusReady || candidate.Status == model.MeshOptimizationCandidateStatusFailed)
		}, 60*time.Second, 100*time.Millisecond)
		require.Equal(t, model.MeshOptimizationCandidateStatusReady, candidate.Status)
		require.NotNil(t, candidate.OutputFileID)
		var output model.File
		require.NoError(t, stack.DB.First(&output, "id = ?", *candidate.OutputFileID).Error)
		require.Greater(t, output.FileSize, int64(0))
	})
}

// The CDN process serves the stored GLB with the File's MIME type to the Web
// origin.
func TestRuntimeEditorMeshUploadServesGLBThroughCDN(t *testing.T) {
	t.Parallel()
	stack := runtimetest.SetupSharedRuntimeStackWithCDN(t)
	manager := stack.CreateUser(t, policyv1.Role.Author().ID())
	fileClient := managev1connect.NewFileServiceClient(&http.Client{Timeout: 30 * time.Second}, stack.BackendURL)

	meshBody, err := os.ReadFile(runtimetest.RepositoryTestMeshGLB(t))
	require.NoError(t, err)
	require.NotEmpty(t, meshBody)

	fileName := runtimeTestFileName("editor-mesh.glb")
	lastModified := time.Now().UnixMilli()
	initReq := connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
		UploadType:       managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH,
		FileName:         fileName,
		FileSize:         int64(len(meshBody)),
		MimeType:         "model/gltf-binary",
		FileLastModified: &lastModified,
	})
	setAuthHeaders(initReq.Header(), manager)
	initResp, err := fileClient.InitiateMultipartUpload(t.Context(), initReq)
	require.NoError(t, err)
	require.NotEmpty(t, initResp.Msg.GetUploadId())
	require.NotEmpty(t, initResp.Msg.GetFileId())
	require.Equal(t, "glb", initResp.Msg.GetExtension())
	fileKey := runtimeMediaObjectKey(t, initResp.Msg.GetFileId(), initResp.Msg.GetExtension())
	require.True(t, strings.HasSuffix(fileKey, ".glb"))

	uploadMultipartBody(t, stack.BackendURL, manager, initResp.Msg, meshBody)

	completeReq := connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		FileId:   initResp.Msg.GetFileId(),
		UploadId: initResp.Msg.GetUploadId(),
	})
	setAuthHeaders(completeReq.Header(), manager)
	completeResp, err := fileClient.CompleteMultipartUpload(t.Context(), completeReq)
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

	stored := requireRuntimeCanonicalFileRecord(t, stack.DB, initResp.Msg.GetFileId(), "glb", "model/gltf-binary", meshBody)
	require.Equal(t, strings.TrimSuffix(fileName, ".glb"), stored.FileName)
	require.Empty(t, stored.SHA256)
	require.Nil(t, stored.IngestSlotID)

	headResp, err := runtimeS3Client(t, stack).HeadObject(t.Context(), &s3.HeadObjectInput{
		Bucket: aws.String(stack.S3MediaBucket),
		Key:    aws.String(fileKey),
	})
	require.NoError(t, err)
	require.Equal(t, "model/gltf-binary", aws.ToString(headResp.ContentType))

	cdnReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, downloadURL, nil)
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
