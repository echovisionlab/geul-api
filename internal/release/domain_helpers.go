package release

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/mutation"
)

type CollaborationPermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
}

func requireDocumentContributors(ctx context.Context, tx *gorm.DB, memberIDs []string) error {
	if len(memberIDs) == 0 {
		return domainerr.InvalidField("contributor_member_ids", "collaboration mutation requires contributors")
	}
	unique := make([]string, 0, len(memberIDs))
	for _, memberID := range memberIDs {
		memberID = strings.TrimSpace(memberID)
		if memberID == "" || (len(unique) > 0 && unique[len(unique)-1] >= memberID) {
			return domainerr.InvalidField("contributor_member_ids", "must contain sorted unique canonical Member UUIDs")
		}
		unique = append(unique, memberID)
	}
	// These rows preserve durable attribution only. Authorization is the exact
	// Release edit check performed when editor-collab admits each frame; Member
	// existence here must never become a fallback grant.
	// PostgreSQL rejects row locks on aggregates, so lock the rows and count them here.
	var locked []string
	if err := tx.WithContext(ctx).Table("member").Clauses(clause.Locking{Strength: "KEY SHARE"}).
		Where("id IN ?", unique).Pluck("id::text", &locked).Error; err != nil {
		return err
	}
	if len(locked) != len(unique) {
		return domainerr.InvalidField("contributor_member_ids", "contains a Member that does not exist")
	}
	return nil
}

// collaborationContributorsAccess is the Access of a collaboration save: the
// save's authority is its contributors, not the request principal, and
// each must still be a current contributor of the Release room.
func (s *Service) collaborationContributorsAccess(releaseID string, contributors []string) mutation.Access {
	return mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		if err := requireDocumentContributors(ctx, tx, contributors); err != nil {
			return err
		}
		return s.checkpoints.RequireCurrentContributors(
			ctx, tx, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_RELEASE, releaseID, contributors,
		)
	})
}

func RequireCollaborationEdit(
	ctx context.Context,
	checker CollaborationPermissionChecker,
	resourceKind intrav1.CollaborationResourceType,
	resourceID string,
	principal *auth.UserInfo,
) error {
	if checker == nil {
		return domainerr.DependencyUnavailable("SpiceDB")
	}
	if resourceKind != intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_RELEASE {
		return domainerr.InvalidField("resource.type", "must be Release")
	}
	if principal == nil || !principal.Authenticated {
		return authenticationRequired()
	}
	can, err := policyv1.Release.Edit(resourceID)
	if err != nil {
		return domainerr.InvalidField("resource.id", "must be a canonical resource UUID")
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
	if err != nil {
		return authenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return noPermission(can.Action().Name(), "release")
	}
	return nil
}
