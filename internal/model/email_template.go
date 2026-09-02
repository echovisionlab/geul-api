package model

import (
	"database/sql/driver"
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
)

// EmailTemplateVariable represents a variable in an email template
type EmailTemplateVariable struct {
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	DefaultValue *string `json:"default_value,omitempty"`
}

// EmailTemplateVariables is a slice of EmailTemplateVariable for JSON scanning
type EmailTemplateVariables []EmailTemplateVariable

// Scan implements sql.Scanner
func (v *EmailTemplateVariables) Scan(value structured.Value) error {
	if value == nil {
		*v = nil
		return nil
	}
	return ScanJSON(value, v)
}

// Value implements driver.Valuer
func (v EmailTemplateVariables) Value() (driver.Value, error) {
	return ValueJSONDefault(v, []EmailTemplateVariable{})
}

// EmailTemplate represents an email template in the database
type EmailTemplate struct {
	ID                string                 `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	ContentDocumentID *string                `gorm:"column:content_document_id;type:uuid"`
	SourceLocale      string                 `gorm:"column:source_locale;type:text;not null;default:en"`
	Key               string                 `gorm:"column:key;type:varchar(100);not null;unique"`
	Name              string                 `gorm:"column:name;type:varchar(255);not null"`
	Subject           string                 `gorm:"-"`
	Description       *string                `gorm:"column:description;type:text"`
	ContentHTML       *string                `gorm:"-"`
	Variables         EmailTemplateVariables `gorm:"column:variables;type:jsonb;default:'[]'"`
	IsSystem          bool                   `gorm:"column:is_system;default:false"`
	IsActive          bool                   `gorm:"column:is_active"`
	EventKey          *string                `gorm:"column:event_key;type:varchar(100);unique"`
	LayoutID          *string                `gorm:"column:layout_id;type:uuid"`
	CreatedAt         time.Time              `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt         *time.Time             `gorm:"column:updated_at;default:now()"`
	DeliveryRunCount  int32                  `gorm:"-"`
}

func (EmailTemplate) TableName() string {
	return "email_template"
}
