package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func TestHandleUnbanExpiredClearsOnlyExpiredTimedBans(t *testing.T) {
	now := time.Now().UTC()
	expired := workerUnbanIdentity("expired", auth.KratosStateInactive, structured.Fields{
		"banned":      true,
		"ban_reason":  "spam",
		"ban_expires": now.Add(-time.Minute).Format(time.RFC3339),
	})
	future := workerUnbanIdentity("future", auth.KratosStateInactive, structured.Fields{
		"banned":      true,
		"ban_reason":  "cooldown",
		"ban_expires": now.Add(time.Hour).Format(time.RFC3339),
	})
	permanent := workerUnbanIdentity("permanent", auth.KratosStateInactive, structured.Fields{
		"banned":     true,
		"ban_reason": "abuse",
	})
	invalidExpiry := workerUnbanIdentity("invalid-expiry", auth.KratosStateInactive, structured.Fields{
		"banned":      true,
		"ban_expires": "not-rfc3339",
	})
	inactiveWithoutBanMetadata := workerUnbanIdentity("inactive-no-ban-metadata", auth.KratosStateInactive, nil)
	inactiveWithClearedBan := workerUnbanIdentity("inactive-cleared-ban", auth.KratosStateInactive, structured.Fields{
		"banned":      false,
		"ban_expires": now.Add(-time.Hour).Format(time.RFC3339),
	})
	active := workerUnbanIdentity("active", auth.KratosStateActive, structured.Fields{
		"banned": false,
	})

	kratos := newWorkerUnbanIdentityManager([][]*auth.Identity{{
		expired,
		future,
		permanent,
		invalidExpiry,
		inactiveWithoutBanMetadata,
		inactiveWithClearedBan,
		active,
	}})
	handlers := &Handlers{kratosClient: kratos}

	require.NoError(t, handlers.handleUnbanExpiredWith(
		context.Background(),
		workerUnbanClearer(kratos),
	))

	require.Equal(t, []string{"expired"}, kratos.metadataUpdateOrder)
	require.Equal(t, []string{"expired"}, kratos.stateUpdateOrder)
	require.Equal(t, auth.KratosStateActive, expired.State)
	require.Equal(t, structured.Fields{
		"banned":      false,
		"ban_reason":  nil,
		"ban_expires": nil,
	}, expired.MetadataAdmin)

	require.Equal(t, auth.KratosStateInactive, future.State)
	require.Equal(t, "cooldown", future.MetadataAdmin["ban_reason"])
	require.Equal(t, auth.KratosStateInactive, permanent.State)
	require.Equal(t, "abuse", permanent.MetadataAdmin["ban_reason"])
	require.Equal(t, auth.KratosStateInactive, invalidExpiry.State)
	require.Nil(t, inactiveWithoutBanMetadata.MetadataAdmin)
	require.Equal(t, auth.KratosStateInactive, inactiveWithClearedBan.State)
	require.Equal(t, auth.KratosStateActive, active.State)
}

func TestHandleUnbanExpiredPaginatesIdentities(t *testing.T) {
	now := time.Now().UTC()
	firstPage := make([]*auth.Identity, 100)
	for i := range firstPage {
		firstPage[i] = workerUnbanIdentity(fmt.Sprintf("active-%03d", i), auth.KratosStateActive, structured.Fields{
			"banned": false,
		})
	}
	firstExpired := workerUnbanIdentity("first-expired", auth.KratosStateInactive, structured.Fields{
		"banned":      true,
		"ban_expires": now.Add(-2 * time.Minute).Format(time.RFC3339),
	})
	secondExpired := workerUnbanIdentity("second-expired", auth.KratosStateInactive, structured.Fields{
		"banned":      true,
		"ban_expires": now.Add(-time.Minute).Format(time.RFC3339),
	})
	firstPage[17] = firstExpired

	kratos := newWorkerUnbanIdentityManager([][]*auth.Identity{firstPage, []*auth.Identity{secondExpired}})
	handlers := &Handlers{kratosClient: kratos}

	require.NoError(t, handlers.handleUnbanExpiredWith(
		context.Background(),
		workerUnbanClearer(kratos),
	))

	require.Equal(t, []int{0, 1}, kratos.listPages)
	require.Equal(t, []string{"first-expired", "second-expired"}, kratos.metadataUpdateOrder)
	require.Equal(t, auth.KratosStateActive, firstExpired.State)
	require.Equal(t, auth.KratosStateActive, secondExpired.State)
}

func TestHandleUnbanExpiredReturnsListIdentityError(t *testing.T) {
	kratos := newWorkerUnbanIdentityManager(nil)
	kratos.listErr = errors.New("kratos unavailable")
	handlers := &Handlers{kratosClient: kratos}

	err := handlers.handleUnbanExpiredWith(context.Background(), workerUnbanClearer(kratos))

	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to list identities")
	require.Empty(t, kratos.metadataUpdateOrder)
	require.Empty(t, kratos.stateUpdateOrder)
}

func workerUnbanClearer(
	manager *workerUnbanIdentityManager,
) func(context.Context, string, time.Time) (bool, error) {
	return func(ctx context.Context, userID string, _ time.Time) (bool, error) {
		if err := account.NewUserStateService(manager).ClearBan(ctx, userID); err != nil {
			return false, err
		}
		return true, nil
	}
}

func workerUnbanIdentity(id string, state string, metadataAdmin structured.Fields) *auth.Identity {
	return &auth.Identity{
		ID:            id,
		State:         state,
		Traits:        structured.Fields{"email": id + "@example.test"},
		MetadataAdmin: metadataAdmin,
	}
}

type workerUnbanIdentityManager struct {
	identities          map[string]*auth.Identity
	pages               [][]*auth.Identity
	listPages           []int
	listErr             error
	metadataUpdateOrder []string
	stateUpdateOrder    []string
}

func newWorkerUnbanIdentityManager(pages [][]*auth.Identity) *workerUnbanIdentityManager {
	manager := &workerUnbanIdentityManager{
		identities: make(map[string]*auth.Identity),
		pages:      pages,
	}
	for _, page := range pages {
		for _, identity := range page {
			manager.identities[identity.ID] = identity
		}
	}
	return manager
}

func (m *workerUnbanIdentityManager) GetIdentity(_ context.Context, identityID string) (*auth.Identity, error) {
	identity, ok := m.identities[identityID]
	if !ok {
		return nil, fmt.Errorf("identity %s not found", identityID)
	}
	return identity, nil
}

func (m *workerUnbanIdentityManager) GetIdentityWithIncludeCredential(
	ctx context.Context,
	identityID string,
	_ string,
) (*auth.Identity, error) {
	return m.GetIdentity(ctx, identityID)
}

func (m *workerUnbanIdentityManager) ListIdentities(
	_ context.Context,
	page int,
	_ int,
) ([]*auth.Identity, int64, error) {
	m.listPages = append(m.listPages, page)
	if m.listErr != nil {
		return nil, 0, m.listErr
	}
	if page < 0 || page >= len(m.pages) {
		return []*auth.Identity{}, int64(len(m.identities)), nil
	}
	return m.pages[page], int64(len(m.identities)), nil
}

func (m *workerUnbanIdentityManager) UpdateIdentityTraits(
	context.Context,
	string,
	structured.Fields,
) error {
	return nil
}

func (m *workerUnbanIdentityManager) UpdateIdentityVerifiableAddresses(
	context.Context,
	string,
	[]auth.VerifiableAddress,
) error {
	return nil
}

func (m *workerUnbanIdentityManager) UpdateIdentityMetadataAdmin(
	_ context.Context,
	identityID string,
	metadataAdmin structured.Fields,
) error {
	identity, ok := m.identities[identityID]
	if !ok {
		return fmt.Errorf("identity %s not found", identityID)
	}
	m.metadataUpdateOrder = append(m.metadataUpdateOrder, identityID)
	identity.MetadataAdmin = metadataAdmin
	return nil
}

func (m *workerUnbanIdentityManager) SetIdentityState(
	_ context.Context,
	identityID string,
	state string,
) error {
	identity, ok := m.identities[identityID]
	if !ok {
		return fmt.Errorf("identity %s not found", identityID)
	}
	m.stateUpdateOrder = append(m.stateUpdateOrder, identityID)
	identity.State = state
	return nil
}

func (m *workerUnbanIdentityManager) DeleteIdentitySessions(context.Context, string) error {
	return nil
}

func (m *workerUnbanIdentityManager) DeleteIdentity(context.Context, string) error {
	return nil
}

func (m *workerUnbanIdentityManager) GetIdentityEmail(ctx context.Context, identityID string) (string, error) {
	identity, err := m.GetIdentity(ctx, identityID)
	if err != nil {
		return "", err
	}
	email := identity.GetTraitString("email")
	if email == nil {
		return "", nil
	}
	return *email, nil
}
