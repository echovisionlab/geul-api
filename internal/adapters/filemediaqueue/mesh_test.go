package filemediaqueue

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestMeshJobMappingPreservesTargetsOptionalIdentityAndUnknownEnums(t *testing.T) {
	input := domain.MeshJob{JobID: "job", CandidateID: "candidate", SourceFileID: "source", EntityID: "entity", EntityType: 999, CompressionMethod: 998, Profile: 997, TargetRatioPercent: 43, Source: &domain.ObjectTarget{FileID: "source", ObjectKey: "media/source.glb", Extension: "glb", MimeType: "model/gltf-binary"}, Output: &domain.ObjectTarget{FileID: "output", ObjectKey: "media/output.glb", Extension: "glb", MimeType: "model/gltf-binary"}, SlotID: new("slot"), AttemptID: new("attempt"), TimestampMS: 123}
	want := &managev1.MeshOptimizationJob{JobId: "job", CorrelationId: "candidate", Identity: &managev1.MeshOptimizationIdentity{EntityType: 999, EntityId: "entity", FileId: "source", Source: &commonv1.MediaObjectTarget{FileId: "source", ObjectKey: "media/source.glb", Extension: "glb", MimeType: "model/gltf-binary"}, SlotId: new("slot"), AttemptId: new("attempt")}, Options: &managev1.MeshOptimizationOptions{CompressionMethod: 998, TargetRatioPercent: 43, Profile: 997}, Output: &commonv1.MediaObjectTarget{FileId: "output", ObjectKey: "media/output.glb", Extension: "glb", MimeType: "model/gltf-binary"}, TimestampMs: 123}
	require.True(t, proto.Equal(want, MeshJobEvent(input)))
	empty := MeshJobEvent(domain.MeshJob{})
	require.Nil(t, empty.Identity.Source)
	require.Nil(t, empty.Identity.SlotId)
	require.Nil(t, empty.Identity.AttemptId)
	require.Nil(t, empty.Output)
}

type meshRecorder struct {
	jobs []*managev1.MeshOptimizationJob
	err  error
}

func (p *meshRecorder) PublishMeshOptimizationJob(_ context.Context, job *managev1.MeshOptimizationJob) error {
	p.jobs = append(p.jobs, job)
	return p.err
}

type meshAsyncRecorder struct {
	meshRecorder
	queue, id string
	message   proto.Message
	executor  eventpkg.DBTX
}

func (*meshAsyncRecorder) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return errors.New("unexpected nontransactional enqueue")
}
func (*meshAsyncRecorder) NotifyProtobuf(context.Context, string, proto.Message) error {
	return errors.New("unexpected notification")
}
func (p *meshAsyncRecorder) EnqueueProtobufWithExecutor(_ context.Context, executor eventpkg.DBTX, queue, id string, message proto.Message) error {
	p.executor, p.queue, p.id, p.message = executor, queue, id, message
	return p.err
}

type meshNontransactional struct{ meshRecorder }

func (*meshNontransactional) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}
func (*meshNontransactional) NotifyProtobuf(context.Context, string, proto.Message) error { return nil }

func TestMeshPublisherSelectionAndLegacyBoundary(t *testing.T) {
	primary, fallback := &meshRecorder{}, &meshRecorder{}
	require.NoError(t, Mesh(primary, fallback).Enqueue(t.Context(), nil, domain.MeshJob{JobID: "primary"}))
	require.Len(t, primary.jobs, 1)
	require.Empty(t, fallback.jobs)
	require.NoError(t, Mesh(struct{}{}, fallback).Enqueue(t.Context(), nil, domain.MeshJob{JobID: "fallback"}))
	require.Len(t, fallback.jobs, 1)
	require.Equal(t, "fallback", fallback.jobs[0].JobId)
	require.ErrorContains(t, Mesh(nil, nil).RequirePublisher(), "publisher is not configured")
}

func TestMeshAsyncPublisherUsesTheTransactionAndNeverFallsBackOnFailure(t *testing.T) {
	for _, mode := range []string{"fallback", "enqueue failure", "missing transactional support"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			sqlDB, mock, err := sqlmock.New()
			require.NoError(t, err)
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true})
			require.NoError(t, err)
			publisher := &meshAsyncRecorder{}
			var selected any = publisher
			injected := errors.New("queue unavailable")
			switch mode {
			case "enqueue failure":
				publisher.err = injected
			case "missing transactional support":
				selected = &meshNontransactional{}
			}
			mock.ExpectBegin()
			if mode == "fallback" {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			err = db.Transaction(func(tx *gorm.DB) error {
				return Mesh(struct{}{}, selected).Enqueue(t.Context(), tx, domain.MeshJob{JobID: "job", CandidateID: "candidate"})
			})
			switch mode {
			case "fallback":
				require.NoError(t, err)
			case "enqueue failure":
				require.ErrorIs(t, err, injected)
			case "missing transactional support":
				require.ErrorContains(t, err, "does not support transactional PGMQ enqueue")
			}
			require.Empty(t, publisher.jobs)
			if mode != "missing transactional support" {
				require.NotNil(t, publisher.executor)
				require.Equal(t, eventpkg.QueueAssetOptimizerMesh, publisher.queue)
				require.Equal(t, "job", publisher.id)
				require.True(t, proto.Equal(MeshJobEvent(domain.MeshJob{JobID: "job", CandidateID: "candidate"}), publisher.message))
			} else {
				require.Empty(t, selected.(*meshNontransactional).jobs)
			}
			require.NoError(t, mock.ExpectationsWereMet())
			mock.ExpectClose()
			require.NoError(t, sqlDB.Close())
		})
	}
}
