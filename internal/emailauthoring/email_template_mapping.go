package emailauthoring

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// EventMapping is the template an automatic email event is mapped to.
type EventMapping struct {
	Event        string
	TemplateID   string
	TemplateName string
}

// GetEventMappings returns each automatic email event's mapped template,
// ordered by event.
func (s *Service) GetEventMappings(ctx context.Context) ([]EventMapping, error) {
	can, canErr := policyv1.EmailTemplate.List()
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return nil, err
	}
	var templates []Template
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		templates, err = store.MappedTemplates(ctx)
		return err
	}); err != nil {
		return nil, err
	}
	mappings := make([]EventMapping, 0, len(templates))
	for _, t := range templates {
		if t.EventKey == nil {
			continue
		}
		mappings = append(mappings, EventMapping{Event: *t.EventKey, TemplateID: t.ID, TemplateName: t.Name})
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
	var lockedTemplates []Template
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[eventMappingChange, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			lockedTemplates, err = store.LockEventMappingTemplates(ctx, eventKey, targetTemplateID)
			return err
		},
		Access: mutation.FreshAdminCan(policyv1.EmailEventMapping.Update()),
		Change: func(ctx context.Context, store Store) (eventMappingChange, error) {
			return updateEmailEventMapping(ctx, store, lockedTemplates, eventKey, targetTemplateID, time.Now().UTC())
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

func updateEmailEventMapping(
	ctx context.Context,
	store Store,
	lockedTemplates []Template,
	eventKey string,
	targetTemplateID string,
	now time.Time,
) (eventMappingChange, error) {
	if err := validateEmailTemplateEventKey(eventKey); err != nil {
		return eventMappingChange{}, domainerr.InvalidField("event", err.Error())
	}
	targetTemplate, err := validateEmailEventMappingTemplates(ctx, store, lockedTemplates, eventKey, targetTemplateID)
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
	if err := store.ClearEventMapping(ctx, eventKey, now); err != nil {
		return eventMappingChange{}, err
	}
	if targetTemplate == nil {
		return change, nil
	}
	return change, store.SetEventMapping(ctx, targetTemplate.ID, eventKey, now)
}

func trimmedEmailTemplateID(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func validateEmailEventMappingTemplates(
	ctx context.Context,
	store Store,
	templates []Template,
	eventKey string,
	targetTemplateID string,
) (*Template, error) {
	var target *Template
	for index := range templates {
		template := &templates[index]
		isCurrentMapping := template.EventKey != nil && *template.EventKey == eventKey
		if isCurrentMapping || template.ID == targetTemplateID {
			if err := store.RequireTemplateMutable(ctx, template.ID); err != nil {
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
