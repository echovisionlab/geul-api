package label

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func requireLabelCollaborationEdit(ctx context.Context, checker *auth.SpiceDBClient, labelID string, principal *auth.UserInfo) error {
	if principal == nil || !principal.Authenticated {
		return errAuthenticationRequired
	}
	can, err := policyv1.Label.Edit(labelID)
	if err != nil {
		return domainerr.InvalidField("resource.id", "must be a canonical resource UUID")
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
	if err != nil {
		return errAuthenticationRequired.WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return domainerr.Denied(fmt.Sprintf("no permission to %s this %s", can.Action().Name(), "label"))
	}
	return nil
}
