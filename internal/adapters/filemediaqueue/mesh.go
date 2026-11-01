package filemediaqueue

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type meshPublisher interface {
	PublishMeshOptimizationJob(context.Context, *managev1.MeshOptimizationJob) error
}
type MeshJobs struct{ publisher meshPublisher }

func Mesh(primary, fallback any) MeshJobs {
	publisher, ok := primary.(meshPublisher)
	if !ok {
		publisher, _ = fallback.(meshPublisher)
	}
	return MeshJobs{publisher: publisher}
}
func (p MeshJobs) RequirePublisher() error {
	if p.publisher == nil {
		return fmt.Errorf("mesh optimization publisher is not configured")
	}
	return nil
}
func (p MeshJobs) Enqueue(ctx context.Context, tx *gorm.DB, input domain.MeshJob) error {
	if err := p.RequirePublisher(); err != nil {
		return err
	}
	job := MeshJobEvent(input)
	if _, ok := p.publisher.(interface {
		EnqueueProtobuf(context.Context, string, string, proto.Message) error
		NotifyProtobuf(context.Context, string, proto.Message) error
	}); !ok {
		return p.publisher.PublishMeshOptimizationJob(ctx, job)
	}
	executor, err := contentsignal.TransactionExecutor(tx)
	if err != nil {
		return err
	}
	publisher, ok := p.publisher.(interface {
		EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error
	})
	if !ok {
		return fmt.Errorf("async publisher does not support transactional PGMQ enqueue")
	}
	return publisher.EnqueueProtobufWithExecutor(ctx, executor, eventpkg.QueueAssetOptimizerMesh, input.JobID, job)
}

func MeshJobEvent(input domain.MeshJob) *managev1.MeshOptimizationJob {
	target := func(v *domain.ObjectTarget) *commonv1.MediaObjectTarget {
		if v == nil {
			return nil
		}
		return &commonv1.MediaObjectTarget{FileId: v.FileID, ObjectKey: v.ObjectKey, Extension: v.Extension, MimeType: v.MimeType}
	}
	return &managev1.MeshOptimizationJob{JobId: input.JobID, CorrelationId: input.CandidateID, Identity: &managev1.MeshOptimizationIdentity{EntityType: managev1.TranscodeEntityType(input.EntityType), EntityId: input.EntityID, FileId: input.SourceFileID, Source: target(input.Source), SlotId: input.SlotID, AttemptId: input.AttemptID}, Options: &managev1.MeshOptimizationOptions{CompressionMethod: managev1.MeshOptimizationCompressionMethod(input.CompressionMethod), TargetRatioPercent: input.TargetRatioPercent, Profile: managev1.MeshOptimizationProfile(input.Profile)}, Output: target(input.Output), TimestampMs: input.TimestampMS}
}
