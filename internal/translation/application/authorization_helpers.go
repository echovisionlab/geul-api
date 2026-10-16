package application

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func checkSpiceDBAdmin(
	ctx context.Context,
	user *auth.UserInfo,
	spiceDB *auth.SpiceDBClient,
) (bool, error) {
	if user == nil || spiceDB == nil {
		err := fmt.Errorf("SpiceDB authorization is not configured")
		slog.ErrorContext(ctx, "Operation failed", "domain", "translation", "event", "translation.authorization.failed", "outcome", "failed", failurecause.Attr(err))
		return false, err
	}
	can, err := policyv1.Platform.IsAdmin()
	if err != nil {
		slog.ErrorContext(ctx, "Operation failed", "domain", "translation", "event", "translation.authorization.failed", "outcome", "failed", failurecause.Attr(err))
		return false, err
	}
	authorizationCtx := auth.WithUser(ctx, user)
	decision, err := auth.AuthorizationDecision(authorizationCtx, can)
	if err != nil {
		slog.WarnContext(ctx, "Operation failed", "domain", "translation", "event", "translation.authorization.failed", "outcome", "failed", failurecause.Attr(err))
		return false, err
	}
	allowed, err := spiceDB.Can(authorizationCtx, decision)
	if err != nil {
		slog.ErrorContext(ctx, "Operation failed", "domain", "translation", "event", "translation.authorization.failed", "outcome", "failed", failurecause.Attr(err))
	}
	return allowed, err
}
