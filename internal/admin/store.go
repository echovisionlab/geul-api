package admin

import "context"

// Store is what the dashboard reads, bound to one read snapshot. Every count
// is another domain's: Member, Post, Page and Campaign own the rows.
type Store interface {
	// CountActiveMembers counts the live Members whose linked Identity is
	// active and not banned.
	CountActiveMembers(ctx context.Context) (int64, error)
	CountPosts(ctx context.Context) (int64, error)
	CountPages(ctx context.Context) (int64, error)
	// CountComments counts the comments that are not deleted.
	CountComments(ctx context.Context) (int64, error)
	CountCampaigns(ctx context.Context) (int64, error)
}

// DashboardStats are the dashboard's totals.
type DashboardStats struct {
	Members   int64
	Posts     int64
	Pages     int64
	Comments  int64
	Campaigns int64
}
