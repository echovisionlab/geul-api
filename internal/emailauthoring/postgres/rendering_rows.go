package emailauthoringpostgres

import (
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
)

func TemplateFromRow(row model.EmailTemplate) email.Template {
	var variables []email.TemplateVariable
	if row.Variables != nil {
		variables = make([]email.TemplateVariable, len(row.Variables))
		for i, value := range row.Variables {
			variables[i] = email.TemplateVariable(value)
		}
	}
	return email.Template{
		ID:                row.ID,
		ContentDocumentID: row.ContentDocumentID,
		SourceLocale:      row.SourceLocale,
		Key:               row.Key,
		Name:              row.Name,
		Subject:           row.Subject,
		Description:       row.Description,
		ContentHTML:       row.ContentHTML,
		Variables:         variables,
		IsSystem:          row.IsSystem,
		IsActive:          row.IsActive,
		EventKey:          row.EventKey,
		LayoutID:          row.LayoutID,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
		DeliveryRunCount:  row.DeliveryRunCount,
	}
}

func TemplateRow(row email.Template) model.EmailTemplate {
	var variables model.EmailTemplateVariables
	if row.Variables != nil {
		variables = make(model.EmailTemplateVariables, len(row.Variables))
		for i, value := range row.Variables {
			variables[i] = model.EmailTemplateVariable(value)
		}
	}
	return model.EmailTemplate{
		ID:                row.ID,
		ContentDocumentID: row.ContentDocumentID,
		SourceLocale:      row.SourceLocale,
		Key:               row.Key,
		Name:              row.Name,
		Subject:           row.Subject,
		Description:       row.Description,
		ContentHTML:       row.ContentHTML,
		Variables:         variables,
		IsSystem:          row.IsSystem,
		IsActive:          row.IsActive,
		EventKey:          row.EventKey,
		LayoutID:          row.LayoutID,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
		DeliveryRunCount:  row.DeliveryRunCount,
	}
}
