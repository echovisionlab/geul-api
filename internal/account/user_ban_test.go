package account

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

const banTestUserID = "11111111-1111-1111-1111-111111111111"

func TestBanUserRequiresInactiveStateAndSessionRevocation(t *testing.T) {
	ctx := context.Background()

	t.Run("returns error when identity cannot be deactivated", func(t *testing.T) {
		kratos := newRecordingBanIdentityManager()
		kratos.setStateErr = assert.AnError
		svc := NewUserStateService(kratos)

		err := svc.Ban(ctx, banTestUserID, nil, nil)

		require.Error(t, err)
		require.Equal(t, []string{auth.KratosStateInactive}, kratos.stateUpdates)
		require.False(t, kratos.sessionsDeleted)
	})

	t.Run("returns error when existing sessions cannot be revoked", func(t *testing.T) {
		kratos := newRecordingBanIdentityManager()
		kratos.deleteSessionsErr = assert.AnError
		svc := NewUserStateService(kratos)

		err := svc.Ban(ctx, banTestUserID, nil, nil)

		require.Error(t, err)
		require.Equal(t, []string{auth.KratosStateInactive}, kratos.stateUpdates)
		require.True(t, kratos.sessionsDeleteAttempted)
	})

	t.Run("returns banned user after metadata, state, and session revoke succeed", func(t *testing.T) {
		kratos := newRecordingBanIdentityManager()
		svc := NewUserStateService(kratos)

		err := svc.Ban(ctx, banTestUserID, nil, nil)

		require.NoError(t, err)
		require.True(t, kratos.sessionsDeleted)
		require.Equal(t, auth.KratosStateInactive, kratos.identity.State)
		require.Equal(t, true, kratos.identity.MetadataAdmin["banned"])
	})
}

func TestUnbanUserRequiresActiveState(t *testing.T) {
	ctx := context.Background()

	kratos := newRecordingBanIdentityManager()
	kratos.identity.State = auth.KratosStateInactive
	kratos.identity.MetadataAdmin = structured.Fields{"banned": true}
	kratos.setStateErr = assert.AnError
	svc := NewUserStateService(kratos)

	err := svc.ClearBan(ctx, banTestUserID)

	require.Error(t, err)
	require.Equal(t, []string{auth.KratosStateActive}, kratos.stateUpdates)
}

type recordingBanIdentityManager struct {
	identity                 *auth.Identity
	setStateErr              error
	deleteSessionsErr        error
	stateUpdates             []string
	sessionsDeleted          bool
	sessionsDeleteAttempted  bool
	metadataAdminUpdateCount int
}

func newRecordingBanIdentityManager() *recordingBanIdentityManager {
	return &recordingBanIdentityManager{
		identity: &auth.Identity{
			ID:            banTestUserID,
			State:         auth.KratosStateActive,
			Traits:        structured.Fields{"email": "user@example.test"},
			MetadataAdmin: structured.Fields{},
			VerifiableAddresses: []auth.VerifiableAddress{
				{Via: "email", Value: "user@example.test", Verified: true},
			},
		},
	}
}

func (m *recordingBanIdentityManager) GetIdentity(_ context.Context, identityID string) (*auth.Identity, error) {
	if m.identity == nil || m.identity.ID != identityID {
		return nil, assert.AnError
	}
	return m.identity, nil
}

func (m *recordingBanIdentityManager) GetIdentityWithIncludeCredential(
	ctx context.Context,
	identityID, _ string,
) (*auth.Identity, error) {
	return m.GetIdentity(ctx, identityID)
}

func (m *recordingBanIdentityManager) ListIdentities(
	context.Context,
	int,
	int,
) ([]*auth.Identity, int64, error) {
	return nil, 0, nil
}

func (m *recordingBanIdentityManager) UpdateIdentityTraits(
	context.Context,
	string,
	structured.Fields,
) error {
	return nil
}

func (m *recordingBanIdentityManager) UpdateIdentityVerifiableAddresses(
	context.Context,
	string,
	[]auth.VerifiableAddress,
) error {
	return nil
}

func (m *recordingBanIdentityManager) UpdateIdentityMetadataAdmin(
	_ context.Context,
	identityID string,
	metadataAdmin structured.Fields,
) error {
	if m.identity == nil || m.identity.ID != identityID {
		return assert.AnError
	}
	m.metadataAdminUpdateCount++
	m.identity.MetadataAdmin = metadataAdmin
	return nil
}

func (m *recordingBanIdentityManager) SetIdentityState(
	_ context.Context,
	identityID string,
	state string,
) error {
	if m.identity == nil || m.identity.ID != identityID {
		return assert.AnError
	}
	m.stateUpdates = append(m.stateUpdates, state)
	if m.setStateErr != nil {
		return m.setStateErr
	}
	m.identity.State = state
	return nil
}

func (m *recordingBanIdentityManager) DeleteIdentitySessions(
	_ context.Context,
	identityID string,
) error {
	if m.identity == nil || m.identity.ID != identityID {
		return assert.AnError
	}
	m.sessionsDeleteAttempted = true
	if m.deleteSessionsErr != nil {
		return m.deleteSessionsErr
	}
	m.sessionsDeleted = true
	return nil
}

func (m *recordingBanIdentityManager) DeleteIdentity(context.Context, string) error {
	return nil
}

func (m *recordingBanIdentityManager) GetIdentityEmail(context.Context, string) (string, error) {
	return "user@example.test", nil
}
