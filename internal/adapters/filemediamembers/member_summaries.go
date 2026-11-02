package filemediamembers

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	"gorm.io/gorm"
)

type MemberSummaries struct {
	db        *gorm.DB
	cdnDomain string
}

func NewMemberSummaries(db *gorm.DB, cdnDomain string) *MemberSummaries {
	return &MemberSummaries{db, cdnDomain}
}
func (r *MemberSummaries) Load(ctx context.Context, ids []string) (map[string]domain.MemberSummary, error) {
	summaries, err := memberpostgres.LoadSummaries(ctx, r.db, r.cdnDomain, ids)
	if err != nil {
		return nil, err
	}
	result := make(map[string]domain.MemberSummary, len(summaries))
	for id, value := range summaries {
		projected := domain.MemberSummary{ID: value.ID, Nickname: value.Nickname, Deleted: value.Deleted}
		projected.Avatar = value.Avatar.Reference()
		result[id] = projected
	}
	return result, nil
}
