package artist

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type artistPermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
}

type artistAction = auth.ResourceAction

func checkArtistPermissionForPrincipal(ctx context.Context, checker artistPermissionChecker, artistID string, action artistAction, principal *auth.UserInfo) (bool, error) {
	if principal == nil || !principal.Authenticated || strings.TrimSpace(principal.IdentityID.String()) == "" {
		return false, nil
	}
	can, err := action(artistID)
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
	if err != nil {
		return false, err
	}
	return checker.Can(ctx, decision)
}

func requireArtistGlobalAction(ctx context.Context, checker artistPermissionChecker, can policyv1.Can) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return errs.NotAuthenticated()
	}
	if checker == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.AdminRequired()
	}
	return nil
}

func requireArtistCreate(ctx context.Context, checker artistPermissionChecker) error {
	can, err := policyv1.Artist.Create()
	if err != nil {
		return errs.Internal(err)
	}
	return requireArtistGlobalAction(ctx, checker, can)
}

func requireArtistList(ctx context.Context, checker artistPermissionChecker) error {
	can, err := policyv1.Artist.List()
	if err != nil {
		return errs.Internal(err)
	}
	return requireArtistGlobalAction(ctx, checker, can)
}

func requireArtistPermission(ctx context.Context, checker artistPermissionChecker, artistID string, action artistAction) error {
	if auth.GetUser(ctx) == nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := checkArtistPermissionForPrincipal(ctx, checker, artistID, action, auth.GetUser(ctx))
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NotFound("artist", artistID)
	}
	return nil
}

func lockArtistParticipantRoot(ctx context.Context, tx *gorm.DB, artistID string) error {
	var row struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).Table("artist").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ?::uuid", artistID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("artist", artistID)
		}
		return errs.Internal(err)
	}
	return nil
}

func lockActiveArtistPrincipal(ctx context.Context, tx *gorm.DB) (*auth.UserInfo, error) {
	principal := auth.GetUser(ctx)
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("lock Artist mutation principal: %w", err))
	}
	if !active {
		return nil, nil
	}
	return principal, nil
}

func requireLockedArtistPermission(ctx context.Context, tx *gorm.DB, checker artistPermissionChecker, artistID string, action artistAction) error {
	principal, err := lockActiveArtistPrincipal(ctx, tx)
	if err != nil {
		return err
	}
	allowed, err := checkArtistPermissionForPrincipal(ctx, checker, artistID, action, principal)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NotFound("artist", artistID)
	}
	return nil
}

func artistAllowedActions(ctx context.Context, checker artistPermissionChecker, artistID, status string) ([]managev1.ArtistAction, error) {
	principal := auth.GetUser(ctx)
	type permissionAction struct {
		can    artistAction
		action managev1.ArtistAction
	}
	publishAction := managev1.ArtistAction_ARTIST_ACTION_PUBLISH
	if status == managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String() {
		publishAction = managev1.ArtistAction_ARTIST_ACTION_UNPUBLISH
	}
	checks := []permissionAction{
		{policyv1.Artist.Edit, managev1.ArtistAction_ARTIST_ACTION_EDIT},
		{policyv1.Artist.ManageShareLinks, managev1.ArtistAction_ARTIST_ACTION_MANAGE_SHARE_LINKS},
		{policyv1.Artist.Delete, managev1.ArtistAction_ARTIST_ACTION_DELETE},
		{policyv1.Artist.ManageParticipants, managev1.ArtistAction_ARTIST_ACTION_MANAGE_PARTICIPANTS},
		{policyv1.Artist.Publish, publishAction},
		{policyv1.Artist.RemoveOwner, managev1.ArtistAction_ARTIST_ACTION_REMOVE_OWNER},
	}
	actions := make([]managev1.ArtistAction, 0, len(checks))
	for _, check := range checks {
		allowed, err := checkArtistPermissionForPrincipal(ctx, checker, artistID, check.can, principal)
		if err != nil {
			return nil, errs.DependencyUnavailable("SpiceDB")
		}
		if allowed {
			actions = append(actions, check.action)
		}
	}
	return actions, nil
}

func hasArtistAction(actions []managev1.ArtistAction, expected managev1.ArtistAction) bool {
	for _, action := range actions {
		if action == expected {
			return true
		}
	}
	return false
}

func isArtistLastOwnerConstraint(err error) bool {
	return err != nil && strings.Contains(err.Error(), "must retain at least one durable Owner")
}

func mapArtistParticipantMutationError(err error) error {
	if err == nil || connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	if isArtistLastOwnerConstraint(err) {
		return errs.FailedPrecondition("an Artist must retain at least one durable Owner")
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errs.NotFoundMsg("Artist participant not found")
	}
	return errs.Internal(err)
}
