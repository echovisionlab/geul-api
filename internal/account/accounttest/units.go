package accounttest

import (
	"context"
	"slices"
	"sync"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// Units is the fake account.Units: the mutationtest unit of work, a fence
// that serializes the fenced flows as the global authorization lock does,
// and a leader lock the test can withhold.
type Units struct {
	*mutationtest.UnitOfWork[account.Store]

	fence sync.Mutex
	mu    sync.Mutex
	// fenced lists the Identity of every fenced flow, in order.
	fenced []string
	// led lists the leader of every reconciler run, in order.
	led       []account.Leader
	notLeader map[account.Leader]bool
	// fencedSteps counts the steps of every fenced flow, in order.
	fencedSteps []int
}

var _ account.Units = (*Units)(nil)

func newUnits(store *Store) *Units {
	return &Units{UnitOfWork: mutationtest.New[account.Store](store), notLeader: map[account.Leader]bool{}}
}

// Fenced implements account.Units: work runs while the fence is held, and
// each step is one transaction of the unit of work.
func (u *Units) Fenced(ctx context.Context, identityID string, work func(context.Context, account.Steps) error) error {
	u.fence.Lock()
	defer u.fence.Unlock()
	u.mu.Lock()
	u.fenced = append(u.fenced, identityID)
	u.fencedSteps = append(u.fencedSteps, 0)
	index := len(u.fencedSteps) - 1
	u.mu.Unlock()
	return work(ctx, steps{units: u, index: index})
}

// Lead implements account.Units; it runs work unless the test withheld the
// leader lock.
func (u *Units) Lead(ctx context.Context, leader account.Leader, work func(context.Context) error) error {
	u.mu.Lock()
	withheld := u.notLeader[leader]
	if !withheld {
		u.led = append(u.led, leader)
	}
	u.mu.Unlock()
	if withheld {
		return nil
	}
	return work(ctx)
}

// WithholdLeader makes another process hold the leader lock of leader.
func (u *Units) WithholdLeader(leader account.Leader) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.notLeader[leader] = true
}

// FencedIdentities returns the Identity of every fenced flow so far, in
// order.
func (u *Units) FencedIdentities() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.fenced)
}

// FencedSteps returns the step count of every fenced flow so far, in order.
func (u *Units) FencedSteps() []int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.fencedSteps)
}

// Led returns the leader of every reconciler run so far, in order.
func (u *Units) Led() []account.Leader {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.led)
}

type steps struct {
	units *Units
	index int
}

func (s steps) Step(ctx context.Context, work func(account.Store) error) error {
	s.units.mu.Lock()
	s.units.fencedSteps[s.index]++
	s.units.mu.Unlock()
	return s.units.Transaction(ctx, false, func(tx mutation.Tx[account.Store]) error { return work(tx.Store()) })
}
