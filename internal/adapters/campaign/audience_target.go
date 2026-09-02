// Package campaign contains composition adapters for Campaign dependencies.
package campaign

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/audience"
	campaigndomain "github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/model"
)

// AudienceTargets adapts Audience's normalized relation-backed segment read to
// the narrow immutable target facts required by Campaign delivery.
type AudienceTargets struct{}

var _ campaigndomain.CampaignAudienceTargetPort = AudienceTargets{}

func NewAudienceTargets() AudienceTargets { return AudienceTargets{} }

func (AudienceTargets) LockTarget(
	ctx context.Context,
	tx *gorm.DB,
	segmentID string,
) (campaigndomain.CampaignAudienceTarget, error) {
	var segment model.AudienceSegment
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&segment, "id = ?", segmentID).Error; err != nil {
		return campaigndomain.CampaignAudienceTarget{}, err
	}
	target := campaigndomain.CampaignAudienceTarget{
		Archived:    segment.ArchivedAt != nil,
		SegmentType: segment.SegmentType,
	}
	if target.Archived {
		return target, nil
	}
	if err := audience.LoadSegmentConfig(ctx, tx, &segment); err != nil {
		return campaigndomain.CampaignAudienceTarget{}, err
	}
	target.Valid = audience.ValidateSegmentConfigForType(segment.SegmentType, segment.Config) == nil
	target.CreatedAfter = segment.CreatedAfter
	target.CreatedBefore = segment.CreatedBefore
	target.MemberTagIDs = append([]string(nil), segment.Config.MemberTagIDs...)
	target.AccountRoles = append([]string(nil), segment.Config.AccountRoles...)
	target.ExcludedMemberIDs = append([]string(nil), segment.Config.ExcludeMemberIDs...)
	return target, nil
}
