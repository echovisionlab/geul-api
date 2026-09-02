package email

import (
	"fmt"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Adapter represents a mail sending adapter with metadata
type Adapter interface {
	Sender
	ID() string
	Name() string
	Type() model.MailAdapterType
}

// AdapterFactory creates production delivery adapters. Non-delivery adapters
// are intentionally excluded from this runtime factory.
type AdapterFactory struct{}

// NewAdapterFactory creates a new AdapterFactory
func NewAdapterFactory() *AdapterFactory {
	return &AdapterFactory{}
}

// Create builds an Adapter from a MailAdapter model
func (f *AdapterFactory) Create(adapter *model.MailAdapter) (Adapter, error) {
	switch adapter.Type {
	case model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_LOGGING.String()):
		return nil, fmt.Errorf("logging mail adapter is non-delivery and cannot be activated")

	case model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES.String()):
		cfg, err := adapter.GetSESConfig()
		if err != nil {
			return nil, fmt.Errorf("invalid SES config: %w", err)
		}
		return NewSESAdapter(adapter.ID, adapter.Name, cfg)

	case model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP.String()):
		cfg, err := adapter.GetSMTPConfig()
		if err != nil {
			return nil, fmt.Errorf("invalid SMTP config: %w", err)
		}
		return NewSMTPAdapter(adapter.ID, adapter.Name, cfg)

	default:
		return nil, fmt.Errorf("unknown adapter type: %s", adapter.Type)
	}
}
