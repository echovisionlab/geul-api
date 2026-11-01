package menupostgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/menu"
	menupostgres "github.com/echovisionlab/geul-api/internal/menu/postgres"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type recordedEvents struct{ events []*managev1.ContentUpdatedEvent }

func (r *recordedEvents) NotifyProtobufWithExecutor(_ context.Context, _ eventpkg.DBTX, signal string, message proto.Message) error {
	if signal != eventpkg.SignalContentUpdated {
		return errors.New("unexpected signal " + signal)
	}
	r.events = append(r.events, proto.Clone(message).(*managev1.ContentUpdatedEvent))
	return nil
}

func TestAIDocumentChangeSchedulesTheInteractiveContentSignal(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	recorded := &recordedEvents{}
	ctx := contentsignal.WithInteractivePublisher(t.Context(), recorded)
	menuID, member, target := uuid.NewString(), uuid.NewString(), uuid.NewString()

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		store := menupostgres.NewStore(tx, menupostgres.Ports{})
		if err := store.PublishAIDocumentChange(ctx, menu.AIDocumentChange{MenuID: menuID, Locale: "en", DocumentRevision: "document", MemberID: member}); err != nil {
			return err
		}
		return store.PublishAIDocumentChange(ctx, menu.AIDocumentChange{
			MenuID: menuID, Locale: "ko", DocumentRevision: "document", TargetRevision: &target, Changed: true, MemberID: member,
		})
	}))

	require.Len(t, recorded.events, 1, "an unchanged document schedules no signal")
	event := recorded.events[0]
	require.NotZero(t, event.GetTimestampMs())
	event.TimestampMs = 0
	document, locale, exists := "document", "ko", true
	require.True(t, proto.Equal(&managev1.ContentUpdatedEvent{
		EntityType: managev1.ContentEntityType_CONTENT_ENTITY_TYPE_MENU, EntityId: menuID,
		Source:           managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		DocumentRevision: &document, TargetRevision: &target, Locale: &locale, LocaleExists: &exists,
		ContributorMemberIds: []string{member},
		ChangedFields: []*managev1.ContentUpdatedField{{
			Path: "document.content", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT,
		}},
	}, event), "content.updated: %v", event)
}
