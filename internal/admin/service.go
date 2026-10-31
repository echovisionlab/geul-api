package admin

import (
	"context"
	"math"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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

type Service struct {
	OG
	db      *gorm.DB
	spiceDB *auth.SpiceDBClient
}

func NewService(
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	ogService OG,
) *Service {
	if db == nil {
		panic("db is required")
	}
	if spiceDB == nil {
		panic("spiceDB is required")
	}
	if ogService == nil {
		panic("OG admin service is required")
	}
	return &Service{
		OG:      ogService,
		db:      db,
		spiceDB: spiceDB,
	}
}

// checkAdminAccess validates that the user has admin access.
// Returns an error if the user is not an admin or is banned.
func checkAdminAccess(ctx context.Context, spiceDB *auth.SpiceDBClient) error {
	user := auth.GetUser(ctx)
	if user == nil || user.Banned {
		return errs.AdminRequired()
	}
	can, err := policyv1.Platform.IsAdmin()
	if err != nil {
		return errs.Internal(err)
	}
	return authz.RequireAdminCan(ctx, spiceDB, can)
}

// GetDashboardStats returns aggregate statistics for the admin dashboard
func (s *Service) GetDashboardStats(
	ctx context.Context,
	req *connect.Request[managev1.GetDashboardStatsRequest],
) (*connect.Response[managev1.GetDashboardStatsResponse], error) {
	if err := checkAdminAccess(ctx, s.spiceDB); err != nil {
		return nil, err
	}

	var memberCount int64
	if err := s.db.Raw(`
		SELECT COUNT(*)
		FROM member
		JOIN kratos.identities AS identity
		  ON identity.id = member.account_identity_id
		WHERE member.deleted_at IS NULL
		  AND identity.state = ?
		  AND COALESCE((identity.metadata_admin->>'banned')::boolean, FALSE) = FALSE
	`, auth.KratosStateActive).Scan(&memberCount).Error; err != nil {
		return nil, errs.Internal(err)
	}

	var postCount int64
	if err := s.db.Table("post").Count(&postCount).Error; err != nil {
		return nil, errs.Internal(err)
	}

	var pageCount int64
	if err := s.db.Table("page").Count(&pageCount).Error; err != nil {
		return nil, errs.Internal(err)
	}

	var commentCount int64
	if err := s.db.Table("comment").Where("is_deleted = false").Count(&commentCount).Error; err != nil {
		return nil, errs.Internal(err)
	}

	var campaignCount int64
	if err := s.db.Table("campaign").Count(&campaignCount).Error; err != nil {
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(&managev1.GetDashboardStatsResponse{
		Stats: &managev1.DashboardStats{
			TotalMembers:   safeInt32(memberCount),
			TotalPosts:     safeInt32(postCount),
			TotalPages:     safeInt32(pageCount),
			TotalComments:  safeInt32(commentCount),
			TotalCampaigns: safeInt32(campaignCount),
		},
	}), nil
}
