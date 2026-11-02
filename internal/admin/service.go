package admin

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Deps are the collaborators of the Admin dashboard use case. Both are
// required; NewService rejects a missing one. The dashboard only reads, so
// it takes no identity fence or audit appender.
type Deps struct {
	Units   mutation.UnitOfWork[Store] // adminpostgres.NewUnits(db, ports)
	SpiceDB authz.AuthorizationDecisionChecker
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("admin dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Admin dashboard use case.
type Service struct {
	units   mutation.UnitOfWork[Store]
	spiceDB authz.AuthorizationDecisionChecker
}

// NewService validates deps and returns the Admin use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{units: deps.Units, spiceDB: deps.SpiceDB}, nil
}

// checkAdminAccess validates that the user has admin access.
// Returns an error if the user is not an admin or is banned.
func checkAdminAccess(ctx context.Context, spiceDB authz.AuthorizationDecisionChecker) error {
	user := auth.GetUser(ctx)
	if user == nil || user.Banned {
		return domainerr.AdminRequired()
	}
	can, err := policyv1.Platform.IsAdmin()
	if err != nil {
		return err
	}
	return authz.RequireAdminCan(ctx, spiceDB, can)
}

// DashboardStats returns aggregate statistics for the admin dashboard, read
// from one snapshot.
func (s *Service) DashboardStats(ctx context.Context) (DashboardStats, error) {
	if err := checkAdminAccess(ctx, s.spiceDB); err != nil {
		return DashboardStats{}, err
	}
	var stats DashboardStats
	err := s.units.Read(ctx, func(store Store) error {
		for _, count := range []struct {
			into  *int64
			count func(context.Context) (int64, error)
		}{
			{&stats.Members, store.CountActiveMembers},
			{&stats.Posts, store.CountPosts},
			{&stats.Pages, store.CountPages},
			{&stats.Comments, store.CountComments},
			{&stats.Campaigns, store.CountCampaigns},
		} {
			var err error
			if *count.into, err = count.count(ctx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return DashboardStats{}, err
	}
	return stats, nil
}
