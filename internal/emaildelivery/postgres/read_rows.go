package emaildeliverypostgres

import (
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
)

func MailAdapterFromRow(row model.MailAdapter) email.MailAdapter {
	return email.MailAdapter{
		ID:        row.ID,
		Name:      row.Name,
		Type:      email.MailAdapterType(row.Type),
		IsActive:  row.IsActive,
		Priority:  row.Priority,
		Config:    row.Config.RawMessage,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func MailAdapterRow(row email.MailAdapter) model.MailAdapter {
	return model.MailAdapter{
		ID:        row.ID,
		Name:      row.Name,
		Type:      model.MailAdapterType(row.Type),
		IsActive:  row.IsActive,
		Priority:  row.Priority,
		Config:    model.MailAdapterConfig{RawMessage: row.Config},
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}
