package emailauthoring

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type emailTemplateUpdatePlan struct {
	updates           structured.Fields
	isActive          *bool
	layoutMutation    bool
	requestedLayoutID string
	now               time.Time
}

func buildEmailTemplateUpdatePlan(
	request UpdateEmailTemplateCommand,
	now time.Time,
) (emailTemplateUpdatePlan, error) {
	plan := emailTemplateUpdatePlan{
		updates:        structured.Fields{},
		isActive:       request.IsActive,
		layoutMutation: request.LayoutID != nil,
		now:            now,
	}
	if request.Name != nil {
		if err := validateTemplateName(*request.Name); err != nil {
			return plan, domainerr.InvalidField("name", err.Error())
		}
		plan.updates["name"] = strings.TrimSpace(*request.Name)
	}
	if err := plan.addDescription(request.Description); err != nil {
		return plan, err
	}
	if request.IsActive != nil {
		plan.updates["is_active"] = *request.IsActive
	}
	plan.addLayout(request.LayoutID)
	return plan, nil
}

func (plan *emailTemplateUpdatePlan) addDescription(description *string) error {
	if description == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*description)
	if len(trimmed) > 1000 {
		return domainerr.InvalidField("description", "must be at most 1000 characters")
	}
	plan.updates["description"] = trimmed
	return nil
}

func (plan *emailTemplateUpdatePlan) addLayout(layoutID *string) {
	if layoutID == nil {
		return
	}
	plan.requestedLayoutID = strings.TrimSpace(*layoutID)
	if plan.requestedLayoutID == "" {
		plan.updates["layout_id"] = nil
		return
	}
	plan.updates["layout_id"] = plan.requestedLayoutID
}

// UpdateEmailTemplateCommand changes an Email Template's metadata and
// layout; a nil field keeps its value and an empty LayoutID clears the
// layout.
type UpdateEmailTemplateCommand struct {
	ID          string
	Name        *string
	Description *string
	IsActive    *bool
	LayoutID    *string
}

// TemplateUpdate is the Email Template state an update left.
type TemplateUpdate struct {
	ID          string
	Changed     bool
	Name        string
	Description *string
	IsActive    bool
	LayoutID    *string
	UpdatedAt   time.Time
}

// emailTemplateUpdate is the committed change of an update and the audit
// records it names.
type emailTemplateUpdate struct {
	template         Template
	changed          bool
	metadataFields   []string
	previousLayoutID string
	nextLayoutID     string
}

func (u emailTemplateUpdate) audit() mutation.Audit {
	audits := []mutation.Audit{mutation.Unchanged(), mutation.Unchanged()}
	if len(u.metadataFields) > 0 {
		audits[0] = mutation.RequestAudit(sharedtelemetry.AuditEmailTemplateUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewEmailTemplateMetadataAuditRecord(metadata, u.template.ID, u.metadataFields)
		})
	}
	if u.previousLayoutID != u.nextLayoutID {
		audits[1] = mutation.RequestAudit(sharedtelemetry.AuditEmailTemplateUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewEmailTemplateLayoutRelationAuditRecord(metadata, u.template.ID, u.previousLayoutID, u.nextLayoutID)
		})
	}
	return mutation.Audits(audits...)
}

// UpdateEmailTemplate changes the template's metadata and layout. It locks
// the observed and requested layouts before the template, so a concurrent
// layout deletion cannot strand the assignment.
func (s *Service) UpdateEmailTemplate(ctx context.Context, command UpdateEmailTemplateCommand) (TemplateUpdate, error) {
	access, err := adminAccess(policyv1.EmailTemplate.Edit(command.ID))
	if err != nil {
		return TemplateUpdate{}, err
	}
	var template Template
	var observedLayoutID string
	var lockedLayouts []string
	update, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[emailTemplateUpdate, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			observedLayoutID, lockedLayouts, err = observeAndLockEmailTemplateLayouts(ctx, store, command.ID, command.LayoutID)
			if err != nil {
				return err
			}
			template, err = lockTemplateForUpdate(ctx, store, command.ID)
			return err
		},
		Access: access,
		Change: func(ctx context.Context, store Store) (emailTemplateUpdate, error) {
			plan, err := buildEmailTemplateUpdatePlan(command, dbtime.Now())
			if err != nil {
				return emailTemplateUpdate{}, err
			}
			if err := validateEmailTemplateUpdate(ctx, store, &template, plan, observedLayoutID, lockedLayouts); err != nil {
				return emailTemplateUpdate{}, err
			}
			return applyEmailTemplateUpdate(ctx, store, template, plan)
		},
		Audit: emailTemplateUpdate.audit,
	})
	if err != nil {
		if !mutation.IsClassified(err) && errors.Is(err, ErrTemplateNotFound) {
			return TemplateUpdate{}, domainerr.ResourceNotFound("email template", command.ID)
		}
		return TemplateUpdate{}, err
	}
	template = update.template
	if template.UpdatedAt == nil {
		return TemplateUpdate{}, errors.New("email template updated_at is missing")
	}
	return TemplateUpdate{
		ID: template.ID, Changed: update.changed, Name: template.Name, Description: template.Description,
		IsActive: template.IsActive, LayoutID: template.LayoutID, UpdatedAt: *template.UpdatedAt,
	}, nil
}

// applyEmailTemplateUpdate writes the changed fields of plan to the locked
// template.
func applyEmailTemplateUpdate(ctx context.Context, store Store, template Template, plan emailTemplateUpdatePlan) (emailTemplateUpdate, error) {
	update := emailTemplateUpdate{
		metadataFields:   emailTemplateMetadataChangedFields(template, plan),
		previousLayoutID: strings.TrimSpace(ptrStringValue(template.LayoutID)),
	}
	update.nextLayoutID = update.previousLayoutID
	if plan.layoutMutation {
		update.nextLayoutID = plan.requestedLayoutID
	}
	layoutChanged := update.previousLayoutID != update.nextLayoutID
	if len(update.metadataFields) > 0 || layoutChanged {
		update.changed = true
		if err := store.UpdateTemplate(ctx, template.ID, plan.change()); err != nil {
			return emailTemplateUpdate{}, err
		}
		applyEmailTemplateUpdateFields(&template, plan)
		updatedAt := plan.now
		template.UpdatedAt = &updatedAt
	}
	update.template = template
	return update, nil
}

// change is the template change of the fields the plan still holds.
func (plan emailTemplateUpdatePlan) change() TemplateChange {
	change := TemplateChange{UpdatedAt: plan.now}
	if name, ok := plan.updates["name"].(string); ok {
		change.Name = &name
	}
	if description, ok := plan.updates["description"].(string); ok {
		change.Description = &description
	}
	if active, ok := plan.updates["is_active"].(bool); ok {
		change.IsActive = &active
	}
	if layout, exists := plan.updates["layout_id"]; exists {
		change.SetLayout = true
		if layoutID, ok := layout.(string); ok {
			change.LayoutID = &layoutID
		}
	}
	return change
}

func applyEmailTemplateUpdateFields(template *Template, plan emailTemplateUpdatePlan) {
	if name, ok := plan.updates["name"].(string); ok {
		template.Name = name
	}
	if description, ok := plan.updates["description"].(string); ok {
		template.Description = &description
	}
	if active, ok := plan.updates["is_active"].(bool); ok {
		template.IsActive = active
	}
	if layout, exists := plan.updates["layout_id"]; exists {
		if layout == nil {
			template.LayoutID = nil
		} else if layoutID, ok := layout.(string); ok {
			template.LayoutID = &layoutID
		}
	}
}

func emailTemplateMetadataChangedFields(template Template, plan emailTemplateUpdatePlan) []string {
	fields := make([]string, 0, 3)
	if value, ok := plan.updates["name"].(string); ok && value != template.Name {
		fields = append(fields, "name")
	} else {
		delete(plan.updates, "name")
	}
	if value, ok := plan.updates["description"]; ok {
		requested, _ := value.(string)
		if requested != ptrStringValue(template.Description) {
			fields = append(fields, "description")
		} else {
			delete(plan.updates, "description")
		}
	}
	if value, ok := plan.updates["is_active"].(bool); ok && value != template.IsActive {
		fields = append(fields, "active")
	} else {
		delete(plan.updates, "is_active")
	}
	if plan.layoutMutation {
		if plan.requestedLayoutID == strings.TrimSpace(ptrStringValue(template.LayoutID)) {
			delete(plan.updates, "layout_id")
		}
	}
	return fields
}

func observeAndLockEmailTemplateLayouts(
	ctx context.Context,
	store Store,
	templateID string,
	requestedLayoutID *string,
) (string, []string, error) {
	if requestedLayoutID == nil {
		return "", nil, nil
	}
	observed, err := store.TemplateLayoutID(ctx, templateID)
	if errors.Is(err, ErrTemplateNotFound) {
		return "", nil, domainerr.ResourceNotFound("email template", templateID)
	}
	if err != nil {
		return "", nil, err
	}
	observedLayoutID := strings.TrimSpace(ptrStringValue(observed))
	locked, err := lockEmailLayoutsForRelationMutation(
		ctx,
		store,
		observedLayoutID,
		strings.TrimSpace(*requestedLayoutID),
	)
	return observedLayoutID, locked, err
}

func validateEmailTemplateUpdate(
	ctx context.Context,
	store Store,
	template *Template,
	plan emailTemplateUpdatePlan,
	observedLayoutID string,
	lockedLayouts []string,
) error {
	if plan.layoutMutation && strings.TrimSpace(ptrStringValue(template.LayoutID)) != observedLayoutID {
		return domainerr.Precondition("email template layout assignment changed; retry")
	}
	if err := store.RequireTemplateMutable(ctx, template.ID); err != nil {
		return err
	}
	if plan.isActive != nil && !*plan.isActive && strings.TrimSpace(ptrStringValue(template.EventKey)) != "" {
		return domainerr.Precondition(
			"mapped email template must be explicitly unmapped before deactivation",
		)
	}
	if !plan.layoutMutation || plan.requestedLayoutID == "" {
		return nil
	}
	if !slices.Contains(lockedLayouts, plan.requestedLayoutID) {
		return domainerr.New(domainerr.NotFound, "", "email layout not found")
	}
	return nil
}
