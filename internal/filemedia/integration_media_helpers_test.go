//go:build integration

package filemedia

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type hardCutRecordingMeshOptimizationPublisher struct {
	jobs []*managev1.MeshOptimizationJob
}

func (p *hardCutRecordingMeshOptimizationPublisher) PublishMeshOptimizationJob(_ context.Context, job *managev1.MeshOptimizationJob) error {
	p.jobs = append(p.jobs, job)
	return nil
}

type hardCutPublishedMessage struct {
	exchange    string
	key         string
	messageID   string
	messageType string
	body        []byte
}

type hardCutAsyncPublisher struct {
	messages []hardCutPublishedMessage
}

func (p *hardCutAsyncPublisher) EnqueueProtobuf(
	_ context.Context,
	queue string,
	messageID string,
	msg proto.Message,
) error {
	body, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(msg)
	p.messages = append(p.messages, hardCutPublishedMessage{
		key:         queue,
		messageID:   messageID,
		messageType: string(msg.ProtoReflect().Descriptor().FullName()),
		body:        body,
	})
	return nil
}

func (p *hardCutAsyncPublisher) NotifyProtobuf(
	_ context.Context,
	signal string,
	msg proto.Message,
) error {
	body, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(msg)
	p.messages = append(p.messages, hardCutPublishedMessage{
		exchange:    signal,
		messageType: string(msg.ProtoReflect().Descriptor().FullName()),
		body:        body,
	})
	return nil
}

func (p *hardCutAsyncPublisher) EnqueueProtobufWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	queue string,
	messageID string,
	msg proto.Message,
) error {
	if executor == nil {
		return errors.New("transactional fixture executor is required")
	}
	return p.EnqueueProtobuf(ctx, queue, messageID, msg)
}

func decodeHardCutRoutedMessages[T proto.Message](
	t *testing.T,
	messages []hardCutPublishedMessage,
	exchange string,
	routingKey string,
	newMessage func() T,
) []T {
	t.Helper()
	decoded := make([]T, 0)
	for _, message := range messages {
		target := newMessage()
		if message.exchange != exchange || message.key != routingKey ||
			message.messageType != string(target.ProtoReflect().Descriptor().FullName()) {
			continue
		}
		require.NoError(t, proto.Unmarshal(message.body, target))
		decoded = append(decoded, target)
	}
	return decoded
}

func hardCutPtrString(value string) *string { return &value }

func seedHardCutPageFixture(t *testing.T, db *gorm.DB) string {
	t.Helper()
	pageID := uuid.NewString()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile) VALUES (?, 'page')`,
		documentID,
	).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO page (id, content_document_id, status, show_title, created_at, updated_at)
		VALUES (?, ?, 'PAGE_STATUS_DRAFT', TRUE, NOW(), NOW())
	`, pageID, documentID).Error)
	return pageID
}
