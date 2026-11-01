package filemediaqueue

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type deletionPublisher struct {
	executor  eventpkg.DBTX
	queue, id string
	event     *managev1.FileDeleteEvent
}

func (p *deletionPublisher) EnqueueProtobufWithExecutor(_ context.Context, executor eventpkg.DBTX, queue, id string, event proto.Message) error {
	p.executor, p.queue, p.id, p.event = executor, queue, id, event.(*managev1.FileDeleteEvent)
	return nil
}
func (p *deletionPublisher) PublishFileDeleteWithExecutor(_ context.Context, executor eventpkg.DBTX, event *managev1.FileDeleteEvent) error {
	p.executor, p.event = executor, event
	return nil
}
func TestDeletionQueuePreservesFileDeleteBytesAndExecutor(t *testing.T) {
	target := domain.DeletionTarget{FileID: "file", Original: &domain.ObjectTarget{FileID: "file", ObjectKey: "media/file.glb", Extension: "glb", MimeType: "model/gltf-binary"}, Generations: []*domain.GenerationTarget{{GenerationID: "generation", FileID: "file", ObjectPrefix: "media/file/hls/generation"}}, Timestamp: time.Unix(123, 456).UTC()}
	expected := &managev1.FileDeleteEvent{FileId: "file", Original: &commonv1.MediaObjectTarget{FileId: "file", ObjectKey: "media/file.glb", Extension: "glb", MimeType: "model/gltf-binary"}, Generations: []*commonv1.MediaGenerationWriteTarget{{GenerationId: "generation", FileId: "file", ObjectPrefix: "media/file/hls/generation"}}, Timestamp: timestamppb.New(target.Timestamp)}
	want, err := proto.MarshalOptions{Deterministic: true}.Marshal(expected)
	require.NoError(t, err)
	executor := &sql.DB{}
	for _, async := range []bool{false, true} {
		p := &deletionPublisher{}
		queue := Publisher(p)
		if async {
			queue = Async(p)
		}
		require.NoError(t, queue.EnqueueFileDeletion(t.Context(), executor, target))
		require.Same(t, executor, p.executor)
		got, err := proto.MarshalOptions{Deterministic: true}.Marshal(p.event)
		require.NoError(t, err)
		require.Equal(t, want, got)
		if async {
			require.Equal(t, eventpkg.QueueFileDelete, p.queue)
			require.Equal(t, "file", p.id)
		}
	}
	for _, queue := range []Deletion{Async(struct{}{}), Publisher(struct{}{})} {
		require.EqualError(t, queue.EnqueueFileDeletion(t.Context(), executor, target), "file delete publisher must support transactional enqueue")
	}
}
