package model

import (
	"database/sql/driver"
	"encoding/json"
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
)

// MailAdapterType defines the type of mail adapter
type MailAdapterType string

// MailAdapterConfig wraps the JSON config for mail adapters
type MailAdapterConfig struct {
	json.RawMessage
}

// Scan implements sql.Scanner for MailAdapterConfig
func (c *MailAdapterConfig) Scan(value structured.Value) error {
	if value == nil {
		c.RawMessage = json.RawMessage("{}")
		return nil
	}
	switch v := value.(type) {
	case []byte:
		c.RawMessage = v
	case string:
		c.RawMessage = json.RawMessage(v)
	}
	return nil
}

// Value implements driver.Valuer for MailAdapterConfig
func (c MailAdapterConfig) Value() (driver.Value, error) {
	if c.RawMessage == nil {
		return "{}", nil
	}
	return []byte(c.RawMessage), nil
}

// SESAdapterConfig contains AWS SES adapter configuration
type SESAdapterConfig struct {
	Region          string `json:"region"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	FromEmail       string `json:"from_email"`
	FromName        string `json:"from_name,omitempty"`
}

// SMTPAdapterConfig contains SMTP adapter configuration
type SMTPAdapterConfig struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Secure    bool   `json:"secure"`
	User      string `json:"user"`
	Password  string `json:"password"`
	FromEmail string `json:"from_email"`
	FromName  string `json:"from_name,omitempty"`
}

// MailAdapter represents a mail adapter configuration
type MailAdapter struct {
	ID        string            `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	Name      string            `gorm:"column:name;type:varchar(255);not null"`
	Type      MailAdapterType   `gorm:"column:type;type:mail_adapter_type;not null"`
	IsActive  bool              `gorm:"column:is_active;not null;default:false"`
	Priority  int               `gorm:"column:priority;not null;default:0"`
	Config    MailAdapterConfig `gorm:"column:config;type:jsonb;not null;default:'{}'"`
	CreatedAt time.Time         `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt *time.Time        `gorm:"column:updated_at;default:now()"`
}

// TableName returns the table name for MailAdapter
func (MailAdapter) TableName() string {
	return "mail_adapter"
}

// GetSESConfig unmarshals config as SES configuration
func (m *MailAdapter) GetSESConfig() (*SESAdapterConfig, error) {
	var cfg SESAdapterConfig
	if err := json.Unmarshal(m.Config.RawMessage, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// GetSMTPConfig unmarshals config as SMTP configuration
func (m *MailAdapter) GetSMTPConfig() (*SMTPAdapterConfig, error) {
	var cfg SMTPAdapterConfig
	if err := json.Unmarshal(m.Config.RawMessage, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SetConfig marshals and sets the config
func (m *MailAdapter) SetConfig(cfg structured.Value) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	m.Config.RawMessage = data
	return nil
}
