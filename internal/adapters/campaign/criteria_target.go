// Package campaign contains composition adapters for Campaign dependencies.
package campaign

import (
	"context"

	campaignpostgres "github.com/echovisionlab/geul-api/internal/campaign/postgres"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	campaigndomain "github.com/echovisionlab/geul-api/internal/campaign"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	criteriapostgres "github.com/echovisionlab/geul-api/internal/member/criteria/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
)

// CriteriaTargets adapts Criteria's normalized relation-backed criteria read to
// the narrow immutable target facts required by Campaign delivery.
type CriteriaTargets struct{}

var _ campaignpostgres.CampaignCriteriaTargetPort = CriteriaTargets{}

func NewCriteriaTargets() CriteriaTargets { return CriteriaTargets{} }

func (CriteriaTargets) LockTarget(
	ctx context.Context,
	tx *gorm.DB,
	criteriaID string,
) (campaigndomain.CampaignCriteriaTarget, error) {
	var criteria model.MemberCriteria
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&criteria, "id = ?", criteriaID).Error; err != nil {
		return campaigndomain.CampaignCriteriaTarget{}, err
	}
	target := campaigndomain.CampaignCriteriaTarget{
		Archived: criteria.ArchivedAt != nil,
		Kind:     campaigndomain.MemberCriteriaKind(criteria.Kind),
	}
	if target.Archived {
		return target, nil
	}
	if err := criteriapostgres.LoadCriteriaConfig(ctx, tx, &criteria); err != nil {
		return campaigndomain.CampaignCriteriaTarget{}, err
	}
	config := criteriapostgres.CriteriaOf(criteria).Config
	target.Valid = membercriteria.ValidateConfig(membercriteria.Kind(criteria.Kind), config) == nil
	target.CreatedAfter = criteria.CreatedAfter
	target.CreatedBefore = criteria.CreatedBefore
	target.MemberTagIDs = append([]string(nil), criteria.Config.MemberTagIDs...)
	target.AccountRoles = append([]string(nil), criteria.Config.AccountRoles...)
	target.ExcludedMemberIDs = append([]string(nil), criteria.Config.ExcludeMemberIDs...)
	return target, nil
}
