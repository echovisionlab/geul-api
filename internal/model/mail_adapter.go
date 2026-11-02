package model

import (
	"database/sql/driver"
	"encoding/json"
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
)

// MailAdapterType is the stored provider of a mail adapter. The values are the
// strings the mail_adapter_type column holds; internal/emaildelivery maps
// them to and from the API enum.
type MailAdapterType string

const (
	MailAdapterTypeLogging MailAdapterType = "MAIL_ADAPTER_TYPE_LOGGING"
	MailAdapterTypeSES     MailAdapterType = "MAIL_ADAPTER_TYPE_SES"
	MailAdapterTypeSMTP    MailAdapterType = "MAIL_ADAPTER_TYPE_SMTP"
)

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
