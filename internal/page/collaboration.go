package page

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

// requireCollaborationEditForPrincipal requires principal to edit the Page
// whose room it opens.
func requireCollaborationEditForPrincipal(
	ctx context.Context,
	checker CollaborationPermissionChecker,
	pageID string,
	principal *auth.UserInfo,
) error {
	can, err := policyv1.Page.Edit(pageID)
	if err != nil {
		return domainerr.InvalidField("resource.id", "must be a canonical resource UUID").WithCause(err)
	}
	if principal == nil || !principal.Authenticated {
		return domainerr.AuthenticationRequired()
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
	if err != nil {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return domainerr.NoPermission(can.Action().Name(), "page")
	}
	return nil
}

func (s *Service) requireDocumentContributors(ctx context.Context, tx *gorm.DB, memberIDs []string) error {
	if len(memberIDs) == 0 {
		return domainerr.InvalidField("contributor_member_ids", "collaboration mutation requires contributors")
	}
	unique := make([]string, 0, len(memberIDs))
	for _, memberID := range memberIDs {
		memberID = strings.TrimSpace(memberID)
		if _, err := uuidutil.ParseCanonical(memberID, "contributor_member_ids"); err != nil {
			return domainerr.InvalidField("contributor_member_ids", "must contain canonical Member UUIDs").WithCause(err)
		}
		if len(unique) > 0 && unique[len(unique)-1] >= memberID {
			return domainerr.InvalidField("contributor_member_ids", "must be sorted and contain unique canonical Member UUIDs")
		}
		unique = append(unique, memberID)
	}
	members, err := s.members.LockMembers(ctx, tx, unique)
	if err != nil {
		return fmt.Errorf("lock Page save contributor Members: %w", err)
	}
	if len(members) != len(unique) {
		return domainerr.InvalidField("contributor_member_ids", "contains a Member that does not exist")
	}
	return nil
}

// requirePageContributorsPermission is the final internal collaboration save
// fence. editor-collab authenticates the service call while the contributor
// list carries the browser actors admitted to the resident room. Every actor
// must still be an active Member with the exact Page permission after the Page
// root has been locked.
func (s *Service) requirePageContributorsPermission(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
	action pageAction,
	memberIDs []string,
) error {
	if err := s.requireDocumentContributors(ctx, tx, memberIDs); err != nil {
		return err
	}
	identities, err := s.members.ActiveIdentities(ctx, tx, memberIDs)
	if err != nil {
		return fmt.Errorf("load Page collaboration contributors: %w", err)
	}
	if len(identities) != len(memberIDs) {
		return domainerr.NoPermission("edit", "page contributor")
	}
	can, err := action(pageID)
	if err != nil {
		return domainerr.ResourceNotFound("page", pageID).WithCause(err)
	}
	for _, memberID := range memberIDs {
		actor, err := policyv1.NewAccountIdentityActor(identities[memberID])
		if err != nil {
			return domainerr.NoPermission("edit", "page contributor").WithCause(err)
		}
		allowed, err := s.permissions.CheckActorCan(ctx, actor, can)
		if err != nil {
			return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
		}
		if !allowed {
			return domainerr.NoPermission("edit", "page contributor")
		}
	}
	return nil
}
