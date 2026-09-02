//go:build integration

package filemedia

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type fileIngestAttachmentTestPublisher struct {
	confirmed []fileIngestPublishedMessage
	signals   []string
	err       error
}

type fileIngestPublishedMessage struct {
	queue     string
	messageID string
	body      []byte
}

func (p *fileIngestAttachmentTestPublisher) NotifyProtobuf(
	_ context.Context, signal string, _ proto.Message,
) error {
	p.signals = append(p.signals, signal)
	return p.err
}

func (p *fileIngestAttachmentTestPublisher) EnqueueProtobuf(
	_ context.Context, queue, messageID string, message proto.Message,
) error {
	body, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	p.confirmed = append(p.confirmed, fileIngestPublishedMessage{queue: queue, messageID: messageID, body: body})
	return p.err
}

func (p *fileIngestAttachmentTestPublisher) EnqueueProtobufWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	queue string,
	messageID string,
	message proto.Message,
) error {
	if executor == nil {
		return errors.New("transactional fixture executor is required")
	}
	return p.EnqueueProtobuf(ctx, queue, messageID, message)
}

func TestEditorFileIngestHasNoDocumentAttachmentTarget(t *testing.T) {
	t.Parallel()
	for _, uploadType := range []managev1.UploadType{
		managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH,
	} {
		target, err := normalizeFileIngestProjectionIdentity(
			uploadType,
			managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED,
			"",
			nil,
		)
		require.NoError(t, err)
		require.Equal(t, fileIngestTargetModeEditorFile, target.mode)
		require.False(t, target.requiresDurableAttachment())

		_, err = normalizeFileIngestProjectionIdentity(
			uploadType,
			managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
			"",
			nil,
		)
		require.ErrorContains(t, err, "must omit document entity type")
		_, err = normalizeFileIngestProjectionIdentity(
			uploadType,
			managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED,
			"block-or-slot",
			nil,
		)
		require.ErrorContains(t, err, "independent of document block attachment")
		expected := uuid.NewString()
		_, err = normalizeFileIngestProjectionIdentity(
			uploadType,
			managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED,
			"",
			&expected,
		)
		require.ErrorContains(t, err, "independent of document block attachment")
	}
}

func TestStoredEditorSessionRejectsLegacyDocumentTarget(t *testing.T) {
	t.Parallel()
	entityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST.String()
	_, err := fileIngestTargetFromStoredSession(model.UploadSession{
		UploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String(),
		EntityType: &entityType,
		EntityID:   uuid.NewString(),
	})
	require.ErrorContains(t, err, "must omit document entity type")
}
