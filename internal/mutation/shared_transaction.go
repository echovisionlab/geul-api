package mutation

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// sharedTransaction is the open transaction that mutations and reads under one
// context join. done is set once it has ended, so a callback still holding the
// context cannot use the finished transaction.
type sharedTransaction struct {
	db          *gorm.DB
	afterCommit []func()
	done        bool
}

type sharedTransactionKey struct{}

// InSharedTransaction runs work with every Run and Read under its context
// joining one transaction, so mutations of different domains commit or roll back
// together. Each joining mutation runs in its own savepoint and keeps its order,
// authorization and audit. Work should lock one aggregate's root: mutations that
// lock several roots in caller order can deadlock. A mutation that writes SpiceDB
// relationships cannot join, because its compensation needs its own commit.
func InSharedTransaction(ctx context.Context, db *gorm.DB, work func(context.Context) error) error {
	if joinedTransaction(ctx) != nil {
		return errors.New("shared transactions do not nest")
	}
	shared := &sharedTransaction{}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		shared.db = tx
		return work(context.WithValue(ctx, sharedTransactionKey{}, shared))
	})
	shared.done = true
	if err != nil {
		return err
	}
	for _, callback := range shared.afterCommit {
		callback()
	}
	return nil
}

// AfterCommit runs callback once the work it belongs to is committed: at the end
// of the shared transaction under ctx, or now when there is none, because then
// the mutation has already committed. It is dropped if the transaction rolls back.
func AfterCommit(ctx context.Context, callback func()) {
	if shared := joinedTransaction(ctx); shared != nil {
		shared.afterCommit = append(shared.afterCommit, callback)
		return
	}
	callback()
}

func joinedTransaction(ctx context.Context) *sharedTransaction {
	shared, _ := ctx.Value(sharedTransactionKey{}).(*sharedTransaction)
	if shared == nil || shared.done {
		return nil
	}
	return shared
}

// transactionBase is the handle a new transaction starts from under ctx: the
// shared transaction, which makes it a savepoint, or db.
func transactionBase(ctx context.Context, db *gorm.DB) *gorm.DB {
	if shared := joinedTransaction(ctx); shared != nil {
		return shared.db.WithContext(ctx)
	}
	return db.WithContext(ctx)
}
