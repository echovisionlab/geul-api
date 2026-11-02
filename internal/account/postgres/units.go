package accountpostgres

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/postgreslock"
)

// leaderLockKeys are the session advisory lock keys of the Account
// reconcilers.
var leaderLockKeys = map[account.Leader]int64{
	account.LeaderLifecycle:   int64(0x4745554c41434354), // GEULACCT
	account.LeaderEmailChange: int64(0x4745554c454d4149), // GUELEMAI
}

// NewUnits is the production unit of work of the Account use cases.
func NewUnits(db *gorm.DB, ports Ports) (account.Units, error) {
	if db == nil {
		return nil, errors.New("account database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return newUnits(db, ports), nil
}

// NewEmailProjectionUnits is the unit of work of a process that only keeps
// the Member email projection in step with Kratos (the backfill command).
func NewEmailProjectionUnits(db *gorm.DB, memberEmails MemberEmails) (account.Units, error) {
	if db == nil || memberEmails == nil {
		return nil, errors.New("account database and Member email projection are required")
	}
	return newUnits(db, Ports{MemberEmails: memberEmails}), nil
}

// newUnits binds ports as given, for the worker entry points that reach only
// some of them.
func newUnits(db *gorm.DB, ports Ports) account.Units {
	return units{
		UnitOfWork: mutation.Postgres(db, func(tx *gorm.DB) account.Store { return store{tx: tx, ports: ports} }),
		db:         db, ports: ports,
	}
}

type units struct {
	mutation.UnitOfWork[account.Store]
	db    *gorm.DB
	ports Ports
}

// Fenced holds the session form of the Identity fence (and the global
// authorization lock) on one dedicated connection through
// identitystate.WithMutationSteps; each step is a transaction on that
// connection.
func (u units) Fenced(ctx context.Context, identityID string, work func(context.Context, account.Steps) error) error {
	return identitystate.WithMutationSteps(ctx, u.db, identityID, func(mutationCtx context.Context, step identitystate.Step) error {
		return work(mutationCtx, steps{step: step, ports: u.ports})
	})
}

// Lead runs work under the reconciler's session advisory leader lock.
func (u units) Lead(ctx context.Context, leader account.Leader, work func(context.Context) error) error {
	key, known := leaderLockKeys[leader]
	if !known {
		return fmt.Errorf("unknown account leader %q", leader)
	}
	return postgreslock.WithAdvisoryLeader(ctx, u.db, key, work)
}

type steps struct {
	step  identitystate.Step
	ports Ports
}

func (s steps) Step(ctx context.Context, work func(account.Store) error) error {
	return s.step(ctx, func(tx *gorm.DB) error { return work(store{tx: tx, ports: s.ports}) })
}
