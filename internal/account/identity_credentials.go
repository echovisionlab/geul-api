package account

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
)

// loadIdentityWithCredentials combines the credential-scoped Kratos reads
// required by a caller into one identity view. Kratos may omit all other
// credential types from each response, so callers must name the complete set
// they need instead of relying on one partially populated response.
func loadIdentityWithCredentials(
	ctx context.Context,
	identityManager auth.IdentityGetter,
	identityID string,
	credentialTypes ...string,
) (*auth.Identity, error) {
	if identityManager == nil {
		return nil, fmt.Errorf("identity manager is required")
	}

	var identity *auth.Identity
	for _, credentialType := range credentialTypes {
		credentialType = strings.TrimSpace(credentialType)
		if credentialType == "" {
			continue
		}
		candidate, err := identityManager.GetIdentityWithIncludeCredential(ctx, identityID, credentialType)
		if err != nil {
			return nil, err
		}
		if candidate == nil {
			continue
		}
		if identity == nil {
			identity = candidate
			if identity.Credentials == nil {
				identity.Credentials = map[string]auth.Credential{}
			}
		}
		if credential, ok := candidate.Credentials[credentialType]; ok {
			identity.Credentials[credentialType] = credential
		}
	}
	return identity, nil
}
