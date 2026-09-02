package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	postgresLeaderLockID   int64 = 4923378199947301971
	leaderElectionInterval       = 15 * time.Second
)

// LeaderElector defines the interface for leader election.
type LeaderElector interface {
	IsLeader() bool
	Start(ctx context.Context)
	Stop()
}

type advisoryLockSession interface {
	TryAcquire(context.Context, int64) (bool, error)
	Ping(context.Context) error
	Unlock(context.Context, int64) error
	Close() error
}

type sqlAdvisoryLockSession struct {
	conn *sql.Conn
}

func (s *sqlAdvisoryLockSession) TryAcquire(ctx context.Context, lockID int64) (bool, error) {
	var acquired bool
	err := s.conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&acquired)
	return acquired, err
}

func (s *sqlAdvisoryLockSession) Ping(ctx context.Context) error {
	return s.conn.PingContext(ctx)
}

func (s *sqlAdvisoryLockSession) Unlock(ctx context.Context, lockID int64) error {
	var released bool
	if err := s.conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", lockID).Scan(&released); err != nil {
		return err
	}
	if !released {
		return errors.New("scheduler advisory lock was not held by this session")
	}
	return nil
}

func (s *sqlAdvisoryLockSession) Close() error {
	return s.conn.Close()
}

// PostgresLeader holds one session-level advisory lock on a dedicated
// database/sql connection. PostgreSQL releases the lock if the process or
// connection dies, so there is no lease row or renewal state to reconcile.
type PostgresLeader struct {
	connect    func(context.Context) (advisoryLockSession, error)
	instanceID string
	isLeader   atomic.Bool
	stopCh     chan struct{}
	stopOnce   sync.Once
}

func NewPostgresLeader(db *sql.DB, instanceID string) *PostgresLeader {
	if db == nil {
		panic("scheduler: database cannot be nil for PostgresLeader")
	}
	return &PostgresLeader{
		connect: func(ctx context.Context) (advisoryLockSession, error) {
			conn, err := db.Conn(ctx)
			if err != nil {
				return nil, err
			}
			return &sqlAdvisoryLockSession{conn: conn}, nil
		},
		instanceID: instanceID,
		stopCh:     make(chan struct{}),
	}
}

func (l *PostgresLeader) Start(ctx context.Context) {
	ticker := time.NewTicker(leaderElectionInterval)
	defer ticker.Stop()

	var session advisoryLockSession
	defer func() { l.releaseSession(session) }()

	for {
		if session == nil {
			var err error
			session, err = l.connect(ctx)
			if err != nil {
				slog.Warn("Failed to open scheduler leader session", "error", err)
			}
		}
		if session != nil {
			if err := l.refreshLeadership(ctx, session); err != nil {
				slog.Warn("Scheduler leader session failed", "error", err)
				// sql.Conn.Close returns a healthy physical session to the pool.
				// Release first so a cancellable Ping error cannot leak the
				// session-level advisory lock into an unrelated pool borrower.
				l.releaseSession(session)
				session = nil
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-l.stopCh:
			return
		case <-ticker.C:
		}
	}
}

func (l *PostgresLeader) refreshLeadership(ctx context.Context, session advisoryLockSession) error {
	if l.isLeader.Load() {
		return session.Ping(ctx)
	}
	acquired, err := session.TryAcquire(ctx, postgresLeaderLockID)
	if err != nil {
		return err
	}
	l.isLeader.Store(acquired)
	if acquired {
		slog.Info("Acquired scheduler leader lock", "instance", l.instanceID)
	}
	return nil
}

func (l *PostgresLeader) releaseSession(session advisoryLockSession) {
	wasLeader := l.isLeader.Swap(false)
	if session == nil {
		return
	}
	if wasLeader {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := session.Unlock(ctx, postgresLeaderLockID); err != nil {
			slog.Warn("Failed to release scheduler leader lock", "instance", l.instanceID, "error", err)
		} else {
			slog.Info("Released scheduler leader lock", "instance", l.instanceID)
		}
	}
	_ = session.Close()
}

func (l *PostgresLeader) Stop() {
	l.stopOnce.Do(func() { close(l.stopCh) })
}

func (l *PostgresLeader) IsLeader() bool {
	return l.isLeader.Load()
}
