package emaildelivery

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/authz"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// MailAdapterSenderUpdate cannot carry credentials or replace provider config.
// Nil fields preserve their current values, including an omitted sender name.
type MailAdapterSenderUpdate struct {
	ID        string
	FromName  *string
	FromEmail *string
	IsActive  *bool
	Priority  *int32
}

// UpdateSender uses the same locked update, fresh administrator authorization,
// provider verification, audit append, and invalidation as the admin RPC Update.
// Merge only after locking so concurrent credential rotations are preserved.
func (s *MailAdapterService) UpdateSender(ctx context.Context, input MailAdapterSenderUpdate) (*connect.Response[managev1.UpdateMailAdapterResponse], error) {
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	return s.update(ctx, connect.NewRequest(&managev1.UpdateMailAdapterRequest{
		Id: input.ID, IsActive: input.IsActive, Priority: input.Priority,
	}), &input)
}

func (s *MailAdapterService) senderConfig(adapter *model.MailAdapter, input MailAdapterSenderUpdate) (model.MailAdapterConfig, structured.Value, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(adapter.Config.RawMessage, &fields); err != nil || fields == nil {
		return model.MailAdapterConfig{}, nil, errs.InternalMsg("stored mail adapter configuration is invalid")
	}
	if input.FromName != nil {
		// Provider configs omit empty names. Clearing an already absent name
		// must remain a semantic no-op, as it is through the admin RPC.
		if _, present := fields["from_name"]; present || *input.FromName != "" {
			fields["from_name"], _ = json.Marshal(*input.FromName)
		}
	}
	if input.FromEmail != nil {
		fields["from_email"], _ = json.Marshal(*input.FromEmail)
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return model.MailAdapterConfig{}, nil, errs.InternalMsg("could not encode mail adapter sender configuration")
	}
	candidate := *adapter
	candidate.Config = model.MailAdapterConfig{RawMessage: encoded}
	var config structured.Value
	switch adapter.Type {
	case model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES.String()):
		stored, err := candidate.GetSESConfig()
		if err != nil {
			return model.MailAdapterConfig{}, nil, errs.InternalMsg("stored SES configuration is invalid")
		}
		config, err = s.buildConfig(adapter.Type, &managev1.SESConfig{
			Region: stored.Region, AccessKeyId: stored.AccessKeyID, SecretAccessKey: stored.SecretAccessKey,
			FromEmail: stored.FromEmail, FromName: &stored.FromName,
		}, nil)
		if err != nil {
			return model.MailAdapterConfig{}, nil, errs.InvalidArgumentMsg(err.Error())
		}
	case model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP.String()):
		stored, err := candidate.GetSMTPConfig()
		if err != nil {
			return model.MailAdapterConfig{}, nil, errs.InternalMsg("stored SMTP configuration is invalid")
		}
		config, err = s.buildConfig(adapter.Type, nil, &managev1.SMTPConfig{
			Host: stored.Host, Port: int32(stored.Port), Secure: stored.Secure, User: stored.User, Password: stored.Password,
			FromEmail: stored.FromEmail, FromName: &stored.FromName,
		})
		if err != nil {
			return model.MailAdapterConfig{}, nil, errs.InvalidArgumentMsg(err.Error())
		}
	default:
		return model.MailAdapterConfig{}, nil, errs.InvalidArgument("type", "sender configuration requires an SES or SMTP adapter")
	}
	// Retain the raw fields, including credentials and any future provider keys.
	return candidate.Config, config, nil
}

func mailAdapterPrivateValues(adapter *model.MailAdapter) []string {
	if config, err := adapter.GetSESConfig(); err == nil {
		if adapter.Type == model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES.String()) {
			return []string{config.AccessKeyID, config.SecretAccessKey}
		}
	}
	if config, err := adapter.GetSMTPConfig(); err == nil {
		return []string{config.User, config.Password}
	}
	return nil
}
