package email

import (
	"encoding/json"
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
	if err := json.Unmarshal(m.Config, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (m *MailAdapter) GetSMTPConfig() (*SMTPAdapterConfig, error) {
	var cfg SMTPAdapterConfig
	if err := json.Unmarshal(m.Config, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
