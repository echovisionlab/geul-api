package filemediaqueue

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Deletion struct {
	publisher any
	async     bool
}

func Async(publisher any) Deletion     { return Deletion{publisher: publisher, async: true} }
func Publisher(publisher any) Deletion { return Deletion{publisher: publisher} }
func (d Deletion) EnqueueFileDeletion(ctx context.Context, executor eventpkg.DBTX, target domain.DeletionTarget) error {
	event := DeletionEvent(target)
	if d.async {
		p, ok := d.publisher.(interface {
			EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error
		})
		if !ok {
			return fmt.Errorf("file delete publisher must support transactional enqueue")
		}
		return p.EnqueueProtobufWithExecutor(ctx, executor, eventpkg.QueueFileDelete, event.GetFileId(), event)
	}
	p, ok := d.publisher.(interface {
		PublishFileDeleteWithExecutor(context.Context, eventpkg.DBTX, *managev1.FileDeleteEvent) error
	})
	if !ok {
		return fmt.Errorf("file delete publisher must support transactional enqueue")
	}
	return p.PublishFileDeleteWithExecutor(ctx, executor, event)
}
func DeletionEvent(target domain.DeletionTarget) *managev1.FileDeleteEvent {
	event := &managev1.FileDeleteEvent{FileId: target.FileID, Timestamp: timestamppb.New(target.Timestamp), Generations: make([]*commonv1.MediaGenerationWriteTarget, 0, len(target.Generations))}
	if o := target.Original; o != nil {
		event.Original = &commonv1.MediaObjectTarget{FileId: o.FileID, ObjectKey: o.ObjectKey, Extension: o.Extension, MimeType: o.MimeType}
	}
	for _, g := range target.Generations {
		if g == nil {
			event.Generations = append(event.Generations, nil)
			continue
		}
		event.Generations = append(event.Generations, &commonv1.MediaGenerationWriteTarget{GenerationId: g.GenerationID, FileId: g.FileID, ObjectPrefix: g.ObjectPrefix})
	}
	return event
}
