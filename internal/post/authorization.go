package post

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// PostAuthority contains only exact action permissions already checked for one
// Post. It never reconstructs authority from platform roles or participant
// rows.
type PostAuthority struct {
	engineKeys map[string]struct{}
}

func postAuthority(cans ...policyv1.Can) PostAuthority {
	authority := PostAuthority{engineKeys: make(map[string]struct{}, len(cans))}
	for _, can := range cans {
		authority.add(can)
	}
	return authority
}

func (a *PostAuthority) add(can policyv1.Can) {
	if a == nil || !can.Valid() {
		return
	}
	if a.engineKeys == nil {
		a.engineKeys = make(map[string]struct{})
	}
	a.engineKeys[can.EngineKey()] = struct{}{}
}

func (a PostAuthority) allows(can policyv1.Can) bool {
	if !can.Valid() {
		return false
	}
	_, ok := a.engineKeys[can.EngineKey()]
	return ok
}

type postAction = auth.ResourceAction

func (a PostAuthority) allowsAction(postID string, action postAction) bool {
	can, err := action(postID)
	return err == nil && a.allows(can)
}

const archivedPostAdminEditMessage = "archived posts may only be edited by a site Admin"

func requirePostList(ctx context.Context, checker authz.AuthorizationDecisionChecker) error {
	if checker == nil {
		return domainerr.DependencyUnavailable("SpiceDB")
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return domainerr.New(domainerr.Unauthenticated, "", "not authenticated")
	}
	can, err := policyv1.Post.List()
	if err != nil {
		return err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return domainerr.AdminRequired()
	}
	return nil
}

func checkPostPermissionForPrincipal(
	ctx context.Context,
	checker authz.AuthorizationDecisionChecker,
	postID string,
	principal *auth.UserInfo,
	action postAction,
) (bool, error) {
	if principal == nil || !principal.Authenticated || strings.TrimSpace(principal.IdentityID.String()) == "" {
		return false, nil
	}
	can, err := action(postID)
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
	if err != nil {
		return false, err
	}
	return checker.Can(ctx, decision)
}

func requirePostContributorsExist(ctx context.Context, store Store, memberIDs []string) error {
	if len(memberIDs) == 0 {
		return domainerr.InvalidField("contributor_member_ids", "must contain at least one Member UUID")
	}
	unique := make([]string, 0, len(memberIDs))
	for _, memberID := range memberIDs {
		memberID = strings.TrimSpace(memberID)
		if memberID == "" {
			return domainerr.InvalidField("contributor_member_ids", "must contain canonical Member UUIDs")
		}
		if _, err := uuidutil.ParseCanonical(memberID, "contributor_member_ids"); err != nil {
			return domainerr.InvalidField("contributor_member_ids", "must contain canonical Member UUIDs").WithCause(err)
		}
		if len(unique) > 0 && unique[len(unique)-1] >= memberID {
			return domainerr.InvalidField(
				"contributor_member_ids",
				"must be sorted and contain unique canonical Member UUIDs",
			)
		}
		unique = append(unique, memberID)
	}

	// Per-frame collaboration authorization is the linearization point. The
	// persistence boundary only preserves durable Member attribution and must
	// not reinterpret a later role, Identity, tombstone, or archive transition.
	locked, err := store.LockContributorMembers(ctx, unique)
	if err != nil {
		return fmt.Errorf("lock Post save contributor Members: %w", err)
	}
	if locked != len(unique) {
		return domainerr.InvalidField("contributor_member_ids", "contains a Member that does not exist")
	}
	return nil
}

// requirePostContributorsPermission is the final collaboration save fence.
// Internal Post calls are authenticated as editor-collab, not as the browser
// actor, so contributor_member_ids are trusted only after editor-collab derives
// them from admitted frames. Once the Post root is locked, every independently
// attributed contributor must still resolve to an active linked Identity with
// the one permission selected from that locked lifecycle state.
func requirePostContributorsPermission(
	ctx context.Context,
	store Store,
	spiceDB Authorizer,
	postID string,
	status Status,
	memberIDs []string,
) error {
	if err := requirePostContributorsExist(ctx, store, memberIDs); err != nil {
		return err
	}
	if spiceDB == nil {
		return domainerr.DependencyUnavailable("SpiceDB")
	}
	identities, err := store.ActiveContributorIdentities(ctx, memberIDs)
	if err != nil {
		return fmt.Errorf("load Post collaboration contributors: %w", err)
	}
	if len(identities) != len(memberIDs) {
		return postContributorPermissionDenied(status)
	}
	action := postActionForStatus(status, policyv1.Post.Edit, policyv1.Post.EditArchived)
	can, err := action(postID)
	if err != nil {
		return postContributorPermissionDenied(status).WithCause(err)
	}
	for _, memberID := range memberIDs {
		actor, err := policyv1.NewAccountIdentityActor(identities[memberID])
		if err != nil {
			return postContributorPermissionDenied(status).WithCause(err)
		}
		allowed, err := spiceDB.CheckActorCan(ctx, actor, can)
		if err != nil {
			return failurecause.WithAttrs(domainerr.DependencyUnavailable("SpiceDB").WithCause(err), slog.String("post_id", postID))
		}
		if !allowed {
			return postContributorPermissionDenied(status)
		}
	}
	return nil
}

func postContributorPermissionDenied(status Status) *domainerr.Error {
	if status == StatusArchived {
		return domainerr.Precondition(archivedPostAdminEditMessage)
	}
	return domainerr.NoPermission("edit", "post contributor")
}

func postActionForStatus(
	status Status,
	ordinary postAction,
	archived postAction,
) postAction {
	if status == StatusArchived {
		return archived
	}
	return ordinary
}

func requirePostPermissionForPrincipal(
	ctx context.Context,
	checker authz.AuthorizationDecisionChecker,
	postID string,
	principal *auth.UserInfo,
	action postAction,
	maskNotFound bool,
) (PostAuthority, error) {
	if principal == nil {
		if maskNotFound {
			return PostAuthority{}, domainerr.ResourceNotFound("post", postID)
		}
		return PostAuthority{}, domainerr.AuthenticationRequired()
	}
	can, canErr := action(postID)
	if canErr != nil {
		return PostAuthority{}, domainerr.InvalidField("post_id", "must be a canonical UUID").WithCause(canErr)
	}
	allowed, err := checkPostPermissionForPrincipal(ctx, checker, postID, principal, action)
	if err != nil {
		return PostAuthority{}, failurecause.WithAttrs(domainerr.DependencyUnavailable("SpiceDB").WithCause(err), slog.String("post_id", postID))
	}
	if !allowed {
		if maskNotFound {
			return PostAuthority{}, domainerr.ResourceNotFound("post", postID)
		}
		return PostAuthority{}, domainerr.NoPermission(can.Action().Name(), "post")
	}
	return postAuthority(can), nil
}

func requirePostViewForStatus(
	ctx context.Context,
	checker authz.AuthorizationDecisionChecker,
	postID string,
	status Status,
) (PostAuthority, error) {
	return requirePostViewPermissionForPrincipal(ctx, checker, postID, auth.GetUser(ctx), status)
}

func requirePostViewPermissionForPrincipal(
	ctx context.Context,
	checker authz.AuthorizationDecisionChecker,
	postID string,
	principal *auth.UserInfo,
	status Status,
) (PostAuthority, error) {
	action := postActionForStatus(status, policyv1.Post.View, policyv1.Post.ViewArchived)
	return requirePostPermissionForPrincipal(ctx, checker, postID, principal, action, true)
}

func requirePostActionForStatus(
	ctx context.Context,
	checker authz.AuthorizationDecisionChecker,
	postID string,
	status Status,
	ordinary postAction,
) (PostAuthority, error) {
	return requirePostActionPermissionForPrincipal(ctx, checker, postID, auth.GetUser(ctx), status, ordinary, false)
}

func requirePostActionPermissionForPrincipal(
	ctx context.Context,
	checker authz.AuthorizationDecisionChecker,
	postID string,
	principal *auth.UserInfo,
	status Status,
	ordinary postAction,
	maskNotFound bool,
) (PostAuthority, error) {
	action := postActionForStatus(status, ordinary, policyv1.Post.EditArchived)
	return requirePostPermissionForPrincipal(ctx, checker, postID, principal, action, maskNotFound)
}

// postCreatorAccess requires the fresh create permission; a caller without
// it is not an author.
func postCreatorAccess() mutation.Access {
	return mutation.Locked(func(_ context.Context, _ Store, require mutation.Require) error {
		can, err := policyv1.Post.Create()
		if err != nil {
			return err
		}
		if err := require(can); err != nil {
			if mutation.IsDenied(err) {
				return domainerr.AuthorRequired().WithCause(err)
			}
			return err
		}
		return nil
	})
}

func requireLockedPostPermission(
	require mutation.Require,
	postID string,
	action postAction,
) (PostAuthority, error) {
	can, err := action(postID)
	if err != nil {
		return PostAuthority{}, domainerr.InvalidField("post_id", "must be a canonical UUID").WithCause(err)
	}
	if err := require(can); err != nil {
		if mutation.IsDenied(err) {
			return PostAuthority{}, domainerr.NoPermission(can.Action().Name(), "post").WithCause(err)
		}
		return PostAuthority{}, err
	}
	return postAuthority(can), nil
}

func requireLockedPostActionForStatus(
	require mutation.Require,
	postID string,
	status Status,
	ordinary postAction,
) (PostAuthority, error) {
	action := postActionForStatus(status, ordinary, policyv1.Post.EditArchived)
	return requireLockedPostPermission(require, postID, action)
}

// lockedPost is the access of a Post mutation: the root is locked before
// the one action its lifecycle selects from ordinary is checked. status,
// when set, receives the locked lifecycle.
func lockedPost(postID string, ordinary postAction, status *Status) mutation.Access {
	return mutation.Locked(func(ctx context.Context, store Store, require mutation.Require) error {
		locked, err := lockPostAuthorizationRoot(ctx, store, postID)
		if err != nil {
			return err
		}
		if status != nil {
			*status = locked
		}
		_, err = requireLockedPostActionForStatus(require, postID, locked, ordinary)
		return err
	})
}

func lockPostAuthorizationRoot(ctx context.Context, store Store, postID string) (Status, error) {
	status, err := store.LockPostStatus(ctx, postID)
	if errors.Is(err, ErrPostNotFound) {
		return "", domainerr.ResourceNotFound("post", postID)
	}
	return status, err
}

func lockPostParticipantRoot(ctx context.Context, store Store, postID string) (Status, error) {
	status, err := store.LockParticipantRoot(ctx, postID)
	if errors.Is(err, ErrPostNotFound) {
		return "", domainerr.ResourceNotFound("post", postID)
	}
	return status, err
}

// RequireLockedView locks the Post lifecycle before one exact
// view/view_archived decision require makes with the principal it locks.
// store is bound to the caller's transaction, which must stay open for the
// entire Post-owned read. Missing and unauthorized Posts share the same
// not-found result.
func RequireLockedView(ctx context.Context, joined Joined, require mutation.Require, postID string) error {
	store := joined.Store

	status, err := lockPostAuthorizationRoot(ctx, store, postID)
	if err != nil {
		return err
	}
	action := postActionForStatus(status, policyv1.Post.View, policyv1.Post.ViewArchived)
	if _, err := requireLockedPostPermission(require, postID, action); err != nil {
		if mutation.IsUnauthenticated(err) || mutation.IsDenied(err) {
			return domainerr.ResourceNotFound("post", postID).WithCause(err)
		}
		return err
	}
	return nil
}

// RequireLockedSourceLocaleEdit locks the Post lifecycle, then requires the
// edit action it selects, for a change another domain makes to the Post in
// its transaction, which store is bound to.
func RequireLockedSourceLocaleEdit(ctx context.Context, joined Joined, require mutation.Require, postID string) error {
	store := joined.Store

	status, err := lockPostAuthorizationRoot(ctx, store, postID)
	if err != nil {
		return err
	}
	_, err = requireLockedPostActionForStatus(require, postID, status, policyv1.Post.Edit)
	return err
}

func postAllowedActions(postID string, status Status, authority PostAuthority) []Action {
	actions := make([]Action, 0, 15)
	archived := status == StatusArchived
	viewAction := policyv1.Post.View
	if archived {
		viewAction = policyv1.Post.ViewArchived
	}
	if !authority.allowsAction(postID, viewAction) {
		return actions
	}
	actions = append(actions, ActionViewVersions)
	if archived {
		if authority.allowsAction(postID, policyv1.Post.EditArchived) {
			actions = append(actions,
				ActionEdit,
				ActionAddAuthor,
				ActionRemoveAuthor,
				ActionManageCollaborators,
				ActionManageShareLinks,
				ActionModerateComments,
				ActionRestoreVersion,
				ActionRepublish,
				ActionUnpublish,
			)
		}
		return actions
	}
	if authority.allowsAction(postID, policyv1.Post.Edit) {
		actions = append(actions, ActionEdit)
	}
	if authority.allowsAction(postID, policyv1.Post.Delete) {
		actions = append(actions, ActionDelete)
	}
	if authority.allowsAction(postID, policyv1.Post.ManageParticipants) {
		actions = append(actions,
			ActionAddAuthor,
			ActionManageCollaborators,
		)
	}
	if authority.allowsAction(postID, policyv1.Post.RemoveAuthor) {
		actions = append(actions, ActionRemoveAuthor)
	}
	if authority.allowsAction(postID, policyv1.Post.ManageShareLinks) {
		actions = append(actions, ActionManageShareLinks)
	}
	if authority.allowsAction(postID, policyv1.Post.Manage) {
		actions = append(actions,
			ActionModerateComments,
			ActionRestoreVersion,
		)
	}
	if authority.allowsAction(postID, policyv1.Post.Publish) {
		switch status {
		case StatusDraft:
			actions = append(actions, ActionPublishNow, ActionSchedule)
		case StatusScheduled:
			actions = append(actions, ActionPublishNow, ActionSchedule, ActionCancelSchedule)
		case StatusPublished:
			actions = append(actions, ActionUnpublish, ActionArchive)
		}
	}
	return actions
}

func isPostLastAuthorConstraint(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "must retain at least one durable author")
}

func mapPostParticipantMutationError(err error) error {
	if err == nil {
		return nil
	}
	if mutation.IsClassified(err) {
		return err
	}
	if isPostLastAuthorConstraint(err) {
		return domainerr.Precondition("a post must retain at least one durable author").WithCause(err)
	}
	if errors.Is(err, ErrPostNotFound) {
		return domainerr.New(domainerr.NotFound, "", "post participant not found")
	}
	return err
}
