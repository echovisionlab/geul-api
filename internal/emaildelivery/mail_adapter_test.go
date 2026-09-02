package emaildelivery

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
)

func TestMailAdapterConfigSemanticEqualityRejectsChangedSecret(t *testing.T) {
	preflight := &model.MailAdapter{Type: "MAIL_ADAPTER_TYPE_SES"}
	require.NoError(t, preflight.SetConfig(&model.SESAdapterConfig{
		Region: "ap-northeast-2", AccessKeyID: "key", SecretAccessKey: "old-secret", FromEmail: "mail@example.test",
	}))
	locked := *preflight
	require.NoError(t, locked.SetConfig(&model.SESAdapterConfig{
		Region: "ap-northeast-2", AccessKeyID: "key", SecretAccessKey: "new-secret", FromEmail: "mail@example.test",
	}))
	require.False(t, configJSONSemanticallyEqual(preflight.Config.RawMessage, locked.Config.RawMessage))
}
