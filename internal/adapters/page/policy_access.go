package page

import (
	"context"

	"gorm.io/gorm"

	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	pagepostgres "github.com/echovisionlab/geul-api/internal/page/postgres"
)

// PolicyAccess delegates relation policy authorization to the Page owner,
// in the caller's transaction.
type PolicyAccess struct {
	authority *pagedomain.PolicyAuthority
}

func NewPolicyAccess(authority *pagedomain.PolicyAuthority) *PolicyAccess {
	if authority == nil {
		panic("Page policy authority is required")
	}
	return &PolicyAccess{authority: authority}
}

func (a *PolicyAccess) RequireLockedView(ctx context.Context, tx *gorm.DB, pageID string) error {
	return a.authority.RequireLockedView(ctx, pagepostgres.NewStore(tx, pagepostgres.Ports{}), pageID)
}

func (a *PolicyAccess) RequireLockedEdit(ctx context.Context, tx *gorm.DB, pageID string) error {
	return a.authority.RequireLockedEdit(ctx, pagepostgres.NewStore(tx, pagepostgres.Ports{}), pageID)
}
