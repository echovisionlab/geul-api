package mq

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/aidocument"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"
)

func TestPublisherNeverSuppressesInteractiveContentSignal(t *testing.T) {
	t.Parallel()
	publisher := &Publisher{}
	event := &managev1.ContentUpdatedEvent{}

	err := publisher.NotifyProtobuf(
		aidocument.WithInteractivePostCommitCompletion(context.Background()),
		eventpkg.SignalContentUpdated,
		event,
	)
	require.ErrorContains(t, err, "PostgreSQL publisher is required")

	err = publisher.NotifyProtobuf(
		aidocument.WithInteractiveFallbackSignal(aidocument.WithInteractivePostCommitCompletion(context.Background())),
		eventpkg.SignalContentUpdated,
		event,
	)
	require.ErrorContains(t, err, "PostgreSQL publisher is required")
}
