package post

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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

func requirePostList(ctx context.Context, checker CollaborationPermissionChecker) error {
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
	spiceDB CollaborationPermissionChecker,
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
	return spiceDB.Can(ctx, decision)
}

func requirePostContributorsExist(ctx context.Context, db *gorm.DB, memberIDs []string) error {
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
	var lockedMembers []struct {
		ID string `gorm:"column:id"`
	}
	if err := db.WithContext(ctx).
		Table("member").
		Clauses(clause.Locking{Strength: "KEY SHARE"}).
		Select("id::text").
		Where("id IN ?", unique).
		Find(&lockedMembers).Error; err != nil {
		return fmt.Errorf("lock Post save contributor Members: %w", err)
	}
	if len(lockedMembers) != len(unique) {
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
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	postID string,
	status model.PostStatus,
	memberIDs []string,
) error {
	if err := requirePostContributorsExist(ctx, tx, memberIDs); err != nil {
		return err
	}
	if spiceDB == nil {
		return domainerr.DependencyUnavailable("SpiceDB")
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
		return fmt.Errorf("load Post collaboration contributors: %w", err)
	}
	identities := make(map[string]string, len(rows))
	for _, row := range rows {
		identities[row.MemberID] = row.IdentityID
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

func postContributorPermissionDenied(status model.PostStatus) *domainerr.Error {
	if status == model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String()) {
		return domainerr.Precondition(archivedPostAdminEditMessage)
	}
	return domainerr.NoPermission("edit", "post contributor")
}

func postActionForStatus(
	status model.PostStatus,
	ordinary postAction,
	archived postAction,
) postAction {
	if status == model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String()) {
		return archived
	}
	return ordinary
}

func requirePostPermissionForPrincipal(
	ctx context.Context,
	spiceDB CollaborationPermissionChecker,
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
	allowed, err := checkPostPermissionForPrincipal(ctx, spiceDB, postID, principal, action)
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
	spiceDB *auth.SpiceDBClient,
	postID string,
	status model.PostStatus,
) (PostAuthority, error) {
	return requirePostViewPermissionForPrincipal(ctx, spiceDB, postID, auth.GetUser(ctx), status)
}

func requirePostViewPermissionForPrincipal(
	ctx context.Context,
	spiceDB CollaborationPermissionChecker,
	postID string,
	principal *auth.UserInfo,
	status model.PostStatus,
) (PostAuthority, error) {
	action := postActionForStatus(status, policyv1.Post.View, policyv1.Post.ViewArchived)
	return requirePostPermissionForPrincipal(ctx, spiceDB, postID, principal, action, true)
}

func requirePostActionForStatus(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	postID string,
	status model.PostStatus,
	ordinary postAction,
) (PostAuthority, error) {
	return requirePostActionPermissionForPrincipal(ctx, spiceDB, postID, auth.GetUser(ctx), status, ordinary, false)
}

func requirePostActionPermissionForPrincipal(
	ctx context.Context,
	spiceDB CollaborationPermissionChecker,
	postID string,
	principal *auth.UserInfo,
	status model.PostStatus,
	ordinary postAction,
	maskNotFound bool,
) (PostAuthority, error) {
	action := postActionForStatus(status, ordinary, policyv1.Post.EditArchived)
	return requirePostPermissionForPrincipal(ctx, spiceDB, postID, principal, action, maskNotFound)
}

// postCreatorAccess requires the fresh create permission; a caller without
// it is not an author.
func postCreatorAccess() mutation.Access {
	return mutation.Locked(func(_ context.Context, _ *gorm.DB, require mutation.Require) error {
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

func freshRequire(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient) mutation.Require {
	return func(can policyv1.Can) error {
		return identitystate.RequireFreshCan(ctx, tx, spiceDB, can)
	}
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
	status model.PostStatus,
	ordinary postAction,
) (PostAuthority, error) {
	action := postActionForStatus(status, ordinary, policyv1.Post.EditArchived)
	return requireLockedPostPermission(require, postID, action)
}

// lockedPost is the access of a Post mutation: the root is locked before
// the one action its lifecycle selects from ordinary is checked. status,
// when set, receives the locked lifecycle.
func lockedPost(postID string, ordinary postAction, status *model.PostStatus) mutation.Access {
	return mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
		locked, err := lockPostAuthorizationRoot(ctx, tx, postID)
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

func lockPostAuthorizationRoot(ctx context.Context, tx *gorm.DB, postID string) (model.PostStatus, error) {
	var row struct {
		Status model.PostStatus `gorm:"column:status"`
	}
	if err := tx.WithContext(ctx).
		Table("post").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("status").
		Where("id = ?::uuid", postID).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", domainerr.ResourceNotFound("post", postID)
		}
		return "", err
	}
	return row.Status, nil
}

// RequireLockedView locks the Post lifecycle and current principal before one
// exact view/view_archived decision. Callers must keep tx open for the entire
// Post-owned read. Missing and unauthorized Posts share the same not-found
// result.
func RequireLockedView(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	postID string,
) error {
	status, err := lockPostAuthorizationRoot(ctx, tx, postID)
	if err != nil {
		return err
	}
	action := postActionForStatus(status, policyv1.Post.View, policyv1.Post.ViewArchived)
	if _, err := requireLockedPostPermission(freshRequire(ctx, tx, spiceDB), postID, action); err != nil {
		if mutation.IsUnauthenticated(err) || mutation.IsDenied(err) {
			return domainerr.ResourceNotFound("post", postID).WithCause(err)
		}
		return err
	}
	return nil
}

func RequireLockedSourceLocaleEdit(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	postID string,
) error {
	status, err := lockPostAuthorizationRoot(ctx, tx, postID)
	if err != nil {
		return err
	}
	_, err = requireLockedPostActionForStatus(freshRequire(ctx, tx, spiceDB), postID, status, policyv1.Post.Edit)
	return err
}

func postAllowedActions(postID string, status model.PostStatus, authority PostAuthority) []managev1.PostAction {
	actions := make([]managev1.PostAction, 0, 15)
	archived := status == model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String())
	viewAction := policyv1.Post.View
	if archived {
		viewAction = policyv1.Post.ViewArchived
	}
	if !authority.allowsAction(postID, viewAction) {
		return actions
	}
	actions = append(actions, managev1.PostAction_POST_ACTION_VIEW_VERSIONS)
	if archived {
		if authority.allowsAction(postID, policyv1.Post.EditArchived) {
			actions = append(actions,
				managev1.PostAction_POST_ACTION_EDIT,
				managev1.PostAction_POST_ACTION_ADD_AUTHOR,
				managev1.PostAction_POST_ACTION_REMOVE_AUTHOR,
				managev1.PostAction_POST_ACTION_MANAGE_COLLABORATORS,
				managev1.PostAction_POST_ACTION_MANAGE_SHARE_LINKS,
				managev1.PostAction_POST_ACTION_MODERATE_COMMENTS,
				managev1.PostAction_POST_ACTION_RESTORE_VERSION,
				managev1.PostAction_POST_ACTION_REPUBLISH,
				managev1.PostAction_POST_ACTION_UNPUBLISH,
			)
		}
		return actions
	}
	if authority.allowsAction(postID, policyv1.Post.Edit) {
		actions = append(actions, managev1.PostAction_POST_ACTION_EDIT)
	}
	if authority.allowsAction(postID, policyv1.Post.Delete) {
		actions = append(actions, managev1.PostAction_POST_ACTION_DELETE)
	}
	if authority.allowsAction(postID, policyv1.Post.ManageParticipants) {
		actions = append(actions,
			managev1.PostAction_POST_ACTION_ADD_AUTHOR,
			managev1.PostAction_POST_ACTION_MANAGE_COLLABORATORS,
		)
	}
	if authority.allowsAction(postID, policyv1.Post.RemoveAuthor) {
		actions = append(actions, managev1.PostAction_POST_ACTION_REMOVE_AUTHOR)
	}
	if authority.allowsAction(postID, policyv1.Post.ManageShareLinks) {
		actions = append(actions, managev1.PostAction_POST_ACTION_MANAGE_SHARE_LINKS)
	}
	if authority.allowsAction(postID, policyv1.Post.Manage) {
		actions = append(actions,
			managev1.PostAction_POST_ACTION_MODERATE_COMMENTS,
			managev1.PostAction_POST_ACTION_RESTORE_VERSION,
		)
	}
	if authority.allowsAction(postID, policyv1.Post.Publish) {
		switch status {
		case model.PostStatus(managev1.PostStatus_POST_STATUS_DRAFT.String()):
			actions = append(actions, managev1.PostAction_POST_ACTION_PUBLISH_NOW, managev1.PostAction_POST_ACTION_SCHEDULE)
		case model.PostStatus(managev1.PostStatus_POST_STATUS_SCHEDULED.String()):
			actions = append(actions, managev1.PostAction_POST_ACTION_PUBLISH_NOW, managev1.PostAction_POST_ACTION_SCHEDULE, managev1.PostAction_POST_ACTION_CANCEL_SCHEDULE)
		case model.PostStatus(managev1.PostStatus_POST_STATUS_PUBLISHED.String()):
			actions = append(actions, managev1.PostAction_POST_ACTION_UNPUBLISH, managev1.PostAction_POST_ACTION_ARCHIVE)
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

func postRelationExists(ctx context.Context, db *gorm.DB, table, postID, memberID string) (bool, error) {
	if table != "post_author" && table != "post_collaborator" {
		return false, fmt.Errorf("unsupported post participant table %q", table)
	}
	var count int64
	if err := db.WithContext(ctx).Table(table).
		Where("post_id = ? AND member_id = ?", postID, memberID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
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
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainerr.New(domainerr.NotFound, "", "post participant not found")
	}
	return err
}
