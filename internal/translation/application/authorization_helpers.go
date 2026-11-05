package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func checkSpiceDBAdmin(
	ctx context.Context,
	user *auth.UserInfo,
	spiceDB authz.AuthorizationDecisionChecker,
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

func (s *Service) requireTranslationPlatformCan(
	ctx context.Context,
	can policyv1.Can,
	canErr error,
) error {
	if canErr != nil {
		return canErr
	}
	return authz.RequireAdminCan(ctx, s.spiceDB, can)
}

// requireAuthenticatedTranslationRequester answers the onboarded, unbanned
// member making a translation request.
func requireAuthenticatedTranslationRequester(ctx context.Context) (string, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || strings.TrimSpace(user.MemberID.String()) == "" {
		return "", domainerr.AuthenticationRequired()
	}
	if user.Banned {
		return "", domainerr.AccountBanned()
	}
	if !user.Onboarded {
		return "", domainerr.NoPermission("edit", "translation")
	}
	return user.MemberID.String(), nil
}
