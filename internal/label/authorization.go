package label

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

type labelPermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
	CheckActorCan(context.Context, policyv1.Actor, policyv1.Can) (bool, error)
}

type labelAction = auth.ResourceAction

func checkLabelPermissionForPrincipal(ctx context.Context, checker labelPermissionChecker, labelID string, action labelAction, principal *auth.UserInfo) (bool, error) {
	if principal == nil || !principal.Authenticated || strings.TrimSpace(principal.IdentityID.String()) == "" {
		return false, nil
	}
	can, err := action(labelID)
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
	if err != nil {
		return false, err
	}
	return checker.Can(ctx, decision)
}

func requireLabelGlobalAction(ctx context.Context, checker labelPermissionChecker, can policyv1.Can) error {
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

func requireLabelCreate(ctx context.Context, checker labelPermissionChecker) error {
	can, err := policyv1.Label.Create()
	if err != nil {
		return errs.Internal(err)
	}
	return requireLabelGlobalAction(ctx, checker, can)
}

func requireLabelList(ctx context.Context, checker labelPermissionChecker) error {
	can, err := policyv1.Label.List()
	if err != nil {
		return errs.Internal(err)
	}
	return requireLabelGlobalAction(ctx, checker, can)
}

func requireLabelPermission(ctx context.Context, checker labelPermissionChecker, labelID string, action labelAction) error {
	if auth.GetUser(ctx) == nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := checkLabelPermissionForPrincipal(ctx, checker, labelID, action, auth.GetUser(ctx))
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NotFound("label", labelID)
	}
	return nil
}

func requireLabelView(ctx context.Context, checker labelPermissionChecker, labelID string) error {
	allowed, err := checkLabelPermissionForPrincipal(ctx, checker, labelID, policyv1.Label.View, auth.GetUser(ctx))
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NotFound("label", labelID)
	}
	return nil
}

func lockLabelParticipantRoot(ctx context.Context, tx *gorm.DB, labelID string) error {
	var row struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).Table("label").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ?::uuid", labelID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("label", labelID)
		}
		return errs.Internal(err)
	}
	return nil
}

func lockActiveLabelPrincipal(ctx context.Context, tx *gorm.DB) (*auth.UserInfo, error) {
	principal := auth.GetUser(ctx)
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("lock Label mutation principal: %w", err))
	}
	if !active {
		return nil, nil
	}
	return principal, nil
}

func requireLockedLabelPermission(ctx context.Context, tx *gorm.DB, checker labelPermissionChecker, labelID string, action labelAction) error {
	principal, err := lockActiveLabelPrincipal(ctx, tx)
	if err != nil {
		return err
	}
	allowed, err := checkLabelPermissionForPrincipal(ctx, checker, labelID, action, principal)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NotFound("label", labelID)
	}
	return nil
}

func labelAllowedActions(ctx context.Context, checker labelPermissionChecker, labelID, status string) ([]managev1.LabelAction, error) {
	principal := auth.GetUser(ctx)
	type permissionAction struct {
		can    labelAction
		action managev1.LabelAction
	}
	publishAction := managev1.LabelAction_LABEL_ACTION_PUBLISH
	if status == managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String() {
		publishAction = managev1.LabelAction_LABEL_ACTION_UNPUBLISH
	}
	checks := []permissionAction{
		{policyv1.Label.Edit, managev1.LabelAction_LABEL_ACTION_EDIT},
		{policyv1.Label.ManageShareLinks, managev1.LabelAction_LABEL_ACTION_MANAGE_SHARE_LINKS},
		{policyv1.Label.Delete, managev1.LabelAction_LABEL_ACTION_DELETE},
		{policyv1.Label.ManageParticipants, managev1.LabelAction_LABEL_ACTION_MANAGE_PARTICIPANTS},
		{policyv1.Label.Publish, publishAction},
		{policyv1.Label.RemoveOwner, managev1.LabelAction_LABEL_ACTION_REMOVE_OWNER},
	}
	actions := make([]managev1.LabelAction, 0, len(checks))
	for _, check := range checks {
		allowed, err := checkLabelPermissionForPrincipal(ctx, checker, labelID, check.can, principal)
		if err != nil {
			return nil, errs.DependencyUnavailable("SpiceDB")
		}
		if allowed {
			actions = append(actions, check.action)
		}
	}
	return actions, nil
}

func isLabelLastOwnerConstraint(err error) bool {
	return err != nil && strings.Contains(err.Error(), "must retain at least one durable Owner")
}

func mapLabelParticipantMutationError(err error) error {
	if err == nil || connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	if isLabelLastOwnerConstraint(err) {
		return errs.FailedPrecondition("a Label must retain at least one durable Owner")
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errs.NotFoundMsg("Label participant not found")
	}
	return errs.Internal(err)
}
