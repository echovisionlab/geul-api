package admin

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// safeInt32 safely converts int64 to int32 with overflow protection.
// Returns math.MaxInt32 if the value exceeds int32 range.
func safeInt32(n int64) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < math.MinInt32 {
		return math.MinInt32
	}
	return int32(n)
}

// Deps are the collaborators of the Admin dashboard use case. Every field is
// required; NewService rejects a missing one.
type Deps struct {
	DB        *gorm.DB
	SpiceDB   *auth.SpiceDBClient
	Members   Members
	Posts     Posts
	Pages     Pages
	Campaigns Campaigns
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Members": d.Members == nil,
		"Posts": d.Posts == nil, "Pages": d.Pages == nil, "Campaigns": d.Campaigns == nil,
	} {
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
	db        *gorm.DB
	spiceDB   *auth.SpiceDBClient
	members   Members
	posts     Posts
	pages     Pages
	campaigns Campaigns
}

// NewService validates deps and returns the Admin use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, members: deps.Members,
		posts: deps.Posts, pages: deps.Pages, campaigns: deps.Campaigns,
	}, nil
}

// checkAdminAccess validates that the user has admin access.
// Returns an error if the user is not an admin or is banned.
func checkAdminAccess(ctx context.Context, spiceDB *auth.SpiceDBClient) error {
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
func (s *Service) DashboardStats(ctx context.Context) (*managev1.DashboardStats, error) {
	if err := checkAdminAccess(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	var members, posts, pages, comments, campaigns int64
	err := mutation.Read(ctx, s.db, func(tx *gorm.DB) error {
		for _, count := range []struct {
			into  *int64
			count func(context.Context, *gorm.DB) (int64, error)
		}{
			{&members, s.members.CountActive},
			{&posts, s.posts.CountPosts},
			{&pages, s.pages.CountPages},
			{&comments, s.posts.CountComments},
			{&campaigns, s.campaigns.CountCampaigns},
		} {
			var err error
			if *count.into, err = count.count(ctx, tx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &managev1.DashboardStats{
		TotalMembers:   safeInt32(members),
		TotalPosts:     safeInt32(posts),
		TotalPages:     safeInt32(pages),
		TotalComments:  safeInt32(comments),
		TotalCampaigns: safeInt32(campaigns),
	}, nil
}
