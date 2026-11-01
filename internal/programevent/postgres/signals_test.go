package programeventpostgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/programevent"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
)

const (
	signalEventID     = "00000000-0000-4000-8000-000000000001"
	signalContributor = "00000000-0000-4000-8000-000000000002"
	signalRevision    = "00000000-0000-4000-8000-000000000003"
)

// Every signal a Program Event change schedules satisfies the
// content.updated contract: source changes carry the shared revision,
// target changes their target revision, deletions no target state.
func TestProgramEventContentSignalsSatisfyContract(t *testing.T) {
	t.Parallel()
	targetRevision := "target-revision"
	for name, test := range map[string]struct {
		update programevent.ContentUpdate
		check  func(*testing.T, *managev1.ContentUpdatedEvent)
	}{
		"AI source change": {
			update: programevent.ContentUpdate{
				EventID: signalEventID, Fields: []string{"content"}, DocumentRevision: signalRevision,
				Contributors: []string{signalContributor}, Source: programevent.SourceAI, Locale: "en", DocumentStateChanged: true,
			},
			check: func(t *testing.T, event *managev1.ContentUpdatedEvent) {
				require.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI, event.GetSource())
				require.Equal(t, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PROGRAM_EVENT, event.GetEntityType())
				require.True(t, event.GetDocumentStateChanged())
				require.Empty(t, event.GetTargetRevision())
				require.Equal(t, "document.content", event.GetChangedFields()[0].GetPath())
			},
		},
		"AI target change": {
			update: programevent.ContentUpdate{
				EventID: signalEventID, Fields: []string{"content"}, DocumentRevision: "document-revision",
				Contributors: []string{signalContributor}, Source: programevent.SourceAI, Locale: "ko", TargetRevision: &targetRevision,
			},
			check: func(t *testing.T, event *managev1.ContentUpdatedEvent) {
				require.Equal(t, "ko", event.GetLocale())
				require.True(t, event.GetLocaleExists())
				require.Equal(t, targetRevision, event.GetTargetRevision())
				require.False(t, event.GetDocumentStateChanged())
			},
		},
		"AI translation deletion": {
			update: programevent.ContentUpdate{
				EventID: signalEventID, Fields: []string{"content"}, DocumentRevision: signalRevision,
				Contributors: []string{signalContributor}, Source: programevent.SourceAI, Locale: "ko",
				LocaleDeleted: true, TargetRevision: &targetRevision,
			},
			check: func(t *testing.T, event *managev1.ContentUpdatedEvent) {
				require.False(t, event.GetLocaleExists())
				require.Nil(t, event.TargetRevision)
				require.Equal(t, signalRevision, event.GetDocumentRevision())
			},
		},
		"collaboration source change": {
			update: programevent.ContentUpdate{
				EventID: signalEventID, Fields: []string{"content"}, DocumentRevision: signalRevision,
				Contributors: []string{signalContributor, " ", signalContributor}, Source: programevent.SourceCollaboration,
				Locale: "en", DocumentStateChanged: true,
			},
			check: func(t *testing.T, event *managev1.ContentUpdatedEvent) {
				require.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB, event.GetSource())
				require.Equal(t, []string{signalContributor}, event.GetContributorMemberIds())
			},
		},
		"collaboration target summary": {
			update: programevent.ContentUpdate{
				EventID: signalEventID, Fields: []string{"summary"}, DocumentRevision: signalRevision,
				Contributors: []string{signalContributor}, Source: programevent.SourceCollaboration, Locale: "ko", TargetRevision: &targetRevision,
			},
			check: func(t *testing.T, event *managev1.ContentUpdatedEvent) {
				require.Equal(t, "summary", event.GetChangedFields()[0].GetPath())
				require.Equal(t, targetRevision, event.GetTargetRevision())
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			event := contentUpdatedEvent(test.update)
			require.NotNil(t, event)
			require.NoError(t, contentsignal.Validate(event))
			require.Equal(t, signalEventID, event.GetEntityId())
			test.check(t, event)
		})
	}
}

func TestProgramEventContentSignalSkipsNoObservableChange(t *testing.T) {
	t.Parallel()
	require.Nil(t, contentUpdatedEvent(programevent.ContentUpdate{Locale: "en", Fields: []string{"content"}}))
	require.Nil(t, contentUpdatedEvent(programevent.ContentUpdate{EventID: signalEventID, Locale: "en"}))
	require.Nil(t, contentUpdatedEvent(programevent.ContentUpdate{EventID: signalEventID, Fields: []string{"content"}}))
}

// A changed event's signal is published only in its owning transaction.
func TestProgramEventContentSignalRequiresOwningTransaction(t *testing.T) {
	t.Parallel()
	publisher := &recordingPublisher{}
	event := contentUpdatedEvent(programevent.ContentUpdate{
		EventID: signalEventID, Fields: []string{"content"}, DocumentRevision: signalRevision,
		Contributors: []string{signalContributor}, Source: programevent.SourceAI, Locale: "en", DocumentStateChanged: true,
	})
	require.NotNil(t, event)
	require.Error(t, (&store{ports: Ports{Signals: publisher}}).PublishContentUpdate(context.Background(), programevent.ContentUpdate{
		EventID: signalEventID, Fields: []string{"content"}, DocumentRevision: signalRevision, Source: programevent.SourceAI,
		Locale: "en", DocumentStateChanged: true,
	}))
	require.Error(t, contentsignal.PublishInTransaction(context.Background(), &gorm.DB{}, publisher, event))
	require.Zero(t, publisher.calls, "content.updated escaped the owning transaction")
}

type recordingPublisher struct{ calls int }

func (p *recordingPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	p.calls++
	return nil
}

func (*recordingPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (p *recordingPublisher) NotifyProtobufWithExecutor(ctx context.Context, _ eventpkg.DBTX, signal string, message proto.Message) error {
	return p.NotifyProtobuf(ctx, signal, message)
}
