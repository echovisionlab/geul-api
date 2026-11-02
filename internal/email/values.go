package email

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Template struct {
	ID                string
	ContentDocumentID *string
	SourceLocale      string
	Key               string
	Name              string
	Subject           string
	Description       *string
	ContentHTML       *string
	Variables         []TemplateVariable
	IsSystem          bool
	IsActive          bool
	EventKey          *string
	LayoutID          *string
	CreatedAt         time.Time
	UpdatedAt         *time.Time
	DeliveryRunCount  int32
}

type TemplateVariable struct {
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	DefaultValue *string `json:"default_value,omitempty"`
}

type MailAdapterType string

type MailAdapter struct {
	ID        string
	Name      string
	Type      MailAdapterType
	IsActive  bool
	Priority  int
	Config    json.RawMessage
	CreatedAt time.Time
	UpdatedAt *time.Time
}

type SESAdapterConfig struct {
	Region          string `json:"region"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	FromEmail       string `json:"from_email"`
	FromName        string `json:"from_name,omitempty"`
}

type SMTPAdapterConfig struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Secure    bool   `json:"secure"`
	User      string `json:"user"`
	Password  string `json:"password"`
	FromEmail string `json:"from_email"`
	FromName  string `json:"from_name,omitempty"`
}

func (m *MailAdapter) GetSESConfig() (*SESAdapterConfig, error) {
	var cfg SESAdapterConfig
	if err := decodeProviderConfig(m.Config, &cfg, "model.SESAdapterConfig"); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (m *MailAdapter) GetSMTPConfig() (*SMTPAdapterConfig, error) {
	var cfg SMTPAdapterConfig
	if err := decodeProviderConfig(m.Config, &cfg, "model.SMTPAdapterConfig"); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Administrator test sends expose these decoding errors, including their stable type labels.
func decodeProviderConfig(raw json.RawMessage, target any, typeLabel string) error {
	err := json.Unmarshal(raw, target)
	var mismatch *json.UnmarshalTypeError
	if errors.As(err, &mismatch) && mismatch.Struct == "" {
		return providerConfigDecodeError{cause: mismatch, typeLabel: typeLabel}
	}
	return err
}

type providerConfigDecodeError struct {
	cause     *json.UnmarshalTypeError
	typeLabel string
}

func (err providerConfigDecodeError) Error() string {
	return fmt.Sprintf("json: cannot unmarshal %s into Go value of type %s", err.cause.Value, err.typeLabel)
}

func (err providerConfigDecodeError) Unwrap() error { return err.cause }
