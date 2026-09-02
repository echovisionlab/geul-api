// Package referencecatalogadapter contains composition-only adapters for the
// Reference Catalog domain.
package referencecatalogadapter

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// MemberSummaries adapts Member-owned projections to Reference Catalog reads.
type MemberSummaries struct {
	cdnDomain string
}

var _ referencecatalog.MemberSummaries = MemberSummaries{}

func NewMemberSummaries(cdnDomain string) MemberSummaries {
	return MemberSummaries{cdnDomain: cdnDomain}
}

func (a MemberSummaries) Resolve(
	ctx context.Context,
	db *gorm.DB,
	memberIDs []string,
) (map[string]*commonv1.MemberSummary, error) {
	var rows []model.Member
	if err := db.WithContext(ctx).Where("id IN ? AND onboarded = TRUE", memberIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	members := make(map[string]*commonv1.MemberSummary, len(rows))
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	summaries, err := member.LoadSummaries(ctx, db, a.cdnDomain, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		members[row.ID] = summaries[row.ID]
	}
	return members, nil
}
