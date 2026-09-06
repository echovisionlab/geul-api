package label

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func requireLabelCollaborationEdit(ctx context.Context, checker *auth.SpiceDBClient, resourceKind intrav1.CollaborationResourceType, labelID string, principal *auth.UserInfo) error {
	if checker == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if resourceKind != intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_LABEL {
		return errs.InvalidArgument("resource.type", "must be Label")
	}
	if principal == nil || !principal.Authenticated {
		return errs.AuthenticationRequired()
	}
	can, err := policyv1.Label.Edit(labelID)
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
		return errs.NoPermission(can.Action().Name(), "label")
	}
	return nil
}
