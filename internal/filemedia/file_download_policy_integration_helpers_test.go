//go:build integration

package filemedia

import (
	"context"
	"errors"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
)

func managePolicyString(value string) *string { return &value }
func observedFileDownloadPolicy(audience managev1.FileDownloadAudience, segmentIDs ...string) *managev1.FileDownloadPolicyObservedState {
	return &managev1.FileDownloadPolicyObservedState{
		Audience:           audience,
		AudienceSegmentIds: segmentIDs,
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
