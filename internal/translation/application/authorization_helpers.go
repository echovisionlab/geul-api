package application

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func checkSpiceDBAdmin(
	ctx context.Context,
	user *auth.UserInfo,
	spiceDB *auth.SpiceDBClient,
) (bool, error) {
	if user == nil || spiceDB == nil {
		return false, fmt.Errorf("SpiceDB authorization is not configured")
	}
	can, err := policyv1.Platform.IsAdmin()
	if err != nil {
		return false, err
	}
	authorizationCtx := auth.WithUser(ctx, user)
	decision, err := auth.AuthorizationDecision(authorizationCtx, can)
	if err != nil {
		return false, err
	}
	return spiceDB.Can(authorizationCtx, decision)
}
