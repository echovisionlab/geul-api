package account

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
)

type fakeIdentityManager struct {
	identity *auth.Identity
}

func (f *fakeIdentityManager) GetIdentity(_ context.Context, identityID string) (*auth.Identity, error) {
	if f.identity == nil || f.identity.ID != identityID {
		return nil, nil
	}
	return f.identity, nil
}

func (f *fakeIdentityManager) GetIdentityWithIncludeCredential(ctx context.Context, identityID, _ string) (*auth.Identity, error) {
	return f.GetIdentity(ctx, identityID)
}
