package emailauthoring

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// templateKeyRegex validates the format of template keys
var templateKeyRegex = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var previewPlaceholderRegex = regexp.MustCompile(`{{\s*([^\s{}]+)\s*}}`)

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

func createEmailTemplateBaseRow(ctx context.Context, tx *gorm.DB, template *model.EmailTemplate) error {
	baseRow := &emailTemplateBaseRow{
		ContentDocumentID: template.ContentDocumentID,
		SourceLocale:      template.SourceLocale,
		Key:               template.Key,
		Name:              template.Name,
		Description:       template.Description,
		Variables:         template.Variables,
		IsSystem:          template.IsSystem,
		IsActive:          template.IsActive,
		EventKey:          template.EventKey,
		LayoutID:          template.LayoutID,
		CreatedAt:         template.CreatedAt,
		UpdatedAt:         template.UpdatedAt,
	}

	if err := tx.WithContext(ctx).
		Omit("ID").
		Clauses(clause.Returning{}).
		Create(baseRow).Error; err != nil {
		return err
	}

	template.ID = baseRow.ID
	template.CreatedAt = baseRow.CreatedAt
	template.UpdatedAt = baseRow.UpdatedAt
	return nil
}

// buildEmailRenderData adds the site defaults to a preview's render data.
func (s *Service) buildEmailRenderData(ctx context.Context, requestedLocale string, input map[string]string) map[string]string {
	return s.renderData.BuildEmailRenderData(ctx, s.db, s.cdnDomain, s.siteOrigin, requestedLocale, input)
}

// requireAdminCan authorizes an administrator read; an ID that cannot name
// the resource is rejected first.
func (s *Service) requireAdminCan(ctx context.Context, can policyv1.Can, canErr error) error {
	if canErr != nil {
		return domainerr.InvalidField("id", canErr.Error())
	}
	return authz.RequireAdminCan(ctx, s.spiceDB, can)
}

// ListEmailTemplatesQuery pages through Email Templates for administrators.
type ListEmailTemplatesQuery struct {
	// Limit defaults to 20 and is capped at 100.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// EmailTemplatePage is one page of Email Templates.
type EmailTemplatePage struct {
	Templates  []*managev1.EmailTemplate
	Pagination *commonv1.PaginationResponse
}

// ListEmailTemplates returns one page of Email Templates with their delivery
// run counts.
func (s *Service) ListEmailTemplates(ctx context.Context, query ListEmailTemplatesQuery) (EmailTemplatePage, error) {
	can, canErr := policyv1.EmailTemplate.List()
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return EmailTemplatePage{}, err
	}
	list, err := emailTemplateFilterConfig.ApplyFilters(s.db.WithContext(ctx).Model(&model.EmailTemplate{}), query.Filters)
	if err != nil {
		return EmailTemplatePage{}, err
	}
	var total int64
	if err := list.Count(&total).Error; err != nil {
		return EmailTemplatePage{}, err
	}
	list, err = emailTemplateSortConfig.ApplySort(list, query.Sorts)
	if err != nil {
		return EmailTemplatePage{}, err
	}
	pg := queryutil.ExtractPaginationRaw(query.Limit, query.Offset, queryutil.DefaultPaginationConfig)
	var templates []model.EmailTemplate
	if err := pg.Apply(list).Find(&templates).Error; err != nil {
		return EmailTemplatePage{}, err
	}
	pointers := make([]*model.EmailTemplate, 0, len(templates))
	for i := range templates {
		pointers = append(pointers, &templates[i])
	}
	if err := loadEmailTemplateReferenceCounts(ctx, s.db, s.references, pointers); err != nil {
		return EmailTemplatePage{}, err
	}
	protoTemplates := make([]*managev1.EmailTemplate, 0, len(templates))
	for i := range templates {
		protoTemplates = append(protoTemplates, toProtoEmailTemplate(&templates[i]))
	}
	return EmailTemplatePage{Templates: protoTemplates, Pagination: pg.BuildResponse(total)}, nil
}

// GetEmailTemplateQuery reads one Email Template.
type GetEmailTemplateQuery struct {
	ID string
}

// GetEmailTemplate returns the Email Template with its source subject and
// document.
func (s *Service) GetEmailTemplate(ctx context.Context, query GetEmailTemplateQuery) (*managev1.EmailTemplate, error) {
	can, canErr := policyv1.EmailTemplate.View(query.ID)
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return nil, err
	}
	var template model.EmailTemplate
	if err := s.db.WithContext(ctx).First(&template, "id = ?", query.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("email template", query.ID)
		}
		return nil, err
	}
	if err := loadEmailTemplateReferenceCounts(ctx, s.db, s.references, []*model.EmailTemplate{&template}); err != nil {
		return nil, err
	}
	sourceContext, err := loadEmailTemplateSourceContext(ctx, s.db, template.ID, false)
	if err != nil {
		return nil, err
	}
	subject, err := loadEmailTemplateSourceSubject(ctx, s.db, template.ID, sourceContext.SourceLocale)
	if err != nil {
		return nil, err
	}
	template.Subject = subject
	return s.toProtoEmailTemplateWithDocument(ctx, &template)
}

// validateTemplateKey validates the format of a template key.
func validateTemplateKey(key string) error {
	if key == "" {
		return fmt.Errorf("key is required")
	}
	if len(key) > 100 {
		return fmt.Errorf("key must be at most 100 characters")
	}
	if !templateKeyRegex.MatchString(key) {
		return fmt.Errorf("key must start with a lowercase letter and contain only lowercase letters, numbers, and underscores")
	}
	return nil
}

// validateTemplateName validates the format of a template name.
func validateTemplateName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if len(name) > 255 {
		return fmt.Errorf("name must be at most 255 characters")
	}
	return nil
}

// validateTemplateSubject validates the format of a template subject.
func validateTemplateSubject(subject string) error {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return fmt.Errorf("subject is required")
	}
	if len(subject) > 500 {
		return fmt.Errorf("subject must be at most 500 characters")
	}
	return nil
}

func validateEmailTemplateEventKey(eventKey string) error {
	eventKey = strings.TrimSpace(eventKey)
	for _, candidate := range automaticEmailEventKeys() {
		if string(candidate) == eventKey {
			return nil
		}
	}
	return fmt.Errorf("unsupported email event")
}

func persistNewEmailTemplateSourceMetadata(
	ctx context.Context,
	tx *gorm.DB,
	template *model.EmailTemplate,
	sourceLocale string,
	now time.Time,
) error {
	return tx.WithContext(ctx).Exec(
		`INSERT INTO email_template_translation (
			entity_id, locale, subject, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?)`,
		template.ID, sourceLocale, template.Subject, now, now,
	).Error
}

// CreateEmailTemplateCommand creates a custom Email Template.
type CreateEmailTemplateCommand struct {
	Key          string
	Name         string
	Subject      string
	SourceLocale string
	Description  *string
}

// validate checks the command and returns the template it creates, its
// source locale normalized.
func (s *Service) newEmailTemplate(command CreateEmailTemplateCommand) (*model.EmailTemplate, error) {
	if err := validateTemplateKey(command.Key); err != nil {
		return nil, domainerr.InvalidField("key", err.Error())
	}
	if err := validateTemplateName(command.Name); err != nil {
		return nil, domainerr.InvalidField("name", err.Error())
	}
	if err := validateTemplateSubject(command.Subject); err != nil {
		return nil, domainerr.InvalidField("subject", err.Error())
	}
	sourceLocale := strings.TrimSpace(command.SourceLocale)
	if sourceLocale == "" {
		return nil, domainerr.Required("source_locale")
	}
	normalizedSourceLocale := s.runtime.NormalizeSupportedLocale(sourceLocale)
	if normalizedSourceLocale == nil {
		return nil, domainerr.InvalidField("source_locale", "unsupported locale")
	}
	now := dbtime.Now()
	template := &model.EmailTemplate{
		Key:          command.Key,
		Name:         strings.TrimSpace(command.Name),
		Subject:      strings.TrimSpace(command.Subject),
		SourceLocale: *normalizedSourceLocale,
		IsActive:     true,
		CreatedAt:    now,
		UpdatedAt:    &now,
	}
	if command.Description != nil {
		description := strings.TrimSpace(*command.Description)
		template.Description = &description
	}
	return template, nil
}

// CreateEmailTemplate creates a custom Email Template with an empty source
// document and grants its policy.
func (s *Service) CreateEmailTemplate(ctx context.Context, command CreateEmailTemplateCommand) (*managev1.EmailTemplate, error) {
	template, err := mutation.Run(ctx, s.mutations, mutation.Spec[*model.EmailTemplate]{
		Access: mutation.FreshAdminCan(policyv1.EmailTemplate.Create()),
		Change: func(ctx context.Context, tx *gorm.DB) (*model.EmailTemplate, error) {
			template, err := s.newEmailTemplate(command)
			if err != nil {
				return nil, err
			}
			document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
				Profile: emailContentProfile, SourceLocale: template.SourceLocale,
			})
			if err != nil {
				return nil, emailTemplateContent.NormalizeContentBlockError(err)
			}
			documentID := document.Document.ID.String()
			template.ContentDocumentID = &documentID
			if err := createEmailTemplateBaseRow(ctx, tx, template); err != nil {
				return nil, err
			}
			return template, persistNewEmailTemplateSourceMetadata(ctx, tx, template, template.SourceLocale, template.CreatedAt)
		},
		Audit: func(template *model.EmailTemplate) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditEmailTemplateCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewEmailTemplateCreatedAuditRecord(metadata, template.ID)
			})
		},
		Relationships: func(template *model.EmailTemplate) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return policyLifecycle(policyv1.EmailTemplate.TouchPolicy, policyv1.EmailTemplate.DeletePolicy, template.ID)
		},
	})
	if err != nil {
		if dberrors.IsUniqueViolation(err) {
			return nil, domainerr.Duplicate("template", "key", command.Key)
		}
		return nil, err
	}
	return s.toProtoEmailTemplateWithDocument(ctx, template)
}

// policyLifecycle returns the relationship batch that grants a created
// resource's policy and its exact inverse; a deletion swaps the two.
func policyLifecycle(
	touch func(string) (policyv1.RelationshipMutation, error),
	remove func(string) (policyv1.RelationshipMutation, error),
	id string,
) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	touchPolicy, err := touch(id)
	if err != nil {
		return nil, nil, err
	}
	deletePolicy, err := remove(id)
	if err != nil {
		return nil, nil, err
	}
	return []policyv1.RelationshipMutation{touchPolicy}, []policyv1.RelationshipMutation{deletePolicy}, nil
}

// DeleteEmailTemplateCommand deletes an Email Template.
type DeleteEmailTemplateCommand struct {
	ID string
}

// DeleteEmailTemplate permanently deletes an unmapped template that is not
// referenced by scheduled or sending work. Terminal delivery history keeps its
// immutable render snapshot and source-version metadata while detaching the
// mutable authoring-row identity.
func (s *Service) DeleteEmailTemplate(ctx context.Context, command DeleteEmailTemplateCommand) error {
	var template model.EmailTemplate
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return lockEmailTemplateForUpdate(ctx, tx, command.ID, &template)
		},
		Access: mutation.FreshAdminCan(policyv1.EmailTemplate.Delete(command.ID)),
		Change: func(ctx context.Context, tx *gorm.DB) (string, error) {
			return template.ID, s.deleteLockedEmailTemplate(ctx, tx, &template)
		},
		Audit: func(templateID string) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditEmailTemplateDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewEmailTemplateDeletedAuditRecord(metadata, templateID)
			})
		},
		Relationships: func(templateID string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			apply, compensate, err := policyLifecycle(policyv1.EmailTemplate.TouchPolicy, policyv1.EmailTemplate.DeletePolicy, templateID)
			return compensate, apply, err
		},
	})
	return err
}

func (s *Service) deleteLockedEmailTemplate(ctx context.Context, tx *gorm.DB, template *model.EmailTemplate) error {
	if template.EventKey != nil && strings.TrimSpace(*template.EventKey) != "" {
		return domainerr.Precondition("email template must be explicitly unmapped before deletion")
	}
	if err := s.references.RequireTemplateMutable(ctx, tx, template.ID); err != nil {
		return err
	}
	if err := s.references.DetachTemplateHistory(ctx, tx, template.ID); err != nil {
		return err
	}
	documentID, err := loadEmailTemplateContentDocument(ctx, tx, template.ID, false)
	if err != nil {
		return err
	}
	if err := s.contentBlocks.DeleteDocument(ctx, tx, documentID, emailTemplateContentFence(s.references, template.ID)); err != nil {
		return emailTemplateContent.NormalizeContentBlockError(err)
	}
	return tx.WithContext(ctx).Delete(template).Error
}
