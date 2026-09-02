package public

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
)

type resolvedMediaAccess struct {
	directDraft           bool
	directDraftIdentityID string
	directDraftMemberID   string
}

func authenticatedAccountIdentity(ctx context.Context) bool {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.Banned || strings.TrimSpace(user.IdentityID.String()) == "" {
		return false
	}
	return true
}

func hasDraftResourceView(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	action auth.ResourceAction,
	resourceID string,
) (bool, error) {
	if !authenticatedAccountIdentity(ctx) {
		return false, nil
	}
	can, err := action(resourceID)
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, err
	}
	return spiceDB.Can(ctx, decision)
}
