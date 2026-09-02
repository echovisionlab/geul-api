package member

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
)

func loadMemberIdentityWithEmailCredentials(
	ctx context.Context, identity auth.IdentityGetter, identityID string,
) (*auth.Identity, error) {
	if identity == nil {
		return nil, fmt.Errorf("identity manager is required")
	}
	var result *auth.Identity
	for _, credentialType := range []string{"code", "oidc"} {
		candidate, err := identity.GetIdentityWithIncludeCredential(ctx, identityID, credentialType)
		if err != nil {
			return nil, err
		}
		if candidate == nil {
			continue
		}
		if result == nil {
			result = candidate
			if result.Credentials == nil {
				result.Credentials = map[string]auth.Credential{}
			}
		}
		if credential, ok := candidate.Credentials[strings.TrimSpace(credentialType)]; ok {
			result.Credentials[credentialType] = credential
		}
	}
	return result, nil
}
