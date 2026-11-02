// Package admintest provides the in-memory Admin dashboard Store and its
// unit of work.
package admintest

import (
	"context"
	"sync"

	"github.com/echovisionlab/geul-api/internal/admin"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// Store implements admin.Store over fixed totals, or fails every count with
// Err.
type Store struct {
	Err error

	mu     sync.Mutex
	totals admin.DashboardStats
}

var _ admin.Store = (*Store)(nil)

// New returns a Store with totals, and the unit of work over it.
func New(totals admin.DashboardStats) (*Store, *mutationtest.UnitOfWork[admin.Store]) {
	store := &Store{totals: totals}
	return store, mutationtest.New[admin.Store](store)
}

// Savepoint implements mutationtest.Savepointer; the dashboard writes
// nothing to restore.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	return mutationtest.Checkpoint{State: struct{}{}, Restore: func() {}}
}

func (s *Store) count(pick func(admin.DashboardStats) int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return 0, s.Err
	}
	return pick(s.totals), nil
}

func (s *Store) CountActiveMembers(context.Context) (int64, error) {
	return s.count(func(totals admin.DashboardStats) int64 { return totals.Members })
}

func (s *Store) CountPosts(context.Context) (int64, error) {
	return s.count(func(totals admin.DashboardStats) int64 { return totals.Posts })
}

func (s *Store) CountPages(context.Context) (int64, error) {
	return s.count(func(totals admin.DashboardStats) int64 { return totals.Pages })
}

func (s *Store) CountComments(context.Context) (int64, error) {
	return s.count(func(totals admin.DashboardStats) int64 { return totals.Comments })
}

func (s *Store) CountCampaigns(context.Context) (int64, error) {
	return s.count(func(totals admin.DashboardStats) int64 { return totals.Campaigns })
}
