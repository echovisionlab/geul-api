//go:build integration

package filemedia

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/adapters/filemediaqueue"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
)

func managePolicyString(value string) *string { return &value }
func observedFileDownloadPolicy(access managev1.FileDownloadAccess, criteriaIDs ...string) *managev1.FileDownloadPolicyObservedState {
	return &managev1.FileDownloadPolicyObservedState{
		Access:            access,
		MemberCriteriaIds: criteriaIDs,
	}
}

type policySignalPublisher struct {
	events   []*managev1.ContentUpdatedEvent
	executor eventpkg.DBTX
	err      error
}

func (p *policySignalPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return errors.New("unexpected enqueue")
}
func (p *policySignalPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return errors.New("unexpected non-transactional signal")
}
func (p *policySignalPublisher) NotifyProtobufWithExecutor(_ context.Context, executor eventpkg.DBTX, signal string, message proto.Message) error {
	if signal != eventpkg.SignalContentUpdated {
		return errors.New("unexpected signal")
	}
	p.executor = executor
	p.events = append(p.events, proto.Clone(message).(*managev1.ContentUpdatedEvent))
	return p.err
}

func (p *policySignalPublisher) PublishIngest(ctx context.Context, event domain.IngestEvent) error {
	return filemediaqueue.Lifecycle(p).PublishIngest(ctx, event)
}
func (p *policySignalPublisher) PublishProcessing(ctx context.Context, event domain.ProcessingEvent) error {
	return filemediaqueue.Lifecycle(p).PublishProcessing(ctx, event)
}
