// Package mutationtest runs a domain's fake store under mutation.Run in
// memory. Its UnitOfWork serializes transactions like one connection, rolls
// the store back when a transaction fails, compensates a relationship batch
// written before the failure, and records the steps Run took, so a use-case
// test asserts order and rollback without PostgreSQL. Store contract tests
// keep SQL, constraints and row locks honest against PostgreSQL.
package mutationtest

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Checkpoint holds a fake's independent persisted snapshot and its rollback.
// State excludes locks, callbacks, failure injection and call observations.
type Checkpoint struct {
	State   any
	Restore func()
}

// Savepointer captures the persisted state a read compares and a failed
// transaction restores. Each fake selects its persisted fields explicitly.
type Savepointer interface {
	Savepoint() Checkpoint
}

// ErrReadOnly reports a write made inside a read-only snapshot.
var ErrReadOnly = errors.New("mutationtest: read-only transaction changed persisted state")

// UnitOfWork implements mutation.UnitOfWork over one fake store.
type UnitOfWork[S any] struct {
	store     S
	savepoint func() Checkpoint

	mu      sync.Mutex
	waiting atomic.Int32
	stepsMu sync.Mutex
	steps   []mutation.Step
}

// New returns a unit of work over store, which must implement Savepointer.
func New[S any](store S) *UnitOfWork[S] {
	savepointer, ok := any(store).(Savepointer)
	if !ok {
		panic("mutationtest: the fake store must implement Savepointer")
	}
	return &UnitOfWork[S]{store: store, savepoint: savepointer.Savepoint}
}

// Store returns the fake store, for a test to seed and inspect.
func (u *UnitOfWork[S]) Store() S {
	return u.store
}

// Steps returns the steps of every transaction so far, in order, each
// ending with StepCommit or StepRollback.
func (u *UnitOfWork[S]) Steps() []mutation.Step {
	u.stepsMu.Lock()
	defer u.stepsMu.Unlock()
	return slices.Clone(u.steps)
}

// Waiting reports how many transactions wait for the one in progress, for a
// store contract test that checks a lock blocks a second writer.
func (u *UnitOfWork[S]) Waiting() int {
	return int(u.waiting.Load())
}

func (u *UnitOfWork[S]) record(step mutation.Step) {
	u.stepsMu.Lock()
	defer u.stepsMu.Unlock()
	u.steps = append(u.steps, step)
}

func (u *UnitOfWork[S]) begin() func() {
	u.waiting.Add(1)
	u.mu.Lock()
	u.waiting.Add(-1)
	return u.mu.Unlock
}

// Transaction implements mutation.UnitOfWork.
func (u *UnitOfWork[S]) Transaction(_ context.Context, lockAuthorization bool, work func(mutation.Tx[S]) error) error {
	defer u.begin()()
	if lockAuthorization {
		u.record(mutation.StepAuthorizationLock)
	}
	rollback := u.savepoint().Restore
	if err := work(mutation.NewTx(u.store, nil, u.record)); err != nil {
		rollback()
		u.record(mutation.StepRollback)
		return err
	}
	u.record(mutation.StepCommit)
	return nil
}

// RelationshipTransaction implements mutation.UnitOfWork with the contract
// of authzmutation.Execute: one write per transaction, compensated when the
// transaction fails after it.
func (u *UnitOfWork[S]) RelationshipTransaction(
	ctx context.Context,
	writer authzmutation.RelationshipWriter,
	work func(mutation.Tx[S], authzmutation.WriteRelationships) error,
) error {
	defer u.begin()()
	u.record(mutation.StepAuthorizationLock)
	rollback := u.savepoint().Restore
	var written, compensation []policyv1.RelationshipMutation
	writes := 0
	write := func(apply, compensate []policyv1.RelationshipMutation) error {
		writes++
		if writes > 1 {
			return errors.New("authorization relationships may be written only once per database transaction")
		}
		if len(apply) == 0 || len(compensate) == 0 {
			return errors.New("authorization relationship mutations and compensation are required")
		}
		if _, err := writer.ApplyRelationshipsExpecting(ctx, apply, compensate); err != nil {
			return err
		}
		written, compensation = apply, compensate
		return nil
	}
	err := work(mutation.NewTx(u.store, nil, u.record), write)
	if err == nil && written == nil {
		err = errors.New("authorization mutation callback must complete one relationship write")
	}
	if err != nil {
		if written != nil {
			if _, compensateErr := writer.CompensateRelationshipsExpecting(context.WithoutCancel(ctx), compensation, written); compensateErr != nil {
				err = errors.Join(err, compensateErr)
			}
		}
		rollback()
		u.record(mutation.StepRollback)
		return err
	}
	u.record(mutation.StepCommit)
	return nil
}

// Read implements mutation.UnitOfWork.
func (u *UnitOfWork[S]) Read(_ context.Context, read func(S) error) error {
	return u.read(true, read)
}

// ReadLocked implements mutation.UnitOfWork; the fake takes no lock.
func (u *UnitOfWork[S]) ReadLocked(_ context.Context, read func(S) error) error {
	return u.read(false, read)
}

// ReadCurrent implements mutation.UnitOfWork; the fake takes no lock.
func (u *UnitOfWork[S]) ReadCurrent(_ context.Context, read func(S) error) error {
	return u.read(false, read)
}

func (u *UnitOfWork[S]) read(readOnly bool, read func(S) error) (err error) {
	defer u.begin()()
	before := u.savepoint()
	finished := false
	defer func() {
		if !finished {
			before.Restore()
		}
	}()
	err = read(u.store)
	if readOnly && !reflect.DeepEqual(before.State, u.savepoint().State) {
		if err == nil {
			err = ErrReadOnly
		} else {
			err = errors.Join(err, ErrReadOnly)
		}
	}
	if err != nil {
		before.Restore()
	}
	finished = true
	return err
}
