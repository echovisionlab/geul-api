package campaign

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCampaignRecipientScopeToProtoDoesNotDefaultInvalidPersistence(t *testing.T) {
	for _, scope := range []string{"", "UNKNOWN", " SUBSCRIBED_USERS_BUT_NOT_VALID "} {
		t.Run(scope, func(t *testing.T) {
			_, err := campaignRecipientScopeToProto(scope)
			require.Error(t, err)
		})
	}
}
