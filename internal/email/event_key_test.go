package email

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEventKeysExcludeRemovedMailEvents(t *testing.T) {
	eventKeys := []EventKey{
		EventAccountDeletionConfirm,
		EventAccountDeletionScheduled,
		EventAccountDeletionCancelled,
		EventAccountDeletionComplete,
		EventAccountRecoveryConfirm,
		EventAccountRecoveryComplete,
		EventPrimaryEmailChanged,
		EventEmailAdded,
		EventEmailRemoved,
		EventPasskeyAdded,
		EventPasskeyRemoved,
		EventSocialLoginAdded,
		EventSocialLoginRemoved,
		EventWelcome,
		EventTermsUpdate,
		EventTermsEffective,
		EventPrivacyUpdate,
		EventPrivacyEffective,
		EventVerificationCode,
		EventLoginCode,
		EventRegistrationCode,
	}

	for _, eventKey := range eventKeys {
		require.NotEqual(t, "email_change_verify", eventKey.String())
		require.NotEqual(t, "new_location_login", eventKey.String())
	}
}
