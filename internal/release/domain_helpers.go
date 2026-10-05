package release

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errTranslationSourceNoLongerCurrent = errors.New("translation source is no longer current")

type CollaborationPermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
}

func requireDocumentContributors(ctx context.Context, tx *gorm.DB, memberIDs []string) error {
	if len(memberIDs) == 0 {
		return errs.InvalidArgument("contributor_member_ids", "collaboration mutation requires contributors")
	}
	unique := make([]string, 0, len(memberIDs))
	for _, memberID := range memberIDs {
		memberID = strings.TrimSpace(memberID)
		if memberID == "" || (len(unique) > 0 && unique[len(unique)-1] >= memberID) {
			return errs.InvalidArgument("contributor_member_ids", "must contain sorted unique canonical Member UUIDs")
		}
		unique = append(unique, memberID)
	}
	// These rows preserve durable attribution only. Authorization is the exact
	// Release edit check performed when editor-collab admits each frame; Member
	// existence here must never become a fallback grant.
	var count int64
	if err := tx.WithContext(ctx).Table("member").Clauses(clause.Locking{Strength: "KEY SHARE"}).
		Where("id IN ?", unique).Count(&count).Error; err != nil {
		return errs.Internal(err)
	}
	if count != int64(len(unique)) {
		return errs.InvalidArgument("contributor_member_ids", "contains a Member that does not exist")
	}
	return nil
}

func requireReleaseCollaborationContributors(
	ctx context.Context,
	tx *gorm.DB,
	checkpoints persistencecheckpoint.ContributorFence,
	releaseID string,
	contributors []string,
) error {
	return checkpoints.RequireCurrentContributors(
		ctx,
		tx,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_RELEASE,
		releaseID,
		contributors,
	)
}

func RequireCollaborationEdit(
	ctx context.Context,
	checker CollaborationPermissionChecker,
	resourceKind intrav1.CollaborationResourceType,
	resourceID string,
	principal *auth.UserInfo,
) error {
	if checker == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if resourceKind != intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_RELEASE {
		return errs.InvalidArgument("resource.type", "must be Release")
	}
	if principal == nil || !principal.Authenticated {
		return errs.AuthenticationRequired()
	}
	can, err := policyv1.Release.Edit(resourceID)
	if err != nil {
		return errs.InvalidArgument("resource.id", "must be a canonical resource UUID")
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NoPermission(can.Action().Name(), "release")
	}
	return nil
}
