package legal

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type legalCanSet struct {
	view             auth.ResourceAction
	edit             auth.ResourceAction
	viewArchived     auth.ResourceAction
	editArchived     auth.ResourceAction
	delete           auth.ResourceAction
	publish          auth.ResourceAction
	manage           auth.ResourceAction
	manageShareLinks auth.ResourceAction
}

type legalAction uint8

const (
	legalActionView legalAction = iota + 1
	legalActionEdit
	legalActionViewArchived
	legalActionEditArchived
	legalActionDelete
	legalActionPublish
	legalActionManage
	legalActionManageShareLinks
)

func legalCanSetForKind(kind intrav1.CollaborationResourceType) (legalCanSet, error) {
	switch kind {
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_TERMS_HISTORY:
		return legalCanSet{
			view:             policyv1.TermsHistory.View,
			edit:             policyv1.TermsHistory.Edit,
			viewArchived:     policyv1.TermsHistory.ViewArchived,
			editArchived:     policyv1.TermsHistory.EditArchived,
			delete:           policyv1.TermsHistory.Delete,
			publish:          policyv1.TermsHistory.Publish,
			manage:           policyv1.TermsHistory.Manage,
			manageShareLinks: policyv1.TermsHistory.ManageShareLinks,
		}, nil
	case intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PRIVACY_HISTORY:
		return legalCanSet{
			view:             policyv1.PrivacyHistory.View,
			edit:             policyv1.PrivacyHistory.Edit,
			viewArchived:     policyv1.PrivacyHistory.ViewArchived,
			editArchived:     policyv1.PrivacyHistory.EditArchived,
			delete:           policyv1.PrivacyHistory.Delete,
			publish:          policyv1.PrivacyHistory.Publish,
			manage:           policyv1.PrivacyHistory.Manage,
			manageShareLinks: policyv1.PrivacyHistory.ManageShareLinks,
		}, nil
	default:
		return legalCanSet{}, errs.InvalidArgument("resource.type", "must be legal policy history")
	}
}

func legalCanForAction(
	kind intrav1.CollaborationResourceType,
	resourceID string,
	action legalAction,
) (policyv1.Can, error) {
	if _, err := uuidutil.ParseCanonical(resourceID, "resource id"); err != nil {
		return policyv1.Can{}, err
	}
	set, err := legalCanSetForKind(kind)
	if err != nil {
		return policyv1.Can{}, err
	}
	switch action {
	case legalActionView:
		return set.view(resourceID)
	case legalActionEdit:
		return set.edit(resourceID)
	case legalActionViewArchived:
		return set.viewArchived(resourceID)
	case legalActionEditArchived:
		return set.editArchived(resourceID)
	case legalActionDelete:
		return set.delete(resourceID)
	case legalActionPublish:
		return set.publish(resourceID)
	case legalActionManage:
		return set.manage(resourceID)
	case legalActionManageShareLinks:
		return set.manageShareLinks(resourceID)
	default:
		return policyv1.Can{}, errs.InvalidArgument("action", "is not supported for legal policy")
	}
}

func requireLegalPermissionForPrincipal(
	ctx context.Context,
	checker CollaborationPermissionChecker,
	kind intrav1.CollaborationResourceType,
	resourceID string,
	action legalAction,
	principal *auth.UserInfo,
) error {
	if checker == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	can, err := legalCanForAction(kind, resourceID, action)
	if err != nil {
		return errs.InvalidArgument("resource.id", "must be a canonical resource UUID")
	}
	authorizationCtx := auth.WithUser(ctx, principal)
	decision, err := auth.AuthorizationDecision(authorizationCtx, can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := checker.Can(authorizationCtx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NoPermission(can.Action().Name(), "legal policy")
	}
	return nil
}
