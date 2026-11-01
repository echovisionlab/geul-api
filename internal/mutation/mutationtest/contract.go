package mutationtest

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"gorm.io/gorm"
)

// Harness is one Store implementation a store contract runs against: the
// in-memory fake in a unit test, the PostgreSQL store in an integration
// test. A contract opens a fresh Harness for each case.
type Harness[S any] struct {
	Units mutation.UnitOfWork[S]
	// Waiting reports how many transactions wait for a lock another holds.
	Waiting func(t *testing.T) int
}

// FakeHarness is the Harness of an in-memory store.
func FakeHarness[S any](store S) Harness[S] {
	units := New(store)
	return Harness[S]{Units: units, Waiting: func(*testing.T) int { return units.Waiting() }}
}

// PostgresHarness is the Harness of a PostgreSQL store: units over db, and
// the lock waiters of db's own database.
func PostgresHarness[S any](db *gorm.DB, units mutation.UnitOfWork[S]) Harness[S] {
	return Harness[S]{Units: units, Waiting: func(t *testing.T) int {
		t.Helper()
		var waiting int
		if err := db.Raw(`SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE NOT l.granted AND a.datname = current_database()`).Scan(&waiting).Error; err != nil {
			t.Fatalf("count lock waiters: %v", err)
		}
		return waiting
	}}
}

// RunContract runs every case against a fresh Harness, in parallel. A
// domain's <domain>test package defines its cases once and calls RunContract
// from the fake's unit test and from the PostgreSQL store's integration test.
func RunContract[S any](t *testing.T, open func(t *testing.T) Harness[S], cases map[string]func(t *testing.T, h Harness[S])) {
	t.Helper()
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run(t, open(t))
		})
	}
}

// Transact runs work in one transaction of h, without the pipeline's steps.
func Transact[S any](t *testing.T, h Harness[S], work func(ctx context.Context, store S) error) error {
	t.Helper()
	return h.Units.Transaction(t.Context(), false, func(tx mutation.Tx[S]) error {
		return work(t.Context(), tx.Store())
	})
}

// Read runs read in one read snapshot of h.
func Read[S any](t *testing.T, h Harness[S], read func(ctx context.Context, store S) error) error {
	t.Helper()
	return h.Units.Read(t.Context(), func(store S) error { return read(t.Context(), store) })
}

// RequireSecondWaits checks a row lock: lock runs in a first transaction,
// which then starts second in another transaction, waits until that one
// waits on a lock, and runs then before it commits. It returns second's
// error once second finished, so the caller asserts what second saw of the
// first commit.
func RequireSecondWaits[S any](
	t *testing.T,
	h Harness[S],
	lock func(ctx context.Context, store S) error,
	then func(ctx context.Context, store S) error,
	second func(ctx context.Context, store S) error,
) error {
	t.Helper()
	done := make(chan error, 1)
	err := Transact(t, h, func(ctx context.Context, store S) error {
		if err := lock(ctx, store); err != nil {
			return err
		}
		go func() { done <- Transact(t, h, second) }()
		deadline := time.Now().Add(10 * time.Second)
		for h.Waiting(t) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("the second transaction never waited for the lock")
			}
			time.Sleep(5 * time.Millisecond)
		}
		return then(ctx, store)
	})
	if err != nil {
		t.Fatalf("first transaction: %v", err)
	}
	return <-done
}
