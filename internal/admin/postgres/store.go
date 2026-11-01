// Package adminpostgres binds the Admin dashboard's Store to one PostgreSQL
// read snapshot: every count is read by the adapter of the domain that owns
// the rows.
package adminpostgres

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/admin"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// Members counts the dashboard's Members (memberadapter.DashboardMembers).
type Members interface {
	CountActive(ctx context.Context, db *gorm.DB) (int64, error)
}

// Posts counts the dashboard's Posts and comments
// (postadapter.DashboardPosts).
type Posts interface {
	CountPosts(ctx context.Context, db *gorm.DB) (int64, error)
	CountComments(ctx context.Context, db *gorm.DB) (int64, error)
}

// Pages counts the dashboard's Pages (pageadapter.DashboardPages).
type Pages interface {
	CountPages(ctx context.Context, db *gorm.DB) (int64, error)
}

// Campaigns counts the dashboard's Campaigns
// (campaignadapter.DashboardCampaigns).
type Campaigns interface {
	CountCampaigns(ctx context.Context, db *gorm.DB) (int64, error)
}

// Ports are the owners' adapters the Store reads through in the snapshot.
type Ports struct {
	Members   Members
	Posts     Posts
	Pages     Pages
	Campaigns Campaigns
}

func (p Ports) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Members": p.Members == nil, "Posts": p.Posts == nil, "Pages": p.Pages == nil, "Campaigns": p.Campaigns == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("admin store ports are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the production unit of work of the Admin use cases.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[admin.Store], error) {
	if db == nil {
		return nil, errors.New("admin store database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) admin.Store { return store{tx: tx, ports: ports} }), nil
}

type store struct {
	tx    *gorm.DB
	ports Ports
}

func (s store) CountActiveMembers(ctx context.Context) (int64, error) {
	return s.ports.Members.CountActive(ctx, s.tx)
}

func (s store) CountPosts(ctx context.Context) (int64, error) {
	return s.ports.Posts.CountPosts(ctx, s.tx)
}

func (s store) CountPages(ctx context.Context) (int64, error) {
	return s.ports.Pages.CountPages(ctx, s.tx)
}

func (s store) CountComments(ctx context.Context) (int64, error) {
	return s.ports.Posts.CountComments(ctx, s.tx)
}

func (s store) CountCampaigns(ctx context.Context) (int64, error) {
	return s.ports.Campaigns.CountCampaigns(ctx, s.tx)
}
