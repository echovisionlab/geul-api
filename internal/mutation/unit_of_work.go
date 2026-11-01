package mutation

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"gorm.io/gorm"
)

// UnitOfWork opens the transactions of a domain and binds the domain's store
// S to each of them. Run is its only caller for mutations; a use case reads
// through Read. Postgres is the production unit of work, and
// mutationtest.UnitOfWork runs a fake store in memory.
type UnitOfWork[S any] interface {
	// Transaction runs work in one read-committed transaction, after the
	// global authorization lock when lockAuthorization is set, and commits
	// when work returns nil.
	Transaction(ctx context.Context, lockAuthorization bool, work func(Tx[S]) error) error
	// RelationshipTransaction runs work in one transaction that holds the
	// global authorization lock, through authzmutation.Execute: work calls
	// write exactly once, and the batch is compensated through writer if
	// the transaction rolls back.
	RelationshipTransaction(ctx context.Context, writer authzmutation.RelationshipWriter, work func(Tx[S], authzmutation.WriteRelationships) error) error
	// Read runs read in one repeatable-read, read-only snapshot (Read).
	Read(ctx context.Context, read func(S) error) error
	// ReadLocked runs read in one repeatable-read snapshot that may take
	// row locks (ReadLocked).
	ReadLocked(ctx context.Context, read func(S) error) error
}

// Tx is one open transaction of a unit of work: the store bound to it, the
// gorm transaction the shared steps (identity fence, audit appender) take,
// which is nil in memory where their fakes ignore it, and an observer of
// Run's steps.
type Tx[S any] struct {
	store    S
	db       *gorm.DB
	observer func(Step)
}

// NewTx returns the Tx a unit of work hands Run. observer may be nil.
func NewTx[S any](store S, db *gorm.DB, observer func(Step)) Tx[S] {
	return Tx[S]{store: store, db: db, observer: observer}
}

// Store returns the store bound to the transaction, for a store contract
// test that drives a unit of work directly; a use case gets it from Run.
func (tx Tx[S]) Store() S {
	return tx.store
}

func (tx Tx[S]) observe(step Step) {
	if tx.observer != nil {
		tx.observer(step)
	}
}

// Step is one step of a mutation, as an in-memory unit of work records it.
type Step string

// The steps of a mutation in the order Run takes them, and the end of its
// transaction.
const (
	StepAuthorizationLock Step = "authorization lock"
	StepSubjects          Step = "subjects"
	StepLock              Step = "lock"
	StepAccess            Step = "access"
	StepChange            Step = "change"
	StepAudit             Step = "audit"
	StepOutbox            Step = "outbox"
	StepRelationships     Step = "relationships"
	StepCommit            Step = "commit"
	StepRollback          Step = "rollback"
)

// Postgres is the production unit of work over db: the same transactions,
// global authorization lock and isolation Run always opened, with bind
// turning each transaction into the domain's store.
func Postgres[S any](db *gorm.DB, bind func(tx *gorm.DB) S) UnitOfWork[S] {
	return postgres[S]{db: db, bind: bind}
}

type postgres[S any] struct {
	db   *gorm.DB
	bind func(*gorm.DB) S
}

func (p postgres[S]) Transaction(ctx context.Context, lockAuthorization bool, work func(Tx[S]) error) error {
	return p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if lockAuthorization {
			if err := authzmutation.LockTransaction(tx); err != nil {
				return err
			}
		}
		return work(NewTx(p.bind(tx), tx, nil))
	})
}

func (p postgres[S]) RelationshipTransaction(
	ctx context.Context,
	writer authzmutation.RelationshipWriter,
	work func(Tx[S], authzmutation.WriteRelationships) error,
) error {
	_, err := authzmutation.Execute(ctx, p.db, writer, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		return work(NewTx(p.bind(tx), tx, nil), write)
	})
	return err
}

func (p postgres[S]) Read(ctx context.Context, read func(S) error) error {
	return Read(ctx, p.db, func(tx *gorm.DB) error { return read(p.bind(tx)) })
}

func (p postgres[S]) ReadLocked(ctx context.Context, read func(S) error) error {
	return ReadLocked(ctx, p.db, func(tx *gorm.DB) error { return read(p.bind(tx)) })
}
