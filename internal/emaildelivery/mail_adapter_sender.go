package emaildelivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/structured"
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

// UpdateMailAdapterSender uses the same locked update, fresh administrator
// authorization, provider verification, audit append, and invalidation as
// UpdateMailAdapter. Merge only after locking so concurrent credential
// rotations are preserved.
func (s *Service) UpdateMailAdapterSender(ctx context.Context, input MailAdapterSenderUpdate) (MailAdapter, error) {
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return MailAdapter{}, err
	}
	return s.updateMailAdapter(ctx, UpdateMailAdapterCommand{
		ID: input.ID, IsActive: input.IsActive, Priority: input.Priority,
	}, &input)
}

func senderConfig(adapter MailAdapter, input MailAdapterSenderUpdate) (json.RawMessage, structured.Value, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(adapter.Config, &fields); err != nil || fields == nil {
		return nil, nil, errors.New("stored mail adapter configuration is invalid")
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
		return nil, nil, errors.New("could not encode mail adapter sender configuration")
	}
	candidate := adapter
	candidate.Config = encoded
	var config structured.Value
	switch adapter.Type {
	case AdapterTypeSES:
		stored, err := candidate.SESConfig()
		if err != nil {
			return nil, nil, errors.New("stored SES configuration is invalid")
		}
		config, err = buildConfig(adapter.Type, &SESConfigInput{
			Region: stored.Region, AccessKeyID: stored.AccessKeyID, SecretAccessKey: stored.SecretAccessKey,
			FromEmail: stored.FromEmail, FromName: &stored.FromName,
		}, nil)
		if err != nil {
			return nil, nil, invalidMailAdapter(err.Error())
		}
	case AdapterTypeSMTP:
		stored, err := candidate.SMTPConfig()
		if err != nil {
			return nil, nil, errors.New("stored SMTP configuration is invalid")
		}
		config, err = buildConfig(adapter.Type, nil, &SMTPConfigInput{
			Host: stored.Host, Port: int32(stored.Port), Secure: stored.Secure, User: stored.User, Password: stored.Password,
			FromEmail: stored.FromEmail, FromName: &stored.FromName,
		})
		if err != nil {
			return nil, nil, invalidMailAdapter(err.Error())
		}
	default:
		return nil, nil, domainerr.InvalidField("type", "sender configuration requires an SES or SMTP adapter")
	}
	// Retain the raw fields, including credentials and any future provider keys.
	return candidate.Config, config, nil
}

// replacementConfig builds the provider config an admin Update requests. For
// the same provider it is a merge over the stored config: an empty SES secret
// access key or SMTP password keeps the stored secret, which responses never
// return, and stored keys this service does not model are retained, as
// senderConfig retains them. A provider switch replaces the config whole.
func replacementConfig(
	adapter MailAdapter,
	adapterType AdapterType,
	typeChanged bool,
	command UpdateMailAdapterCommand,
) (json.RawMessage, structured.Value, error) {
	sesConfig, smtpConfig := command.SES, command.SMTP
	fields := map[string]json.RawMessage{}
	if !typeChanged {
		// An unreadable stored config has nothing to keep; the request
		// replaces it whole.
		if err := json.Unmarshal(adapter.Config, &fields); err != nil || fields == nil {
			fields = map[string]json.RawMessage{}
		}
		switch adapterType {
		case AdapterTypeSES:
			if stored, err := adapter.SESConfig(); err == nil && sesConfig != nil && sesConfig.SecretAccessKey == "" {
				withSecret := *sesConfig
				withSecret.SecretAccessKey = stored.SecretAccessKey
				sesConfig = &withSecret
			}
		case AdapterTypeSMTP:
			if stored, err := adapter.SMTPConfig(); err == nil && smtpConfig != nil && smtpConfig.Password == "" {
				withPassword := *smtpConfig
				withPassword.Password = stored.Password
				smtpConfig = &withPassword
			}
		}
		// from_name is the only modeled key a provider config omits when
		// empty, so a request without it clears the stored name.
		delete(fields, "from_name")
	}
	config, err := buildConfig(adapterType, sesConfig, smtpConfig)
	if err != nil {
		return nil, nil, invalidMailAdapter(err.Error())
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to set config: %w", err)
	}
	var requested map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &requested); err != nil {
		return nil, nil, fmt.Errorf("failed to set config: %w", err)
	}
	maps.Copy(fields, requested)
	if encoded, err = json.Marshal(fields); err != nil {
		return nil, nil, fmt.Errorf("failed to set config: %w", err)
	}
	return encoded, config, nil
}

func mailAdapterPrivateValues(adapter MailAdapter) []string {
	if config, err := adapter.SESConfig(); err == nil {
		if adapter.Type == AdapterTypeSES {
			return []string{config.AccessKeyID, config.SecretAccessKey}
		}
	}
	if config, err := adapter.SMTPConfig(); err == nil {
		return []string{config.User, config.Password}
	}
	return nil
}
