//go:build integration

package mq

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
)

// TestContentUpdatedSignalIsAtomicWithTransaction proves the delivery
// semantics editor-collab relies on against a real PostgreSQL LISTEN session:
// a rolled-back domain transaction publishes nothing and a committed one
// publishes exactly once.
func TestContentUpdatedSignalIsAtomicWithTransaction(t *testing.T) {
	t.Parallel()
	pg := newMQIntegrationPostgres(t, "content.updated")
	var err error
	ctx := t.Context()

	listener, err := pgx.Connect(ctx, pg.DSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close(context.Background()) })
	_, err = listener.Exec(ctx, "LISTEN "+pgx.Identifier{eventpkg.SignalContentUpdated}.Sanitize())
	require.NoError(t, err)
	_, err = listener.Exec(ctx, "LISTEN "+pgx.Identifier{signalBarrier}.Sanitize())
	require.NoError(t, err)

	publisher := &Publisher{db: pg.SQLDB}
	event := func(id string) *managev1.ContentUpdatedEvent {
		revision := "0199a3c4-0000-7000-8000-000000000001"
		locale := "en"
		exists := true
		return &managev1.ContentUpdatedEvent{
			EntityType:           managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST,
			EntityId:             id,
			Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			DocumentRevision:     &revision,
			DocumentStateChanged: true,
			Locale:               &locale,
			LocaleExists:         &exists,
		}
	}
	rollback := errors.New("domain mutation failed after scheduling the signal")

	err = pg.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := contentsignal.PublishInTransaction(ctx, tx, publisher, event("rolled-back")); err != nil {
			return err
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	requireNoContentSignal(t, pg.DB, listener)
	var count int64
	require.NoError(t, pg.DB.Raw(`SELECT count(*) FROM pgmq."q_content.updated"`).Scan(&count).Error)
	require.Zero(t, count)

	require.NoError(t, pg.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return contentsignal.PublishInTransaction(ctx, tx, publisher, event("committed"))
	}))
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	notification, err := listener.WaitForNotification(waitCtx)
	require.NoError(t, err)
	require.Equal(t, eventpkg.SignalContentUpdated, notification.Channel)
	var envelope eventpkg.Envelope
	require.NoError(t, json.Unmarshal([]byte(notification.Payload), &envelope))
	body, err := envelope.Payload()
	require.NoError(t, err)
	decoded := &managev1.ContentUpdatedEvent{}
	require.NoError(t, proto.Unmarshal(body, decoded))
	require.Equal(t, "committed", decoded.GetEntityId())
	require.NoError(t, pg.DB.Raw(`SELECT count(*) FROM pgmq."q_content.updated"`).Scan(&count).Error)
	require.EqualValues(t, 1, count)
	var messageID string
	require.NoError(t, pg.DB.Raw(`SELECT message->>'message_id' FROM pgmq."q_content.updated"`).Scan(&messageID).Error)
	require.Equal(t, envelope.MessageID, messageID)

	requireNoContentSignal(t, pg.DB, listener)
}

// signalBarrier is the channel requireNoContentSignal notifies after the
// transactions under test ended.
const signalBarrier = "mq_test_signal_barrier"

// requireNoContentSignal fails when a signal is pending for listener.
// PostgreSQL delivers notifications in commit order, so a barrier notified
// after the transactions under test arrives first only when none of them
// sent a signal; no fixed wait is needed.
func requireNoContentSignal(t *testing.T, db *gorm.DB, listener *pgx.Conn) {
	t.Helper()
	require.NoError(t, db.Exec(`SELECT pg_notify(?, '')`, signalBarrier).Error)
	waitCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	notification, err := listener.WaitForNotification(waitCtx)
	require.NoError(t, err)
	require.Equal(t, signalBarrier, notification.Channel, "unexpected signal: %+v", notification)
}
