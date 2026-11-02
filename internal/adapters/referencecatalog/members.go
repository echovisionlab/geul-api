// Package referencecatalogadapter contains composition-only adapters for the
// Reference Catalog domain.
package referencecatalogadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/memberref"

	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"

	referencecatalogpostgres "github.com/echovisionlab/geul-api/internal/referencecatalog/postgres"
)

// MemberSummaries adapts Member-owned projections to Reference Catalog reads.
type MemberSummaries struct {
	cdnDomain string
}

var _ referencecatalogpostgres.Members = MemberSummaries{}

func NewMemberSummaries(cdnDomain string) MemberSummaries {
	return MemberSummaries{cdnDomain: cdnDomain}
}

func (a MemberSummaries) Resolve(
	ctx context.Context,
	db *gorm.DB,
	memberIDs []string,
) (map[string]*memberref.Summary, error) {
	var rows []model.Member
	if err := db.WithContext(ctx).Where("id IN ? AND onboarded = TRUE", memberIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	members := make(map[string]*memberref.Summary, len(rows))
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	summaries, err := memberpostgres.LoadSummaries(ctx, db, a.cdnDomain, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		members[row.ID] = summaries[row.ID]
	}
	return members, nil
}
