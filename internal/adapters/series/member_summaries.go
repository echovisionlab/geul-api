package series

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/member"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// MemberSummaries adapts Member-owned summaries to Series manager projections.
type MemberSummaries struct {
	db        *gorm.DB
	cdnDomain string
}

func NewMemberSummaries(db *gorm.DB, cdnDomain string) *MemberSummaries {
	if db == nil {
		panic("series member summaries: db is required")
	}
	return &MemberSummaries{db: db, cdnDomain: cdnDomain}
}

func (a *MemberSummaries) LoadSeriesManagers(
	ctx context.Context,
	memberIDs []string,
) (map[string]*managev1.SeriesManager, error) {
	summaries, err := member.LoadSummaries(ctx, a.db, a.cdnDomain, memberIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*managev1.SeriesManager, len(summaries))
	for memberID, summary := range summaries {
		if summary == nil {
			continue
		}
		result[memberID] = &managev1.SeriesManager{
			MemberId:    memberID,
			Nickname:    summary.GetNickname(),
			AvatarAsset: summary.AvatarAsset,
		}
	}
	return result, nil
}
