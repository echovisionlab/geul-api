package member

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

type fakeIdentityManager struct{ identity *auth.Identity }

func (f *fakeIdentityManager) GetIdentity(_ context.Context, identityID string) (*auth.Identity, error) {
	if f.identity == nil || f.identity.ID != identityID {
		return nil, nil
	}
	return f.identity, nil
}

func (f *fakeIdentityManager) GetIdentityWithIncludeCredential(ctx context.Context, identityID, _ string) (*auth.Identity, error) {
	return f.GetIdentity(ctx, identityID)
}

func (f *fakeIdentityManager) ListIdentities(context.Context, int, int) ([]*auth.Identity, int64, error) {
	return nil, 0, nil
}

func (f *fakeIdentityManager) GetIdentityEmail(ctx context.Context, identityID string) (string, error) {
	identity, err := f.GetIdentity(ctx, identityID)
	if err != nil || identity == nil {
		return "", err
	}
	return identity.CurrentEmail(), nil
}

func (f *fakeIdentityManager) UpdateIdentityTraits(context.Context, string, structured.Fields) error {
	return nil
}

func (f *fakeIdentityManager) UpdateIdentityVerifiableAddresses(context.Context, string, []auth.VerifiableAddress) error {
	return nil
}

func (f *fakeIdentityManager) UpdateIdentityMetadataAdmin(context.Context, string, structured.Fields) error {
	return nil
}

func (f *fakeIdentityManager) SetIdentityState(context.Context, string, string) error { return nil }

func (f *fakeIdentityManager) DeleteIdentitySessions(context.Context, string) error { return nil }

func (f *fakeIdentityManager) DeleteIdentity(context.Context, string) error { return nil }
