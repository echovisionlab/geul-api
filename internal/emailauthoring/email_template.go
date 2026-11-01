package emailauthoring

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// templateKeyRegex validates the format of template keys
var templateKeyRegex = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var previewPlaceholderRegex = regexp.MustCompile(`{{\s*([^\s{}]+)\s*}}`)

// ListEmailTemplatesQuery pages through Email Templates for administrators.
type ListEmailTemplatesQuery struct {
	// Limit defaults to 20 and is capped at 100.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// EmailTemplatePage is one page of Email Templates and the total of every
// match.
type EmailTemplatePage struct {
	Templates []Template
	Page      queryutil.Pagination
	Total     int64
}

// ListEmailTemplates returns one page of Email Templates with their delivery
// run counts.
func (s *Service) ListEmailTemplates(ctx context.Context, query ListEmailTemplatesQuery) (EmailTemplatePage, error) {
	can, canErr := policyv1.EmailTemplate.List()
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return EmailTemplatePage{}, err
	}
	page := EmailTemplatePage{Page: queryutil.ExtractPaginationRaw(query.Limit, query.Offset, queryutil.DefaultPaginationConfig)}
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		page.Templates, page.Total, err = store.ListTemplates(ctx, ListQuery{Filters: query.Filters, Sorts: query.Sorts, Page: page.Page})
		if err != nil {
			return err
		}
		return loadTemplateReferenceCounts(ctx, store, page.Templates)
	})
	if err != nil {
		return EmailTemplatePage{}, err
	}
	return page, nil
}

// GetEmailTemplateQuery reads one Email Template.
type GetEmailTemplateQuery struct {
	ID string
}

// TemplateDocument is an Email Template with its source document.
type TemplateDocument struct {
	Template
	Projection TemplateEditorProjection
}

// GetEmailTemplate returns the Email Template with its source subject and
// document.
func (s *Service) GetEmailTemplate(ctx context.Context, query GetEmailTemplateQuery) (TemplateDocument, error) {
	can, canErr := policyv1.EmailTemplate.View(query.ID)
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return TemplateDocument{}, err
	}
	var document TemplateDocument
	// The source document share-locks its rows, so the read is not read
	// only.
	err := s.units.ReadLocked(ctx, func(store Store) error {
		template, err := findTemplate(ctx, store, query.ID)
		if err != nil {
			return err
		}
		templates := []Template{template}
		if err := loadTemplateReferenceCounts(ctx, store, templates); err != nil {
			return err
		}
		template = templates[0]
		sourceLocale, err := loadTemplateSourceLocale(ctx, store, template.ID, false)
		if err != nil {
			return err
		}
		if template.Subject, err = loadTemplateSourceSubject(ctx, store, template.ID, sourceLocale); err != nil {
			return err
		}
		document, err = templateWithDocument(ctx, store, template)
		return err
	})
	return document, err
}

// findTemplate reads the template, answering a missing one as not found.
func findTemplate(ctx context.Context, store Store, templateID string) (Template, error) {
	template, err := store.FindTemplate(ctx, templateID)
	if errors.Is(err, ErrTemplateNotFound) {
		return Template{}, domainerr.ResourceNotFound("email template", templateID)
	}
	return template, err
}

// templateWithDocument adds the template's source document to it.
func templateWithDocument(ctx context.Context, store Store, template Template) (TemplateDocument, error) {
	projection, err := loadTemplateEditorProjection(ctx, store, template.ID)
	if err != nil {
		return TemplateDocument{}, err
	}
	return TemplateDocument{Template: template, Projection: projection}, nil
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

// CreateEmailTemplateCommand creates a custom Email Template.
type CreateEmailTemplateCommand struct {
	Key          string
	Name         string
	Subject      string
	SourceLocale string
	Description  *string
}

// newEmailTemplate checks the command and returns the template it creates,
// its source locale normalized.
func (s *Service) newEmailTemplate(command CreateEmailTemplateCommand) (Template, error) {
	if err := validateTemplateKey(command.Key); err != nil {
		return Template{}, domainerr.InvalidField("key", err.Error())
	}
	if err := validateTemplateName(command.Name); err != nil {
		return Template{}, domainerr.InvalidField("name", err.Error())
	}
	if err := validateTemplateSubject(command.Subject); err != nil {
		return Template{}, domainerr.InvalidField("subject", err.Error())
	}
	sourceLocale := strings.TrimSpace(command.SourceLocale)
	if sourceLocale == "" {
		return Template{}, domainerr.Required("source_locale")
	}
	normalizedSourceLocale := s.locales.NormalizeSupportedLocale(sourceLocale)
	if normalizedSourceLocale == nil {
		return Template{}, domainerr.InvalidField("source_locale", "unsupported locale")
	}
	now := dbtime.Now()
	template := Template{
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
func (s *Service) CreateEmailTemplate(ctx context.Context, command CreateEmailTemplateCommand) (TemplateDocument, error) {
	template, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Template, Store]{
		Access: mutation.FreshAdminCan(policyv1.EmailTemplate.Create()),
		Change: func(ctx context.Context, store Store) (Template, error) {
			template, err := s.newEmailTemplate(command)
			if err != nil {
				return Template{}, err
			}
			created, err := store.CreateContentDocument(ctx, contentblock.CreateInput{
				Profile: emailContentProfile, SourceLocale: template.SourceLocale,
			})
			if err != nil {
				return Template{}, emailTemplateContent.NormalizeContentBlockError(err)
			}
			documentID := created.Document.ID.String()
			template.ContentDocumentID = &documentID
			if err := store.InsertTemplate(ctx, &template); err != nil {
				return Template{}, err
			}
			subject := template.Subject
			_, err = store.InsertLocale(ctx, template.ID, template.SourceLocale, &subject, template.CreatedAt)
			return template, err
		},
		Audit: func(template Template) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditEmailTemplateCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewEmailTemplateCreatedAuditRecord(metadata, template.ID)
			})
		},
		Relationships: func(template Template) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return policyLifecycle(policyv1.EmailTemplate.TouchPolicy, policyv1.EmailTemplate.DeletePolicy, template.ID)
		},
	})
	if err != nil {
		if errors.Is(err, ErrTemplateKeyTaken) {
			return TemplateDocument{}, domainerr.Duplicate("template", "key", command.Key)
		}
		return TemplateDocument{}, err
	}
	var document TemplateDocument
	err = s.units.ReadLocked(ctx, func(store Store) error {
		document, err = templateWithDocument(ctx, store, template)
		return err
	})
	return document, err
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
	var template Template
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			template, err = lockTemplateForUpdate(ctx, store, command.ID)
			return err
		},
		Access: mutation.FreshAdminCan(policyv1.EmailTemplate.Delete(command.ID)),
		Change: func(ctx context.Context, store Store) (string, error) {
			return template.ID, deleteLockedEmailTemplate(ctx, store, template)
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

func deleteLockedEmailTemplate(ctx context.Context, store Store, template Template) error {
	if template.EventKey != nil && strings.TrimSpace(*template.EventKey) != "" {
		return domainerr.Precondition("email template must be explicitly unmapped before deletion")
	}
	if err := store.RequireTemplateMutable(ctx, template.ID); err != nil {
		return err
	}
	if err := store.DetachTemplateHistory(ctx, template.ID); err != nil {
		return err
	}
	documentID, err := loadTemplateContentDocument(ctx, store, template.ID, false)
	if err != nil {
		return err
	}
	if err := store.DeleteContentDocument(ctx, documentID, emailTemplateContentFence(store, template.ID)); err != nil {
		return emailTemplateContent.NormalizeContentBlockError(err)
	}
	return store.DeleteTemplate(ctx, template.ID)
}

// lockTemplateForUpdate locks the template, answering a missing one as not
// found.
func lockTemplateForUpdate(ctx context.Context, store Store, templateID string) (Template, error) {
	template, err := store.LockTemplate(ctx, templateID)
	if errors.Is(err, ErrTemplateNotFound) {
		return Template{}, domainerr.ResourceNotFound("email template", templateID)
	}
	return template, err
}

// loadTemplateReferenceCounts sets each template's delivery run count.
func loadTemplateReferenceCounts(ctx context.Context, store Store, templates []Template) error {
	ids := make([]string, 0, len(templates))
	for index := range templates {
		templates[index].DeliveryRunCount = 0
		ids = append(ids, templates[index].ID)
	}
	if len(ids) == 0 {
		return nil
	}
	counts, err := store.TemplateDeliveryRunCounts(ctx, ids)
	if err != nil {
		return err
	}
	for index := range templates {
		templates[index].DeliveryRunCount = counts[templates[index].ID]
	}
	return nil
}
