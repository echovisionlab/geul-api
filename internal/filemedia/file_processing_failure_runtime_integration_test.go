//go:build integration

package filemedia

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/transcode"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type runtimeEditorProcessingFailureCase struct {
	name       string
	blockType  testutil.EditorMediaBlockType
	uploadType managev1.UploadType
	mimeType   string
}

func TestRuntimeEditorMediaProcessingFailureIntegration(t *testing.T) {
	testutil.RequireFFmpeg(t)
	stack := testutil.SetupSharedRuntimeStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())

	for _, tc := range runtimeEditorProcessingFailureCases(true) {
		t.Run(tc.name, func(t *testing.T) {
			fixturePath := testutil.GenerateInvalidProcessingMediaFixture(t, t.TempDir(), tc.blockType)
			body, err := os.ReadFile(fixturePath)
			require.NoError(t, err)

			fileID, uploadID, delivery := completeRuntimeStructuredEditorMediaUploadAndWait(
				t,
				stack,
				admin,
				tc.uploadType,
				tc.mimeType,
				runtimeTestFileName("editor-processing-failure"+runtimeMediaExtension(tc.blockType)),
				body,
			)
			require.Equal(t, commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_FAILED, delivery.GetProcessingStatus())
			requireRuntimeUploadSessionRemoved(t, stack, uploadID)
			queueName := eventpkg.QueueTranscoderAudio
			if tc.blockType == testutil.EditorMediaBlockTypeVideo {
				queueName = eventpkg.QueueTranscoderVideo
			}
			requireRuntimeTerminalTranscodeFailure(t, stack, fileID, queueName)
		})
	}
}

func TestRuntimeEditorAudioWaveformFailureIntegration(t *testing.T) {
	testutil.RequireFFmpeg(t)
	stack := testutil.SetupSharedRuntimeWaveformFailureStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())

	for _, tc := range runtimeEditorProcessingFailureCases(false) {
		t.Run(tc.name, func(t *testing.T) {
			fixturePath := testutil.GenerateTestAudioWAV(t, t.TempDir(), 2)
			body, err := os.ReadFile(fixturePath)
			require.NoError(t, err)

			fileID, uploadID, delivery := completeRuntimeStructuredEditorMediaUploadAndWait(
				t,
				stack,
				admin,
				managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
				"audio/wav",
				runtimeTestFileName("editor-waveform-failure.wav"),
				body,
			)
			require.Equal(t, commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_FAILED, delivery.GetProcessingStatus())
			requireRuntimeUploadSessionRemoved(t, stack, uploadID)
			requireRuntimeTerminalWaveformFailure(t, stack, fileID)
		})
	}
}

func runtimeEditorProcessingFailureCases(includeVideo bool) []runtimeEditorProcessingFailureCase {
	cases := []runtimeEditorProcessingFailureCase{{
		name:       "audio",
		blockType:  testutil.EditorMediaBlockTypeAudio,
		uploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		mimeType:   "audio/mpeg",
	}}
	if includeVideo {
		cases = append(cases, runtimeEditorProcessingFailureCase{
			name:       "video",
			blockType:  testutil.EditorMediaBlockTypeVideo,
			uploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO,
			mimeType:   "video/mp4",
		})
	}
	return cases
}

func runtimeMediaExtension(blockType testutil.EditorMediaBlockType) string {
	if blockType == testutil.EditorMediaBlockTypeVideo {
		return ".mp4"
	}
	return ".mp3"
}

func completeRuntimeStructuredEditorMediaUploadAndWait(
	t *testing.T,
	stack *testutil.RuntimeStack,
	user *testutil.OryUser,
	uploadType managev1.UploadType,
	mimeType string,
	fileName string,
	body []byte,
) (string, string, *commonv1.MediaDelivery) {
	t.Helper()

	fileClient := managev1connect.NewFileServiceClient(&http.Client{Timeout: 30 * time.Second}, stack.BackendURL)
	lastModified := time.Now().UnixMilli()
	initReq := connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
		UploadType:       uploadType,
		FileSize:         int64(len(body)),
		MimeType:         mimeType,
		FileName:         fileName,
		FileLastModified: &lastModified,
	})
	setAuthHeaders(initReq.Header(), user)
	initResp, err := fileClient.InitiateMultipartUpload(context.Background(), initReq)
	require.NoError(t, err)

	uploadMultipartBody(t, stack.BackendURL, user, initResp.Msg, body)
	completeReq := connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		FileId:        initResp.Msg.GetFileId(),
		UploadId:      initResp.Msg.GetUploadId(),
		CorrelationId: runtimePtr(uuid.NewString()),
	})
	setAuthHeaders(completeReq.Header(), user)
	completeResp, err := fileClient.CompleteMultipartUpload(context.Background(), completeReq)
	require.NoError(t, err)
	require.Equal(t, initResp.Msg.GetFileId(), completeResp.Msg.GetFileId())
	requireRuntimeCanonicalFileRecord(t, stack.DB, initResp.Msg.GetFileId(), initResp.Msg.GetExtension(), mimeType, body)

	delivery := waitForRuntimeTerminalFailure(t, fileClient, user, initResp.Msg.GetFileId(), func(delivery *commonv1.MediaDelivery) bool {
		return delivery.GetProcessingStatus() == commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_FAILED
	})
	return initResp.Msg.GetFileId(), initResp.Msg.GetUploadId(), delivery
}

func waitForRuntimeTerminalFailure(
	t *testing.T,
	fileClient managev1connect.FileServiceClient,
	user *testutil.OryUser,
	fileID string,
	failed func(*commonv1.MediaDelivery) bool,
) *commonv1.MediaDelivery {
	t.Helper()

	var last *commonv1.MediaDelivery
	testutil.WaitForFileProcessingComplete(t, 30*time.Second, func() (bool, string, error) {
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
		return failed(last), fmt.Sprintf(
			"processing status=%s percentage=%v",
			last.GetProcessingStatus().String(),
			last.ProcessingPercentage,
		), nil
	})
	require.NotNil(t, last)
	return last
}

func requireRuntimeUploadSessionRemoved(t *testing.T, stack *testutil.RuntimeStack, uploadID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return countUploadSessions(t, stack.DB, uploadID) == 0
	}, 10*time.Second, 200*time.Millisecond)
}

func requireRuntimeTerminalTranscodeFailure(t *testing.T, stack *testutil.RuntimeStack, fileID string, queueName string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var count int64
		err := stack.DB.Model(&model.TranscodeJob{}).
			Where("file_id = ? AND queue_name = ? AND status = ?", fileID, queueName, "TRANSCODE_JOB_STATUS_FAILED_TERMINAL").
			Count(&count).Error
		return err == nil && count == 1
	}, 15*time.Second, 200*time.Millisecond)
}

func requireRuntimeTerminalWaveformFailure(t *testing.T, stack *testutil.RuntimeStack, fileID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var count int64
		err := stack.DB.Model(&model.WaveformJob{}).
			Where("file_id = ? AND status = ?", fileID, transcode.WaveformJobStatusFailed).
			Count(&count).Error
		return err == nil && count == 1
	}, 15*time.Second, 200*time.Millisecond)
}
