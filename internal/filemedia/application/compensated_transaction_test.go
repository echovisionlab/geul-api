package application

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type workerBoundaryConnPool struct {
	gorm.ConnPool
	mu          sync.Mutex
	events      []string
	commitPanic bool
	transaction *workerBoundaryTransaction
}

func (pool *workerBoundaryConnPool) BeginTx(
	ctx context.Context,
	options *sql.TxOptions,
) (gorm.ConnPool, error) {
	beginner, ok := pool.ConnPool.(gorm.TxBeginner)
	if !ok {
		return nil, gorm.ErrInvalidTransaction
	}
	tx, err := beginner.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	pool.transaction = &workerBoundaryTransaction{ConnPool: tx, transaction: tx, owner: pool}
	return pool.transaction, nil
}

func (pool *workerBoundaryConnPool) record(event string) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.events = append(pool.events, event)
}

func (pool *workerBoundaryConnPool) recordedEvents() []string {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return append([]string(nil), pool.events...)
}

type workerBoundaryTransaction struct {
	gorm.ConnPool
	transaction *sql.Tx
	owner       *workerBoundaryConnPool
}

func (transaction *workerBoundaryTransaction) Commit() error {
	transaction.owner.record("commit")
	if transaction.owner.commitPanic {
		panic("forced worker commit panic")
	}
	return transaction.transaction.Commit()
}

func (transaction *workerBoundaryTransaction) Rollback() error {
	transaction.owner.record("rollback")
	return transaction.transaction.Rollback()
}

func newWorkerBoundaryTestDB(t *testing.T, commitPanic bool) (*gorm.DB, *workerBoundaryConnPool) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE worker_boundary_test (id TEXT PRIMARY KEY)").Error)
	pool := &workerBoundaryConnPool{ConnPool: db.Statement.ConnPool, commitPanic: commitPanic}
	db.Statement.ConnPool = pool
	db.Config.ConnPool = pool
	return db, pool
}

func TestExecuteWorkerCompensatedTransactionCompensatesBeforeRollback(t *testing.T) {
	db, pool := newWorkerBoundaryTestDB(t, false)
	callbackErr := errors.New("finalization failed")

	err := executeCompensatedTransaction(t.Context(), db, func(tx *gorm.DB, register registerTransactionCompensation) error {
		require.NoError(t, tx.Exec("INSERT INTO worker_boundary_test (id) VALUES (?)", "rollback").Error)
		require.NoError(t, register(time.Now(), func(context.Context) error {
			pool.record("compensate")
			return nil
		}))
		return callbackErr
	})
	require.ErrorIs(t, err, callbackErr)
	require.Equal(t, []string{"compensate", "rollback"}, pool.recordedEvents())

	var count int64
	require.NoError(t, db.Table("worker_boundary_test").Where("id = ?", "rollback").Count(&count).Error)
	require.Zero(t, count)
}

func TestExecuteWorkerCompensatedTransactionPanicCompensatesBeforeRollback(t *testing.T) {
	db, pool := newWorkerBoundaryTestDB(t, false)
	panicValue := captureWorkerTransactionPanic(func() {
		_ = executeCompensatedTransaction(t.Context(), db, func(tx *gorm.DB, register registerTransactionCompensation) error {
			require.NoError(t, tx.Exec("INSERT INTO worker_boundary_test (id) VALUES (?)", "panic").Error)
			require.NoError(t, register(time.Now(), func(context.Context) error {
				pool.record("compensate")
				return nil
			}))
			panic("forced callback panic")
		})
	})
	require.Equal(t, "forced callback panic", panicValue)
	require.Equal(t, []string{"compensate", "rollback"}, pool.recordedEvents())

	var count int64
	require.NoError(t, db.Table("worker_boundary_test").Where("id = ?", "panic").Count(&count).Error)
	require.Zero(t, count)
}

func TestExecuteWorkerCompensatedTransactionCommitPanicDoesNotCompensateOrRollback(t *testing.T) {
	db, pool := newWorkerBoundaryTestDB(t, true)
	panicValue := captureWorkerTransactionPanic(func() {
		_ = executeCompensatedTransaction(t.Context(), db, func(_ *gorm.DB, register registerTransactionCompensation) error {
			return register(time.Now(), func(context.Context) error {
				pool.record("compensate")
				return nil
			})
		})
	})
	panicErr, ok := panicValue.(error)
	require.True(t, ok, "panic value = %#v", panicValue)
	require.ErrorContains(t, panicErr, "forced worker commit panic")
	require.ErrorContains(t, panicErr, "commit outcome is uncertain")
	require.Equal(t, []string{"commit"}, pool.recordedEvents())
	require.NotNil(t, pool.transaction)
	require.NoError(t, pool.transaction.transaction.Rollback())
}

func captureWorkerTransactionPanic(run func()) (panicValue any) {
	defer func() { panicValue = recover() }()
	run()
	return nil
}
