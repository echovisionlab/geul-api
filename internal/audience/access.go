package audience

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

var authenticatedAccessSegmentTypes = []model.AudienceSegmentType{
	model.AudienceSegmentMemberTags,
	model.AudienceSegmentMembersByFilter,
}

// ValidateAuthenticatedAccessSegmentIDs validates active segment references
// while holding the same row locks used by relation-policy mutation.
func ValidateAuthenticatedAccessSegmentIDs(ctx context.Context, db *gorm.DB, segmentIDs []string) error {
	if len(segmentIDs) == 0 {
		return nil
	}
	var segments []model.AudienceSegment
	if err := db.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id IN ?", segmentIDs).
		Order("id ASC").
		Find(&segments).Error; err != nil {
		return fmt.Errorf("lock authenticated access segments: %w", err)
	}
	if len(segments) != len(segmentIDs) {
		return domainerr.InvalidField(
			"audience_segment_ids",
			"one or more audience segments do not exist or cannot grant authenticated access",
		)
	}
	for i := range segments {
		if segments[i].ArchivedAt != nil || !authenticatedAccessSegmentType(segments[i].SegmentType) {
			return domainerr.InvalidField(
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
	return &managev1.AudienceSegmentSummary{
		Id:          segment.ID,
		Name:        segment.Name,
		Description: segment.Description,
		SegmentType: segmentTypeToProto(trimmedSegmentType(segment.SegmentType)),
	}, true
}

func trimmedSegmentType(segmentType model.AudienceSegmentType) model.AudienceSegmentType {
	return model.AudienceSegmentType(strings.TrimSpace(string(segmentType)))
}

func authenticatedAccessSegmentType(segmentType model.AudienceSegmentType) bool {
	switch trimmedSegmentType(segmentType) {
	case model.AudienceSegmentMemberTags, model.AudienceSegmentMembersByFilter:
		return true
	default:
		return false
	}
}

func requireDownloadPolicyAuthor(ctx context.Context, spiceDB *auth.SpiceDBClient) error {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || strings.TrimSpace(user.MemberID.String()) == "" {
		return domainerr.New(domainerr.Unauthenticated, "", "authentication required")
	}
	if user.Banned {
		return domainerr.Denied("account is banned")
	}
	can, err := policyv1.AudienceSegment.ListAuthenticatedAccess()
	if err != nil {
		return err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return domainerr.New(domainerr.Unauthenticated, "", "authentication required").WithCause(err)
	}
	allowed, err := spiceDB.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return domainerr.Denied("author access required")
	}
	return nil
}
