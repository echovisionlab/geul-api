package page

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func RequireCollaborationEditForPrincipal(
	ctx context.Context,
	checker CollaborationPermissionChecker,
	resourceKind intrav1.CollaborationResourceType,
	resourceID string,
	principal *auth.UserInfo,
) error {
	if checker == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if resourceKind != intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PAGE {
		return errs.InvalidArgument("resource.type", "must be Page")
	}
	can, err := policyv1.Page.Edit(resourceID)
	if err != nil {
		return errs.InvalidArgument("resource.id", "must be a canonical resource UUID")
	}
	if principal == nil || !principal.Authenticated {
		return errs.AuthenticationRequired()
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
		return errs.NoPermission(can.Action().Name(), "page")
	}
	return nil
}

func requireDocumentContributors(ctx context.Context, db *gorm.DB, memberIDs []string) error {
	if len(memberIDs) == 0 {
		return errs.InvalidArgument("contributor_member_ids", "collaboration mutation requires contributors")
	}
	unique := make([]string, 0, len(memberIDs))
	for _, memberID := range memberIDs {
		memberID = strings.TrimSpace(memberID)
		if _, err := uuidutil.ParseCanonical(memberID, "contributor_member_ids"); err != nil {
			return errs.InvalidArgument("contributor_member_ids", "must contain canonical Member UUIDs")
		}
		if len(unique) > 0 && unique[len(unique)-1] >= memberID {
			return errs.InvalidArgument("contributor_member_ids", "must be sorted and contain unique canonical Member UUIDs")
		}
		unique = append(unique, memberID)
	}
	var members []struct {
		ID string `gorm:"column:id"`
	}
	if err := db.WithContext(ctx).
		Table("member").
		Clauses(clause.Locking{Strength: "KEY SHARE"}).
		Select("id::text").
		Where("id IN ?", unique).
		Find(&members).Error; err != nil {
		return errs.Internal(fmt.Errorf("lock Page save contributor Members: %w", err))
	}
	if len(members) != len(unique) {
		return errs.InvalidArgument("contributor_member_ids", "contains a Member that does not exist")
	}
	return nil
}

// requirePageContributorsPermission is the final internal collaboration save
// fence. editor-collab authenticates the service call while the contributor
// list carries the browser actors admitted to the resident room. Every actor
// must still be an active Member with the exact Page permission after the Page
// root has been locked.
func requirePageContributorsPermission(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB CollaborationPermissionChecker,
	pageID string,
	action pageAction,
	memberIDs []string,
) error {
	if err := requireDocumentContributors(ctx, tx, memberIDs); err != nil {
		return err
	}
	if spiceDB == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	type contributorRow struct {
		MemberID   string `gorm:"column:member_id"`
		IdentityID string `gorm:"column:identity_id"`
	}
	var rows []contributorRow
	if err := tx.WithContext(ctx).
		Table("member AS member").
		Joins("JOIN kratos.identities AS identity ON identity.id = member.account_identity_id AND identity.external_id = member.id::text AND identity.state = ?", "active").
		Select("member.id::text AS member_id, member.account_identity_id::text AS identity_id").
		Where("member.id IN ? AND member.deleted_at IS NULL AND member.onboarded = TRUE AND member.account_identity_id IS NOT NULL AND COALESCE((identity.metadata_admin ->> 'banned')::boolean, false) = FALSE", memberIDs).
		Find(&rows).Error; err != nil {
		return errs.Internal(fmt.Errorf("load Page collaboration contributors: %w", err))
	}
	identities := make(map[string]string, len(rows))
	for _, row := range rows {
		identities[row.MemberID] = row.IdentityID
	}
	if len(identities) != len(memberIDs) {
		return errs.NoPermission("edit", "page contributor")
	}
	can, err := action(pageID)
	if err != nil {
		return errs.NotFound("page", pageID)
	}
	for _, memberID := range memberIDs {
		actor, err := policyv1.NewAccountIdentityActor(identities[memberID])
		if err != nil {
			return errs.NoPermission("edit", "page contributor")
		}
		allowed, err := spiceDB.CheckActorCan(ctx, actor, can)
		if err != nil {
			return errs.DependencyUnavailable("SpiceDB")
		}
		if !allowed {
			return errs.NoPermission("edit", "page contributor")
		}
	}
	return nil
}
