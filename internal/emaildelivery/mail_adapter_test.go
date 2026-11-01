package emaildelivery

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMailAdapterConfigSemanticEqualityRejectsChangedSecret(t *testing.T) {
	preflight, err := json.Marshal(&SESConfig{
		Region: "ap-northeast-2", AccessKeyID: "key", SecretAccessKey: "old-secret", FromEmail: "mail@example.test",
	})
	require.NoError(t, err)
	locked, err := json.Marshal(&SESConfig{
		Region: "ap-northeast-2", AccessKeyID: "key", SecretAccessKey: "new-secret", FromEmail: "mail@example.test",
	})
	require.NoError(t, err)
	require.False(t, configJSONSemanticallyEqual(preflight, locked))
}
