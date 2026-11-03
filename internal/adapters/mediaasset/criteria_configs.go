package mediaasset

import (
	"context"

	criteriapostgres "github.com/echovisionlab/geul-api/internal/member/criteria/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

type CriteriaConfigs struct{}

func NewCriteriaConfigs() CriteriaConfigs { return CriteriaConfigs{} }

func (CriteriaConfigs) LoadCriteriaConfigs(ctx context.Context, db *gorm.DB, criteriaSet []*model.MemberCriteria) error {
	return criteriapostgres.LoadCriteriaConfigs(ctx, db, criteriaSet)
}
