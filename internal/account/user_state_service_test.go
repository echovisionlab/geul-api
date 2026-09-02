package account

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func TestUserStateServiceBanUpdatesMetadataDeactivatesAndRevokesSessions(t *testing.T) {
	t.Parallel()

	kratos := newRecordingBanIdentityManager()
	kratos.identity.MetadataAdmin["social_profile_synced"] = true
	svc := NewUserStateService(kratos)
	reason := "policy violation"
	until := time.Date(2026, 4, 20, 12, 30, 0, 0, time.UTC)

	err := svc.Ban(context.Background(), banTestUserID, &reason, &until)

	require.NoError(t, err)
	require.Equal(t, []string{auth.KratosStateInactive}, kratos.stateUpdates)
	require.True(t, kratos.sessionsDeleted)
	require.Equal(t, true, kratos.identity.MetadataAdmin["banned"])
	require.Equal(t, reason, kratos.identity.MetadataAdmin["ban_reason"])
	require.Equal(t, until.Format(time.RFC3339), kratos.identity.MetadataAdmin["ban_expires"])
	require.Equal(t, true, kratos.identity.MetadataAdmin["social_profile_synced"])
	require.Equal(t, auth.KratosStateInactive, kratos.identity.State)
}

func TestUserStateServiceBanStopsBeforeSessionRevokeWhenDeactivateFails(t *testing.T) {
	t.Parallel()

	kratos := newRecordingBanIdentityManager()
	kratos.setStateErr = assert.AnError
	svc := NewUserStateService(kratos)

	err := svc.Ban(context.Background(), banTestUserID, nil, nil)

	require.Error(t, err)
	require.Equal(t, []string{auth.KratosStateInactive}, kratos.stateUpdates)
	require.False(t, kratos.sessionsDeleteAttempted)
	require.NotEqual(t, true, kratos.identity.MetadataAdmin["banned"])
	require.Equal(t, auth.KratosStateActive, kratos.identity.State)
}

func TestUserStateServiceBanSessionRevokeFailurePreservesTimedBanForRetry(t *testing.T) {
	t.Parallel()

	kratos := newRecordingBanIdentityManager()
	kratos.deleteSessionsErr = assert.AnError
	svc := NewUserStateService(kratos)
	until := time.Now().UTC().Add(time.Hour)

	err := svc.Ban(context.Background(), banTestUserID, nil, &until)

	require.Error(t, err)
	require.Equal(t, auth.KratosStateInactive, kratos.identity.State)
	require.True(t, kratos.sessionsDeleteAttempted)
	require.Equal(t, true, kratos.identity.MetadataAdmin["banned"])
	require.Equal(t, until.Format(time.RFC3339), kratos.identity.MetadataAdmin["ban_expires"])
}

func TestUserStateServiceClearBanReactivatesWithoutRevokingSessions(t *testing.T) {
	t.Parallel()

	kratos := newRecordingBanIdentityManager()
	kratos.identity.State = auth.KratosStateInactive
	kratos.identity.MetadataAdmin = structured.Fields{
		"banned":                true,
		"ban_reason":            "temporary",
		"ban_expires":           time.Now().Add(time.Hour).Format(time.RFC3339),
		"social_profile_synced": true,
	}
	svc := NewUserStateService(kratos)

	err := svc.ClearBan(context.Background(), banTestUserID)

	require.NoError(t, err)
	require.Equal(t, []string{auth.KratosStateActive}, kratos.stateUpdates)
	require.False(t, kratos.sessionsDeleteAttempted)
	require.Equal(t, false, kratos.identity.MetadataAdmin["banned"])
	require.Nil(t, kratos.identity.MetadataAdmin["ban_reason"])
	require.Nil(t, kratos.identity.MetadataAdmin["ban_expires"])
	require.Equal(t, true, kratos.identity.MetadataAdmin["social_profile_synced"])
	require.Equal(t, auth.KratosStateActive, kratos.identity.State)
}

func TestUserStateServiceClearBanActivationFailurePreservesBanForRetry(t *testing.T) {
	t.Parallel()

	kratos := newRecordingBanIdentityManager()
	kratos.identity.State = auth.KratosStateInactive
	kratos.identity.MetadataAdmin = structured.Fields{
		"banned":      true,
		"ban_reason":  "temporary",
		"ban_expires": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	}
	kratos.setStateErr = assert.AnError

	err := NewUserStateService(kratos).ClearBan(context.Background(), banTestUserID)

	require.Error(t, err)
	require.Equal(t, auth.KratosStateInactive, kratos.identity.State)
	require.Equal(t, true, kratos.identity.MetadataAdmin["banned"])
	require.Equal(t, "temporary", kratos.identity.MetadataAdmin["ban_reason"])
}

func TestIdentityHasExpiredTimedBanRequiresExplicitCurrentBan(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	expired := now.Add(-time.Minute).Format(time.RFC3339)

	require.True(t, identityHasExpiredTimedBan(&auth.Identity{MetadataAdmin: structured.Fields{
		"banned":      true,
		"ban_expires": expired,
	}}, now))
	require.False(t, identityHasExpiredTimedBan(&auth.Identity{
		State: auth.KratosStateInactive,
		MetadataAdmin: structured.Fields{
			"banned":      false,
			"ban_expires": expired,
		},
	}, now))
	require.False(t, identityHasExpiredTimedBan(&auth.Identity{MetadataAdmin: structured.Fields{
		"banned":      true,
		"ban_expires": "invalid",
	}}, now))
}

func TestNewUserStateServiceRejectsMissingIdentityManager(t *testing.T) {
	require.Panics(t, func() {
		NewUserStateService(nil)
	})
}
