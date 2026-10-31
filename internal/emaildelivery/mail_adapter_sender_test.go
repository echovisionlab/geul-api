package emaildelivery

import (
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
)

func TestMailAdapterSenderConfigPreservesCredentialsAndUnknownFields(t *testing.T) {
	for _, test := range []struct {
		name   string
		typeID model.MailAdapterType
		config string
	}{
		{"SES", "MAIL_ADAPTER_TYPE_SES", `{"region":"ap-northeast-2","access_key_id":"rotated-key","secret_access_key":"rotated-secret","from_name":"Old name","from_email":"old@example.test","future_provider_token":{"value":"preserve-exactly"}}`},
		{"SMTP", "MAIL_ADAPTER_TYPE_SMTP", `{"host":"smtp.example.test","port":587,"secure":false,"user":"rotated-user","password":"rotated-password","from_name":"Old name","from_email":"old@example.test","future_provider_token":{"value":"preserve-exactly"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, input := range []MailAdapterSenderUpdate{
				{FromName: mailSenderPointer("")},
				{FromEmail: mailSenderPointer("new@example.test")},
				{FromName: mailSenderPointer("New name"), FromEmail: mailSenderPointer("new@example.test")},
			} {
				adapter := &model.MailAdapter{Type: test.typeID, Config: model.MailAdapterConfig{RawMessage: json.RawMessage(test.config)}}
				merged, validated, err := senderConfig(adapter, input)
				require.NoError(t, err)
				require.NotNil(t, validated)
				var before, after map[string]json.RawMessage
				require.NoError(t, json.Unmarshal([]byte(test.config), &before))
				require.NoError(t, json.Unmarshal(merged.RawMessage, &after))
				for key, original := range before {
					if key == "from_name" && input.FromName != nil {
						encoded, err := json.Marshal(*input.FromName)
						require.NoError(t, err)
						require.JSONEq(t, string(encoded), string(after[key]))
					} else if key == "from_email" && input.FromEmail != nil {
						encoded, err := json.Marshal(*input.FromEmail)
						require.NoError(t, err)
						require.JSONEq(t, string(encoded), string(after[key]))
					} else {
						require.JSONEq(t, string(original), string(after[key]), key)
					}
				}
				// Building the candidate must not mutate the locked original.
				require.Equal(t, test.config, string(adapter.Config.RawMessage))
			}
		})
	}
}

func TestMailAdapterSenderConfigReusesAdminValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		typeID model.MailAdapterType
		config string
		email  string
		code   connect.Code
	}{
		{"invalid SES email", "MAIL_ADAPTER_TYPE_SES", `{"region":"r","access_key_id":"k","secret_access_key":"s","from_email":"old@example.test"}`, "bad", connect.CodeInvalidArgument},
		{"empty SES email", "MAIL_ADAPTER_TYPE_SES", `{"region":"r","access_key_id":"k","secret_access_key":"s","from_email":"old@example.test"}`, "", connect.CodeInvalidArgument},
		{"invalid SMTP email", "MAIL_ADAPTER_TYPE_SMTP", `{"host":"h","port":587,"from_email":"old@example.test"}`, "bad", connect.CodeInvalidArgument},
		{"missing stored region", "MAIL_ADAPTER_TYPE_SES", `{"access_key_id":"k","secret_access_key":"s"}`, "new@example.test", connect.CodeInvalidArgument},
		{"logging has no sender", "MAIL_ADAPTER_TYPE_LOGGING", `{}`, "new@example.test", connect.CodeInvalidArgument},
		{"unknown type", "FUTURE_PROVIDER", `{}`, "new@example.test", connect.CodeInvalidArgument},
		{"invalid stored JSON", "MAIL_ADAPTER_TYPE_SES", `{bad`, "new@example.test", connect.CodeInternal},
		{"null stored config", "MAIL_ADAPTER_TYPE_SES", `null`, "new@example.test", connect.CodeInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := &model.MailAdapter{Type: test.typeID, Config: model.MailAdapterConfig{RawMessage: json.RawMessage(test.config)}}
			_, _, err := senderConfig(adapter, MailAdapterSenderUpdate{FromEmail: &test.email})
			require.Error(t, err)
			require.Equal(t, test.code, errs.Wrap(err).Code())
			require.Equal(t, test.config, string(adapter.Config.RawMessage))
		})
	}
}

func TestMailAdapterSenderConfigRecognizesNoOp(t *testing.T) {
	for _, test := range []struct {
		name   string
		config string
	}{
		{"Name", `{"region":"r","access_key_id":"k","secret_access_key":"s","from_name":"Name","from_email":"sender@example.test"}`},
		{"", `{"region":"r","access_key_id":"k","secret_access_key":"s","from_email":"sender@example.test"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := &model.MailAdapter{Type: "MAIL_ADAPTER_TYPE_SES", Config: model.MailAdapterConfig{RawMessage: json.RawMessage(test.config)}}
			merged, _, err := senderConfig(adapter, MailAdapterSenderUpdate{FromName: &test.name})
			require.NoError(t, err)
			require.True(t, configJSONSemanticallyEqual(adapter.Config.RawMessage, merged.RawMessage))
		})
	}
}

func TestMailAdapterSenderRejectsUnauthenticatedBeforeDependencies(t *testing.T) {
	service := &Service{}
	_, err := service.UpdateMailAdapterSender(t.Context(), MailAdapterSenderUpdate{ID: "11111111-1111-4111-8111-111111111111", FromName: mailSenderPointer("Name")})
	require.Equal(t, connect.CodeUnauthenticated, errs.Wrap(err).Code())
}

func mailSenderPointer[T any](value T) *T { return &value }
