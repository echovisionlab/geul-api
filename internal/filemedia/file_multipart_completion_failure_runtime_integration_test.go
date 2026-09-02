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
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type runtimeMultipartCompletionFailureInput struct {
	uploadType managev1.UploadType
	entityID   string
	entityType managev1.TranscodeEntityType
	fileName   string
	mimeType   string
	body       []byte
}

type runtimeMultipartCompletionFailureResult struct {
	fileID        string
	uploadID      string
	correlationID string
	totalParts    int32
	failedEvent   *managev1.FileIngestFailedEvent
}

// Editor completion failures remain File-scoped and never create document
// relations, regardless of the media kind.
func TestRuntimeEditorMediaCompleteMultipartFailureIntegration(t *testing.T) {
	stack := testutil.SetupSharedRuntimeCompleteMultipartFailureStack(t)

	editorCases := []struct {
		name      string
		blockType testutil.EditorMediaBlockType
	}{
		{name: "audio", blockType: testutil.EditorMediaBlockTypeAudio},
		{name: "video", blockType: testutil.EditorMediaBlockTypeVideo},
		{name: "image", blockType: testutil.EditorMediaBlockTypeImage},
		{name: "attachment", blockType: testutil.EditorMediaBlockTypeAttachment},
	}

	for _, tc := range editorCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
			uploadType, fileName, mimeType, body := runtimeEditorCompletionFailureFixture(t, tc.blockType)
			result := runRuntimeMultipartCompletionFailure(t, stack, admin, runtimeMultipartCompletionFailureInput{
				uploadType: uploadType,
				fileName:   runtimeTestFileName(fileName),
				mimeType:   mimeType,
				body:       body,
			})

			require.Equal(t, int64(0), countFilesByID(t, stack.DB, result.fileID))
			require.Equal(t, int64(1), countUploadSessions(t, stack.DB, result.uploadID))
			require.EqualValues(t, result.totalParts, countUploadParts(t, stack.DB, result.uploadID))
			requireUploadSessionStatus(
				t,
				stack.DB,
				result.uploadID,
				managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_FAILED,
			)
			require.EqualValues(t, 1, stack.MultipartCompletionFailureCount(t, result.uploadID))

			var failedSessionCount int64
			require.NoError(t, stack.DB.Table("upload_session").
				Where("upload_id = ? AND entity_id IS NULL AND slot_id IS NULL AND status = ?", result.uploadID, model.UploadSessionStatusFailed).
				Count(&failedSessionCount).Error)
			require.EqualValues(t, 1, failedSessionCount)

			identity := result.failedEvent.GetIdentity()
			require.Equal(t, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE, identity.GetEntityType())
			require.Equal(t, result.fileID, identity.GetEntityId())
			require.Equal(t, result.fileID, identity.GetFileId())
			require.Equal(t, result.uploadID, identity.GetUploadId())
			require.Empty(t, identity.GetSlotId())
		})
	}
}

func runRuntimeMultipartCompletionFailure(
	t *testing.T,
	stack *testutil.RuntimeStack,
	user *testutil.OryUser,
	input runtimeMultipartCompletionFailureInput,
) runtimeMultipartCompletionFailureResult {
	t.Helper()

	fileClient := managev1connect.NewFileServiceClient(
		&http.Client{Timeout: 30 * time.Second},
		stack.BackendURL,
	)
	lastModified := time.Now().UnixMilli()
	var entityType *managev1.TranscodeEntityType
	if input.entityID != "" {
		entityType = &input.entityType
	}
	initReq := connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
		UploadType:       input.uploadType,
		EntityId:         input.entityID,
		EntityType:       entityType,
		FileName:         input.fileName,
		FileSize:         int64(len(input.body)),
		MimeType:         input.mimeType,
		FileLastModified: &lastModified,
	})
	setAuthHeaders(initReq.Header(), user)
	initResp, err := fileClient.InitiateMultipartUpload(context.Background(), initReq)
	require.NoError(t, err)
	require.NotEmpty(t, initResp.Msg.GetFileId())
	require.NotEmpty(t, initResp.Msg.GetUploadId())

	uploadRuntimeMultipartBody(
		t,
		stack.BackendURL,
		user,
		initResp.Msg.GetFileId(),
		initResp.Msg.GetUploadId(),
		input.body,
	)
	require.EqualValues(t, initResp.Msg.GetTotalParts(), countUploadParts(t, stack.DB, initResp.Msg.GetUploadId()))

	stack.MarkMultipartCompletionFailure(t, initResp.Msg.GetUploadId())
	correlationID := uuid.NewString()
	failedReceiver := newFileIngestSignalReceiver(t, stack.PostgresDSN)
	completeReq := connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		FileId:        initResp.Msg.GetFileId(),
		UploadId:      initResp.Msg.GetUploadId(),
		CorrelationId: &correlationID,
	})
	setAuthHeaders(completeReq.Header(), user)

	_, err = fileClient.CompleteMultipartUpload(context.Background(), completeReq)
	require.Error(t, err)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.Contains(t, err.Error(), "NoSuchUpload")

	failedEvent := waitForFileIngestProtoEvent(
		t,
		failedReceiver,
		10*time.Second,
		func() *managev1.FileIngestFailedEvent { return &managev1.FileIngestFailedEvent{} },
		func(event *managev1.FileIngestFailedEvent) bool {
			return event.GetCorrelationId() == correlationID &&
				event.GetIdentity().GetUploadId() == initResp.Msg.GetUploadId() &&
				event.GetIdentity().GetFileId() == initResp.Msg.GetFileId()
		},
	)
	require.Equal(t, managev1.FileIngestFailureReason_FILE_INGEST_FAILURE_REASON_STORAGE_FAILED, failedEvent.GetReason())
	require.Contains(t, failedEvent.GetError(), "NoSuchUpload")
	require.Equal(t, int32(100), failedEvent.GetProgress().GetPercentage())
	require.Equal(t, int64(len(input.body)), failedEvent.GetProgress().GetBytesCompleted())
	require.Equal(t, int64(len(input.body)), failedEvent.GetProgress().GetBytesTotal())

	return runtimeMultipartCompletionFailureResult{
		fileID:        initResp.Msg.GetFileId(),
		uploadID:      initResp.Msg.GetUploadId(),
		correlationID: correlationID,
		totalParts:    initResp.Msg.GetTotalParts(),
		failedEvent:   failedEvent,
	}
}

func uploadRuntimeMultipartBody(
	t *testing.T,
	backendURL string,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	body []byte,
) {
	t.Helper()

	for offset, partNumber := 0, 1; offset < len(body); offset, partNumber = offset+chunkSize, partNumber+1 {
		end := offset + chunkSize
		if end > len(body) {
			end = len(body)
		}
		part := uploadMultipartPart(t, backendURL, user, fileID, uploadID, partNumber, body[offset:end])
		require.NotEmpty(t, part.ETag)
	}
}

func runtimeEditorCompletionFailureFixture(
	t *testing.T,
	blockType testutil.EditorMediaBlockType,
) (managev1.UploadType, string, string, []byte) {
	t.Helper()

	var (
		uploadType managev1.UploadType
		fileName   string
		mimeType   string
		path       string
	)
	switch blockType {
	case testutil.EditorMediaBlockTypeAudio:
		uploadType = managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO
		fileName = "editor-complete-failure.mp3"
		mimeType = "audio/mpeg"
		path = testutil.RepositoryTestAudioMP3(t)
	case testutil.EditorMediaBlockTypeVideo:
		uploadType = managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO
		fileName = "editor-complete-failure.mp4"
		mimeType = "video/mp4"
		path = testutil.RepositoryTestVideoMP4(t)
	case testutil.EditorMediaBlockTypeImage:
		uploadType = managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE
		fileName = "editor-complete-failure.jpg"
		mimeType = "image/jpeg"
		path = testutil.RepositoryTestImageJPEG(t)
	case testutil.EditorMediaBlockTypeAttachment:
		return managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT,
			"editor-complete-failure.txt",
			"text/plain",
			[]byte("runtime multipart completion failure attachment\n")
	default:
		require.FailNow(t, fmt.Sprintf("unsupported editor media block type: %s", blockType))
		return managev1.UploadType_UPLOAD_TYPE_UNSPECIFIED, "", "", nil
	}

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, body)
	return uploadType, fileName, mimeType, body
}
