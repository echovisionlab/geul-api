package emailauthoring

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// GetEventMappings returns each automatic email event's mapped template,
// ordered by event.
func (s *Service) GetEventMappings(ctx context.Context) ([]*managev1.EmailEventMapping, error) {
	can, canErr := policyv1.EmailTemplate.List()
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return nil, err
	}
	var templates []model.EmailTemplate
	if err := s.db.WithContext(ctx).
		Where("event_key IS NOT NULL").
		Order("event_key ASC").
		Find(&templates).Error; err != nil {
		return nil, err
	}
	mappings := make([]*managev1.EmailEventMapping, 0, len(templates))
	for _, t := range templates {
		if t.EventKey == nil {
			continue
		}
		mappings = append(mappings, &managev1.EmailEventMapping{
			Event:        *t.EventKey,
			TemplateId:   &t.ID,
			TemplateName: &t.Name,
		})
	}
	return mappings, nil
}

// UpdateEventMappingCommand maps an automatic email event to a template; a
// nil or empty TemplateID unmaps it.
type UpdateEventMappingCommand struct {
	Event      string
	TemplateID *string
}

// eventMappingChange is the template an event was mapped to before and
// after an update.
type eventMappingChange struct {
	event    string
	previous string
	next     string
}

// UpdateEventMapping maps the event to the template, or unmaps it. Both the
// current and the requested template must be free of active deliveries.
func (s *Service) UpdateEventMapping(ctx context.Context, command UpdateEventMappingCommand) error {
	eventKey := strings.TrimSpace(command.Event)
	targetTemplateID := trimmedEmailTemplateID(command.TemplateID)
	var lockedTemplates []model.EmailTemplate
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[eventMappingChange]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			var err error
			lockedTemplates, err = lockEmailEventMappingTemplates(ctx, tx, eventKey, targetTemplateID)
			return err
		},
		Access: mutation.FreshAdminCan(policyv1.EmailEventMapping.Update()),
		Change: func(ctx context.Context, tx *gorm.DB) (eventMappingChange, error) {
			return s.updateEmailEventMapping(ctx, tx, lockedTemplates, eventKey, targetTemplateID, time.Now().UTC())
		},
		Audit: func(change eventMappingChange) mutation.Audit {
			if change.previous == change.next {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditEmailEventMappingUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewEmailEventMappingTemplateAuditRecord(metadata, change.event, change.previous, change.next)
			})
		},
	})
	return err
}

func (s *Service) updateEmailEventMapping(
	ctx context.Context,
	tx *gorm.DB,
	lockedTemplates []model.EmailTemplate,
	eventKey string,
	targetTemplateID string,
	now time.Time,
) (eventMappingChange, error) {
	if err := validateEmailTemplateEventKey(eventKey); err != nil {
		return eventMappingChange{}, domainerr.InvalidField("event", err.Error())
	}
	targetTemplate, err := validateEmailEventMappingTemplates(ctx, tx, s.references, lockedTemplates, eventKey, targetTemplateID)
	if err != nil {
		return eventMappingChange{}, err
	}
	change := eventMappingChange{event: eventKey, next: targetTemplateID}
	for index := range lockedTemplates {
		template := &lockedTemplates[index]
		if template.EventKey != nil && *template.EventKey == eventKey {
			change.previous = template.ID
			break
		}
	}
	if change.previous == targetTemplateID {
		return change, nil
	}
	if err := clearEmailEventMapping(tx, eventKey, now); err != nil {
		return eventMappingChange{}, err
	}
	if targetTemplate == nil {
		return change, nil
	}
	return change, setEmailEventMapping(tx, targetTemplate.ID, eventKey, now)
}

func trimmedEmailTemplateID(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func lockEmailEventMappingTemplates(
	ctx context.Context,
	tx *gorm.DB,
	eventKey string,
	targetTemplateID string,
) ([]model.EmailTemplate, error) {
	var templates []model.EmailTemplate
	query := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("event_key = ?", eventKey)
	if targetTemplateID != "" {
		query = query.Or("id = ?", targetTemplateID)
	}
	err := query.Order("id ASC").Find(&templates).Error
	return templates, err
}

func validateEmailEventMappingTemplates(
	ctx context.Context,
	tx *gorm.DB,
	references CampaignDeliveryReferences,
	templates []model.EmailTemplate,
	eventKey string,
	targetTemplateID string,
) (*model.EmailTemplate, error) {
	var target *model.EmailTemplate
	for index := range templates {
		template := &templates[index]
		isCurrentMapping := template.EventKey != nil && *template.EventKey == eventKey
		if isCurrentMapping || template.ID == targetTemplateID {
			if err := references.RequireTemplateMutable(ctx, tx, template.ID); err != nil {
				return nil, err
			}
		}
		if template.ID == targetTemplateID {
			target = template
		}
	}
	if targetTemplateID == "" {
		return nil, nil
	}
	if target == nil {
		return nil, domainerr.ResourceNotFound("email template", targetTemplateID)
	}
	if !target.IsActive {
		return nil, domainerr.Precondition("email template must be active before it can be mapped")
	}
	return target, nil
}

func clearEmailEventMapping(tx *gorm.DB, eventKey string, now time.Time) error {
	return tx.Model(&model.EmailTemplate{}).
		Where("event_key = ?", eventKey).
		Updates(structured.Fields{"event_key": nil, "updated_at": now}).Error
}

func setEmailEventMapping(tx *gorm.DB, templateID, eventKey string, now time.Time) error {
	return tx.Model(&model.EmailTemplate{}).
		Where("id = ?", templateID).
		Updates(structured.Fields{"event_key": eventKey, "updated_at": now}).Error
}
