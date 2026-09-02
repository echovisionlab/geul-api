package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewPostgresLeaderRequiresDatabase(t *testing.T) {
	require.PanicsWithValue(t, "scheduler: database cannot be nil for PostgresLeader", func() {
		NewPostgresLeader(nil, "instance-a")
	})
}

func TestPostgresLeaderAcquiresAndReleasesOneSessionLock(t *testing.T) {
	session := &fakeAdvisoryLockSession{acquire: true}
	leader := testPostgresLeader(session)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		leader.Start(ctx)
	}()
	require.Eventually(t, leader.IsLeader, time.Second, time.Millisecond)
	cancel()
	requireClosed(t, done)
	require.Equal(t, int64(1), session.acquires.Load())
	require.Equal(t, int64(1), session.unlocks.Load())
	require.Equal(t, int64(1), session.closes.Load())
}

func TestPostgresLeaderDoesNotClaimLeadershipWhenLockIsHeld(t *testing.T) {
	session := &fakeAdvisoryLockSession{acquire: false}
	leader := testPostgresLeader(session)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		leader.Start(ctx)
	}()
	require.Eventually(t, func() bool { return session.acquires.Load() == 1 }, time.Second, time.Millisecond)
	require.False(t, leader.IsLeader())
	cancel()
	requireClosed(t, done)
	require.Zero(t, session.unlocks.Load())
	require.Equal(t, int64(1), session.closes.Load())
}

func TestPostgresLeaderDropsLeadershipAfterSessionFailure(t *testing.T) {
	session := &fakeAdvisoryLockSession{acquire: true, pingErr: errors.New("connection lost")}
	leader := testPostgresLeader(session)
	require.NoError(t, leader.refreshLeadership(t.Context(), session))
	require.True(t, leader.IsLeader())
	require.ErrorContains(t, leader.refreshLeadership(t.Context(), session), "connection lost")
	leader.releaseSession(session)
	require.False(t, leader.IsLeader())
	require.Equal(t, int64(1), session.unlocks.Load())
	require.Equal(t, int64(1), session.closes.Load())
}

func testPostgresLeader(session advisoryLockSession) *PostgresLeader {
	return &PostgresLeader{
		connect:    func(context.Context) (advisoryLockSession, error) { return session, nil },
		instanceID: "instance-a",
		stopCh:     make(chan struct{}),
	}
}

func requireClosed(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("leader loop did not stop")
	}
}

type fakeAdvisoryLockSession struct {
	acquire  bool
	pingErr  error
	acquires atomic.Int64
	unlocks  atomic.Int64
	closes   atomic.Int64
}

func (s *fakeAdvisoryLockSession) TryAcquire(context.Context, int64) (bool, error) {
	s.acquires.Add(1)
	return s.acquire, nil
}

func (s *fakeAdvisoryLockSession) Ping(context.Context) error { return s.pingErr }
func (s *fakeAdvisoryLockSession) Unlock(context.Context, int64) error {
	s.unlocks.Add(1)
	return nil
}
func (s *fakeAdvisoryLockSession) Close() error {
	s.closes.Add(1)
	return nil
}
