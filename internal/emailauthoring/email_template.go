package emailauthoring

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
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

// EmailTemplateService implements the EmailTemplateService Connect handler
type EmailTemplateService struct {
	managev1connect.UnimplementedEmailTemplateServiceHandler
	db            *gorm.DB
	spiceDB       *auth.SpiceDBClient
	publisher     EmailPublisher
	runtime       EmailTemplateRuntime
	cdnDomain     string
	siteOrigin    string
	auditWriter   domainaudit.Appender
	contentBlocks *contentblock.Store
	renderData    EmailRenderDataBuilder
	references    CampaignDeliveryReferences
}

type EmailTemplateServiceOption func(*EmailTemplateService)

func WithEmailTemplateContentBlockStore(store *contentblock.Store) EmailTemplateServiceOption {
	return func(service *EmailTemplateService) {
		service.contentBlocks = store
	}
}

func WithEmailTemplateRenderDataBuilder(builder EmailRenderDataBuilder) EmailTemplateServiceOption {
	return func(service *EmailTemplateService) { service.renderData = builder }
}

func WithEmailTemplateCampaignDeliveryReferences(references CampaignDeliveryReferences) EmailTemplateServiceOption {
	return func(service *EmailTemplateService) { service.references = references }
}

func (s *EmailTemplateService) buildEmailRenderData(ctx context.Context, requestedLocale string, input map[string]string) map[string]string {
	if s.renderData != nil {
		return s.renderData.BuildEmailRenderData(ctx, s.db, s.cdnDomain, s.siteOrigin, requestedLocale, input)
	}
	input["site_origin"] = s.siteOrigin
	return input
}

// NewAuditedEmailTemplateService creates an EmailTemplateService whose
// authoritative authoring mutations append Domain Audit in the same database
// transaction.
func NewAuditedEmailTemplateService(db *gorm.DB, publisher EmailPublisher, runtime EmailTemplateRuntime, cdnDomain, siteOrigin string, auditWriter domainaudit.Appender, spiceDB *auth.SpiceDBClient, options ...EmailTemplateServiceOption) *EmailTemplateService {
	if auditWriter == nil {
		panic("email template audit writer is required")
	}
	service := NewEmailTemplateService(db, publisher, runtime, cdnDomain, siteOrigin, spiceDB, options...)
	service.auditWriter = auditWriter
	return service
}

// NewEmailTemplateService creates a new EmailTemplateService
func NewEmailTemplateService(db *gorm.DB, publisher EmailPublisher, runtime EmailTemplateRuntime, cdnDomain, siteOrigin string, spiceDB *auth.SpiceDBClient, options ...EmailTemplateServiceOption) *EmailTemplateService {
	dependencycheck.MustNotNil(db, "db")
	dependencycheck.MustNotNil(spiceDB, "spiceDB")
	dependencycheck.MustNotNil(runtime, "Email Template runtime")
	service := &EmailTemplateService{
		db:         db,
		spiceDB:    spiceDB,
		publisher:  publisher,
		runtime:    runtime,
		cdnDomain:  cdnDomain,
		siteOrigin: strings.TrimRight(strings.TrimSpace(siteOrigin), "/"),
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *EmailTemplateService) requireEmailTemplateCan(
	ctx context.Context,
	can policyv1.Can,
	canErr error,
) (policyv1.Can, error) {
	if canErr != nil {
		return policyv1.Can{}, errs.InvalidArgument("id", canErr.Error())
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return policyv1.Can{}, err
	}
	return can, nil
}

// ListEmailTemplatesAdmin returns all email templates with pagination (admin)
func (s *EmailTemplateService) ListEmailTemplatesAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListEmailTemplatesAdminRequest],
) (*connect.Response[managev1.ListEmailTemplatesAdminResponse], error) {
	can, canErr := policyv1.EmailTemplate.List()
	if _, err := s.requireEmailTemplateCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	var templates []model.EmailTemplate
	var total int64

	query := s.db.WithContext(ctx).Model(&model.EmailTemplate{})

	// Apply filters using FilterConfig
	query, err := EmailTemplateFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply sorting
	query, err = emailTemplateSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	// Apply pagination
	pg := queryutil.ExtractPagination(req.Msg.Pagination, queryutil.DefaultPaginationConfig)

	if err := pg.Apply(query).Find(&templates).Error; err != nil {
		return nil, errs.Internal(err)
	}
	templatePointers := make([]*model.EmailTemplate, 0, len(templates))
	for i := range templates {
		templatePointers = append(templatePointers, &templates[i])
	}
	if err := loadEmailTemplateReferenceCounts(ctx, s.db, s.references, templatePointers); err != nil {
		return nil, errs.Internal(err)
	}
	protoTemplates := make([]*managev1.EmailTemplate, 0, len(templates))
	for i := range templates {
		protoTemplates = append(protoTemplates, toProtoEmailTemplate(&templates[i]))
	}

	return connect.NewResponse(&managev1.ListEmailTemplatesAdminResponse{
		Templates:  protoTemplates,
		Pagination: pg.BuildResponse(total),
	}), nil
}

// GetEmailTemplate retrieves an email template by ID (admin)
func (s *EmailTemplateService) GetEmailTemplate(
	ctx context.Context,
	req *connect.Request[managev1.GetEmailTemplateRequest],
) (*connect.Response[managev1.EmailTemplate], error) {
	can, canErr := policyv1.EmailTemplate.View(req.Msg.Id)
	if _, err := s.requireEmailTemplateCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	var template model.EmailTemplate
	if err := s.db.WithContext(ctx).First(&template, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("email template", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	if err := loadEmailTemplateReferenceCounts(
		ctx,
		s.db,
		s.references,
		[]*model.EmailTemplate{&template},
	); err != nil {
		return nil, errs.Internal(err)
	}

	sourceContext, err := loadCampaignEmailSourceContext(ctx, s.db, emailTemplateContentEntity, template.ID)
	if err != nil {
		return nil, err
	}
	subject, err := loadCampaignEmailSourceSubject(ctx, s.db, emailTemplateContentEntity, template.ID, sourceContext.SourceLocale)
	if err != nil {
		return nil, err
	}
	template.Subject = subject
	protoTemplate, err := s.toProtoEmailTemplateWithDocument(ctx, &template)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(protoTemplate), nil
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
	if err := tx.WithContext(ctx).Exec(
		`INSERT INTO email_template_translation (
			entity_id, locale, subject, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?)`,
		template.ID, sourceLocale, template.Subject, now, now,
	).Error; err != nil {
		return errs.Internal(err)
	}
	return nil
}

// CreateEmailTemplate creates a new email template (admin)
func (s *EmailTemplateService) CreateEmailTemplate(
	ctx context.Context,
	req *connect.Request[managev1.CreateEmailTemplateRequest],
) (*connect.Response[managev1.EmailTemplate], error) {
	templateCan, err := policyv1.EmailTemplate.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}

	var template *model.EmailTemplate
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, templateCan); err != nil {
			return err
		}
		if s.contentBlocks == nil {
			return errs.InternalMsg("Email Template content Block store is not configured")
		}
		if err := validateTemplateKey(req.Msg.Key); err != nil {
			return errs.InvalidArgument("key", err.Error())
		}
		if err := validateTemplateName(req.Msg.Name); err != nil {
			return errs.InvalidArgument("name", err.Error())
		}
		if err := validateTemplateSubject(req.Msg.Subject); err != nil {
			return errs.InvalidArgument("subject", err.Error())
		}
		sourceLocale := strings.TrimSpace(req.Msg.SourceLocale)
		if sourceLocale == "" {
			return errs.Required("source_locale")
		}
		normalizedSourceLocale := s.runtime.NormalizeSupportedLocale(sourceLocale)
		if normalizedSourceLocale == nil {
			return errs.InvalidArgument("source_locale", "unsupported locale")
		}
		sourceLocale = *normalizedSourceLocale

		now := time.Now().UTC()
		template = &model.EmailTemplate{
			Key:          req.Msg.Key,
			Name:         strings.TrimSpace(req.Msg.Name),
			Subject:      strings.TrimSpace(req.Msg.Subject),
			SourceLocale: sourceLocale,
			IsSystem:     false,
			IsActive:     true,
			CreatedAt:    now,
			UpdatedAt:    &now,
		}
		if req.Msg.Description != nil {
			trimmedDesc := strings.TrimSpace(*req.Msg.Description)
			template.Description = &trimmedDesc
		}
		document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
			Profile:      emailContentProfile,
			SourceLocale: sourceLocale,
		})
		if err != nil {
			return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
		}
		documentID := document.Document.ID.String()
		template.ContentDocumentID = &documentID
		if err := createEmailTemplateBaseRow(ctx, tx, template); err != nil {
			return err
		}
		if err := persistNewEmailTemplateSourceMetadata(ctx, tx, template, sourceLocale, now); err != nil {
			return err
		}
		if err := appendEmailTemplateCreatedAudit(ctx, tx, s.auditWriter, template.ID); err != nil {
			return err
		}
		touchPolicy, err := policyv1.EmailTemplate.TouchPolicy(template.ID)
		if err != nil {
			return err
		}
		deletePolicy, err := policyv1.EmailTemplate.DeletePolicy(template.ID)
		if err != nil {
			return err
		}
		return write(
			[]policyv1.RelationshipMutation{touchPolicy},
			[]policyv1.RelationshipMutation{deletePolicy},
		)
	})
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return nil, errs.AlreadyExists("template", "key", req.Msg.Key)
		}
		return nil, errs.Wrap(err)
	}
	protoTemplate, err := s.toProtoEmailTemplateWithDocument(ctx, template)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(protoTemplate), nil
}

// UpdateEmailTemplate updates an email template (admin)
func (s *EmailTemplateService) UpdateEmailTemplate(
	ctx context.Context,
	req *connect.Request[managev1.UpdateEmailTemplateRequest],
) (*connect.Response[managev1.UpdateEmailTemplateResponse], error) {
	templateCan, err := policyv1.EmailTemplate.Edit(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}

	template, changed, err := s.applyEmailTemplateUpdate(ctx, req.Msg, templateCan)
	if err != nil {
		return nil, mapEmailTemplateUpdateError(err, req.Msg.Id)
	}
	if template.UpdatedAt == nil {
		return nil, errs.InternalMsg("email template updated_at is missing")
	}
	return connect.NewResponse(&managev1.UpdateEmailTemplateResponse{
		Id:          template.ID,
		Changed:     changed,
		Name:        template.Name,
		Description: template.Description,
		IsActive:    template.IsActive,
		LayoutId:    template.LayoutID,
		UpdatedAt:   timestamppb.New(*template.UpdatedAt),
	}), nil
}

// DeleteEmailTemplate permanently deletes an unmapped template that is not
// referenced by scheduled or sending work. Terminal delivery history keeps its
// immutable render snapshot and source-version metadata while detaching the
// mutable authoring-row identity.
func (s *EmailTemplateService) DeleteEmailTemplate(
	ctx context.Context,
	req *connect.Request[managev1.DeleteEmailTemplateRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	templateCan, err := policyv1.EmailTemplate.Delete(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		var template model.EmailTemplate
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&template, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("email template", req.Msg.Id)
			}
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, templateCan); err != nil {
			return err
		}
		if s.contentBlocks == nil {
			return errs.InternalMsg("Email Template content Block store is not configured")
		}
		if template.EventKey != nil && strings.TrimSpace(*template.EventKey) != "" {
			return errs.FailedPrecondition(
				"email template must be explicitly unmapped before deletion",
			)
		}
		if err := ensureEmailTemplateMutableForActiveDelivery(ctx, tx, s.references, template.ID); err != nil {
			return err
		}
		if err := s.references.DetachTemplateHistory(ctx, tx, template.ID); err != nil {
			return err
		}
		documentID, err := loadCampaignEmailContentDocumentID(ctx, tx, emailTemplateContentEntity, template.ID)
		if err != nil {
			return err
		}
		if err := s.contentBlocks.DeleteDocument(
			ctx,
			tx,
			documentID,
			campaignEmailDeleteContentFence(s.references, emailTemplateContentEntity, template.ID),
		); err != nil {
			return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
		}
		if err := tx.WithContext(ctx).Delete(&template).Error; err != nil {
			return err
		}
		if err := appendEmailTemplateDeletedAudit(ctx, tx, s.auditWriter, template.ID); err != nil {
			return err
		}
		deletePolicy, err := policyv1.EmailTemplate.DeletePolicy(template.ID)
		if err != nil {
			return err
		}
		touchPolicy, err := policyv1.EmailTemplate.TouchPolicy(template.ID)
		if err != nil {
			return err
		}
		return write(
			[]policyv1.RelationshipMutation{deletePolicy},
			[]policyv1.RelationshipMutation{touchPolicy},
		)
	})
	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

// GetEventMappings returns all event-to-template mappings (admin)
