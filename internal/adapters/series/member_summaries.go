package series

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/member"
	seriesdomain "github.com/echovisionlab/geul-api/internal/series"
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
) (map[string]seriesdomain.ManagerSummary, error) {
	summaries, err := member.LoadSummaries(ctx, a.db, a.cdnDomain, memberIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string]seriesdomain.ManagerSummary, len(summaries))
	for memberID, summary := range summaries {
		if summary == nil {
			continue
		}
		result[memberID] = seriesdomain.ManagerSummary{
			MemberID: memberID,
			Nickname: summary.GetNickname(),
			Avatar:   AssetFromRef(summary.AvatarAsset),
		}
	}
	return result, nil
}
