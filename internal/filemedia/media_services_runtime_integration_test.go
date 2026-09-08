//go:build integration

package filemedia

import (
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func TestRuntimeIntegratedOGGenerationThroughAuthenticatedAPI(t *testing.T) {
	stack := testutil.SetupSharedRuntimeStack(t)
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

func TestRuntimeIntegratedVideoAndMeshWorkers(t *testing.T) {
	stack := testutil.SetupSharedRuntimeStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	client := managev1connect.NewFileServiceClient(&http.Client{Timeout: 30 * time.Second}, stack.BackendURL)
	t.Run("video", func(t *testing.T) {
		body, err := os.ReadFile(testutil.RepositoryTestVideoMP4(t))
		require.NoError(t, err)
		_, delivery := completeRuntimeEditorMediaUploadAndWait(t, stack, client, admin, managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO, "video/mp4", runtimeTestFileName("integrated-video.mp4"), body, func(d *commonv1.MediaDelivery) bool {
			return d.GetProcessingStatus() == commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY && d.GetPlayback().GetUrl() != ""
		})
		res, err := http.Get(delivery.GetPlayback().GetUrl())
		require.NoError(t, err)
		defer res.Body.Close()
		require.Equal(t, http.StatusOK, res.StatusCode)
		playlist, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		require.Contains(t, string(playlist), "#EXTM3U")
	})
	t.Run("mesh", func(t *testing.T) {
		body, err := os.ReadFile(testutil.RepositoryTestMeshGLB(t))
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
