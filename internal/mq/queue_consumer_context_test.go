package mq

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"
)

func TestProcessMessageReleasesSingleHandlerContextRegistration(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	parentBase, parentCancel := context.WithCancel(context.Background())
	t.Cleanup(parentCancel)
	parent := newAfterFuncTrackingContext(parentBase)
	envelope, err := eventpkg.NewEnvelope("message-1", "api.test.Message", nil)
	require.NoError(t, err)
	mock.ExpectQuery(`SELECT pgmq\.delete\(\$1, \$2::bigint\)`).
		WithArgs("test.queue", int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"deleted"}).AddRow(true))

	var handlerCtx context.Context
	consumer := NewQueueConsumer(db, QueueConfig{
		Name:        "test.queue",
		MessageType: "api.test.Message",
		Timeout:     time.Second,
	}, func(ctx context.Context, _ Message) error {
		handlerCtx = ctx
		return nil
	})
	consumer.processMessage(parent, eventpkg.Message{
		TransportID: 1,
		ReadCount:   1,
		Envelope:    envelope,
	})

	require.NotNil(t, handlerCtx)
	require.ErrorIs(t, handlerCtx.Err(), context.Canceled)
	registrations, active := parent.registrationCounts()
	require.Equal(t, 2, registrations, "one callback belongs to the delivery context and one to database/sql")
	require.Zero(t, active, "completed delivery and database query must unregister their callbacks")
	require.NoError(t, mock.ExpectationsWereMet())
}

type afterFuncTrackingContext struct {
	context.Context
	mu            sync.Mutex
	registrations int
	active        map[int]struct{}
	nextID        int
}

func newAfterFuncTrackingContext(parent context.Context) *afterFuncTrackingContext {
	return &afterFuncTrackingContext{Context: parent, active: make(map[int]struct{})}
}

func (c *afterFuncTrackingContext) Value(any) any { return nil }

func (c *afterFuncTrackingContext) AfterFunc(func()) func() bool {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.registrations++
	c.active[id] = struct{}{}
	c.mu.Unlock()
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, exists := c.active[id]; !exists {
			return false
		}
		delete(c.active, id)
		return true
	}
}

func (c *afterFuncTrackingContext) registrationCounts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.registrations, len(c.active)
}

var _ interface {
	context.Context
	AfterFunc(func()) func() bool
} = (*afterFuncTrackingContext)(nil)
