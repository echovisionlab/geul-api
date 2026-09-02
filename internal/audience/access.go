package audience

import (
	"context"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

var authenticatedAccessSegmentTypes = []string{
	managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS.String(),
	managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER.String(),
}

// ValidateAuthenticatedAccessSegmentIDs validates active segment references
// while holding the same row locks used by relation-policy mutation.
func ValidateAuthenticatedAccessSegmentIDs(ctx context.Context, db *gorm.DB, segmentIDs []string) error {
	if len(segmentIDs) == 0 {
		return nil
	}
	var segments []model.AudienceSegment
	if err := db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", segmentIDs).
		Order("id ASC").
		Find(&segments).Error; err != nil {
		return errs.Internal(err)
	}
	if len(segments) != len(segmentIDs) {
		return errs.InvalidArgument(
			"audience_segment_ids",
			"one or more audience segments do not exist or cannot grant authenticated access",
		)
	}
	for i := range segments {
		if segments[i].ArchivedAt != nil || !authenticatedAccessSegmentType(segments[i].SegmentType) {
			return errs.InvalidArgument(
				"audience_segment_ids",
				"one or more audience segments do not exist or cannot grant authenticated access",
			)
		}
	}
	return nil
}

// AuthenticatedAccessSegmentSummary returns the non-config summary exposed by
// restricted download policy APIs.
func AuthenticatedAccessSegmentSummary(segment *model.AudienceSegment) (*managev1.AudienceSegmentSummary, bool) {
	if segment == nil || segment.ArchivedAt != nil || !authenticatedAccessSegmentType(segment.SegmentType) {
		return nil, false
	}
	segmentType := managev1.SegmentType(managev1.SegmentType_value[strings.TrimSpace(segment.SegmentType)])
	return &managev1.AudienceSegmentSummary{
		Id:          segment.ID,
		Name:        segment.Name,
		Description: segment.Description,
		SegmentType: segmentType,
	}, true
}

func authenticatedAccessSegmentType(segmentType string) bool {
	switch strings.TrimSpace(segmentType) {
	case managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS.String(),
		managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER.String():
		return true
	default:
		return false
	}
}

func requireDownloadPolicyAuthor(ctx context.Context, spiceDB *auth.SpiceDBClient) (*auth.UserInfo, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || strings.TrimSpace(user.MemberID.String()) == "" {
		return nil, errs.AuthenticationRequired()
	}
	if user.Banned {
		return nil, errs.AccountBanned()
	}
	can, err := policyv1.AudienceSegment.ListAuthenticatedAccess()
	if err != nil {
		return nil, errs.Internal(err)
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return nil, errs.AuthenticationRequired()
	}
	allowed, err := spiceDB.Can(ctx, decision)
	if err != nil {
		return nil, errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return nil, errs.AuthorRequired()
	}
	return user, nil
}
