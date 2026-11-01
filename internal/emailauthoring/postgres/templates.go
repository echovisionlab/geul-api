package emailauthoringpostgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// emailTemplateSortConfig defines allowed sort fields for email templates
var emailTemplateSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"key":        "key",
		"created_at": "created_at",
		"updated_at": "updated_at",
		"is_active":  "is_active",
		"is_system":  "is_system",
	},
	DefaultSort: "name ASC",
}

// emailTemplateFilterConfig defines filter fields for ListEmailTemplates.
var emailTemplateFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:          queryutil.TypeText,
			AllowedOps:    queryutil.SearchOps,
			SearchColumns: []string{"name", "key"},
		},
	},
}

// emailTemplateBaseRow inserts a template without the columns the row no
// longer has.
type emailTemplateBaseRow struct {
	ID                string                       `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	ContentDocumentID *string                      `gorm:"column:content_document_id;type:uuid;not null"`
	SourceLocale      string                       `gorm:"column:source_locale;type:text;not null;default:en"`
	Key               string                       `gorm:"column:key;type:varchar(100);uniqueIndex;not null"`
	Name              string                       `gorm:"column:name;type:varchar(255);not null"`
	Description       *string                      `gorm:"column:description;type:text"`
	Variables         model.EmailTemplateVariables `gorm:"column:variables;type:jsonb;default:'[]'"`
	IsSystem          bool                         `gorm:"column:is_system;default:false"`
	IsActive          bool                         `gorm:"column:is_active"`
	EventKey          *string                      `gorm:"column:event_key;type:varchar(100);uniqueIndex"`
	LayoutID          *string                      `gorm:"column:layout_id;type:uuid"`
	CreatedAt         time.Time                    `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt         *time.Time                   `gorm:"column:updated_at;default:now()"`
}

func (emailTemplateBaseRow) TableName() string {
	return "email_template"
}

func variablesOf(variables model.EmailTemplateVariables) []emailauthoring.TemplateVariable {
	if variables == nil {
		return nil
	}
	converted := make([]emailauthoring.TemplateVariable, len(variables))
	for index, variable := range variables {
		converted[index] = emailauthoring.TemplateVariable{
			Name: variable.Name, Description: variable.Description, DefaultValue: variable.DefaultValue,
		}
	}
	return converted
}

func variableRows(variables []emailauthoring.TemplateVariable) model.EmailTemplateVariables {
	if variables == nil {
		return nil
	}
	converted := make(model.EmailTemplateVariables, len(variables))
	for index, variable := range variables {
		converted[index] = model.EmailTemplateVariable{
			Name: variable.Name, Description: variable.Description, DefaultValue: variable.DefaultValue,
		}
	}
	return converted
}

func templateOf(row model.EmailTemplate) emailauthoring.Template {
	return emailauthoring.Template{
		ID: row.ID, ContentDocumentID: row.ContentDocumentID, SourceLocale: row.SourceLocale,
		Key: row.Key, Name: row.Name, Subject: row.Subject, Description: row.Description,
		ContentHTML: row.ContentHTML, Variables: variablesOf(row.Variables),
		IsSystem: row.IsSystem, IsActive: row.IsActive, EventKey: row.EventKey, LayoutID: row.LayoutID,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, DeliveryRunCount: row.DeliveryRunCount,
	}
}

func templateRow(template emailauthoring.Template) model.EmailTemplate {
	return model.EmailTemplate{
		ID: template.ID, ContentDocumentID: template.ContentDocumentID, SourceLocale: template.SourceLocale,
		Key: template.Key, Name: template.Name, Subject: template.Subject, Description: template.Description,
		ContentHTML: template.ContentHTML, Variables: variableRows(template.Variables),
		IsSystem: template.IsSystem, IsActive: template.IsActive, EventKey: template.EventKey, LayoutID: template.LayoutID,
		CreatedAt: template.CreatedAt, UpdatedAt: template.UpdatedAt, DeliveryRunCount: template.DeliveryRunCount,
	}
}

func templatesOf(rows []model.EmailTemplate) []emailauthoring.Template {
	templates := make([]emailauthoring.Template, len(rows))
	for index := range rows {
		templates[index] = templateOf(rows[index])
	}
	return templates
}

func (s *store) ListTemplates(ctx context.Context, query emailauthoring.ListQuery) ([]emailauthoring.Template, int64, error) {
	list, err := emailTemplateFilterConfig.ApplyFilters(s.tx.WithContext(ctx).Model(&model.EmailTemplate{}), query.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := list.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	list, err = emailTemplateSortConfig.ApplySort(list, query.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var rows []model.EmailTemplate
	if err := query.Page.Apply(list).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return templatesOf(rows), total, nil
}

func (s *store) FindTemplate(ctx context.Context, templateID string) (emailauthoring.Template, error) {
	var row model.EmailTemplate
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", templateID).Error; err != nil {
		return emailauthoring.Template{}, notFound(err, emailauthoring.ErrTemplateNotFound)
	}
	return templateOf(row), nil
}

func (s *store) LockTemplate(ctx context.Context, templateID string) (emailauthoring.Template, error) {
	var row model.EmailTemplate
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&row, "id = ?", templateID).Error; err != nil {
		return emailauthoring.Template{}, notFound(err, emailauthoring.ErrTemplateNotFound)
	}
	return templateOf(row), nil
}

func (s *store) TemplateLayoutID(ctx context.Context, templateID string) (*string, error) {
	var observed struct {
		LayoutID *string `gorm:"column:layout_id"`
	}
	if err := s.tx.WithContext(ctx).
		Model(&model.EmailTemplate{}).
		Select("layout_id").
		First(&observed, "id = ?", templateID).Error; err != nil {
		return nil, notFound(err, emailauthoring.ErrTemplateNotFound)
	}
	return observed.LayoutID, nil
}

func (s *store) TemplateDocument(ctx context.Context, templateID string, lock bool) (*string, error) {
	var root struct {
		ID                string         `gorm:"column:id"`
		ContentDocumentID sql.NullString `gorm:"column:content_document_id"`
		SourceLocale      string         `gorm:"column:source_locale"`
	}
	query := s.tx.WithContext(ctx).
		Table("email_template").
		Select("id", "content_document_id", "source_locale").
		Where("id = ?", templateID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	if err := query.Take(&root).Error; err != nil {
		return nil, notFound(err, emailauthoring.ErrTemplateNotFound)
	}
	if !root.ContentDocumentID.Valid {
		return nil, nil
	}
	return &root.ContentDocumentID.String, nil
}

func (s *store) TemplateSourceLocale(ctx context.Context, templateID string, lock bool) (string, error) {
	var state struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	query := s.tx.WithContext(ctx).Table("email_template").Select("source_locale").Where("id = ?", templateID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	if err := query.Take(&state).Error; err != nil {
		return "", notFound(err, emailauthoring.ErrTemplateNotFound)
	}
	return state.SourceLocale, nil
}

func (s *store) InsertTemplate(ctx context.Context, template *emailauthoring.Template) error {
	baseRow := &emailTemplateBaseRow{
		ContentDocumentID: template.ContentDocumentID,
		SourceLocale:      template.SourceLocale,
		Key:               template.Key,
		Name:              template.Name,
		Description:       template.Description,
		Variables:         variableRows(template.Variables),
		IsSystem:          template.IsSystem,
		IsActive:          template.IsActive,
		EventKey:          template.EventKey,
		LayoutID:          template.LayoutID,
		CreatedAt:         template.CreatedAt,
		UpdatedAt:         template.UpdatedAt,
	}
	if err := s.tx.WithContext(ctx).
		Omit("ID").
		Clauses(clause.Returning{}).
		Create(baseRow).Error; err != nil {
		return keyTaken(err, emailauthoring.ErrTemplateKeyTaken)
	}
	template.ID = baseRow.ID
	template.CreatedAt = baseRow.CreatedAt
	template.UpdatedAt = baseRow.UpdatedAt
	return nil
}

func (s *store) UpdateTemplate(ctx context.Context, templateID string, change emailauthoring.TemplateChange) error {
	updates := structured.Fields{"updated_at": change.UpdatedAt}
	if change.Name != nil {
		updates["name"] = *change.Name
	}
	if change.Description != nil {
		updates["description"] = *change.Description
	}
	if change.IsActive != nil {
		updates["is_active"] = *change.IsActive
	}
	if change.SetLayout {
		if change.LayoutID == nil {
			updates["layout_id"] = nil
		} else {
			updates["layout_id"] = *change.LayoutID
		}
	}
	return s.tx.WithContext(ctx).Model(&model.EmailTemplate{ID: templateID}).Updates(updates).Error
}

func (s *store) SetCustomTemplateVariables(ctx context.Context, templateID string, variables []emailauthoring.TemplateVariable) error {
	return s.tx.WithContext(ctx).Model(&emailTemplateBaseRow{}).
		Where("id = ? AND is_system = FALSE", templateID).Update("variables", variableRows(variables)).Error
}

func (s *store) DeleteTemplate(ctx context.Context, templateID string) error {
	return s.tx.WithContext(ctx).Delete(&model.EmailTemplate{ID: templateID}).Error
}

func (s *store) MappedTemplates(ctx context.Context) ([]emailauthoring.Template, error) {
	var rows []model.EmailTemplate
	if err := s.tx.WithContext(ctx).
		Where("event_key IS NOT NULL").
		Order("event_key ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return templatesOf(rows), nil
}

func (s *store) LockEventMappingTemplates(ctx context.Context, eventKey, targetID string) ([]emailauthoring.Template, error) {
	var rows []model.EmailTemplate
	query := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("event_key = ?", eventKey)
	if targetID != "" {
		query = query.Or("id = ?", targetID)
	}
	if err := query.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return templatesOf(rows), nil
}

func (s *store) ClearEventMapping(_ context.Context, eventKey string, now time.Time) error {
	return s.tx.Model(&model.EmailTemplate{}).
		Where("event_key = ?", eventKey).
		Updates(structured.Fields{"event_key": nil, "updated_at": now}).Error
}

func (s *store) SetEventMapping(_ context.Context, templateID, eventKey string, now time.Time) error {
	return s.tx.Model(&model.EmailTemplate{}).
		Where("id = ?", templateID).
		Updates(structured.Fields{"event_key": eventKey, "updated_at": now}).Error
}

func (s *store) LockTranslationSource(ctx context.Context, entityType, entityID string) error {
	sentinel := emailauthoring.ErrTemplateNotFound
	if entityType == "email_layout" {
		sentinel = emailauthoring.ErrLayoutNotFound
	}
	var root struct {
		ID string `gorm:"column:id"`
	}
	result := s.tx.WithContext(ctx).Table(entityType).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id").Where("id = ?", entityID).Take(&root)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return sentinelError{error: result.Error, sentinel: sentinel}
		}
		return result.Error
	}
	return nil
}
