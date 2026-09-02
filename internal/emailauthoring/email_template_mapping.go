package emailauthoring

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// templateKeyRegex validates the format of template keys
func (s *EmailTemplateService) GetEventMappings(
	ctx context.Context,
	req *connect.Request[managev1.GetEventMappingsRequest],
) (*connect.Response[managev1.GetEventMappingsResponse], error) {
	can, canErr := policyv1.EmailTemplate.List()
	if _, err := s.requireEmailTemplateCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	// Query templates that have an event_key assigned
	var templates []model.EmailTemplate
	if err := s.db.WithContext(ctx).
		Where("event_key IS NOT NULL").
		Order("event_key ASC").
		Find(&templates).Error; err != nil {
		return nil, errs.Internal(err)
	}

	mappings := make([]*managev1.EmailEventMapping, 0, len(templates))
	for _, t := range templates {
		// Safety check: skip if EventKey is nil (shouldn't happen due to WHERE clause, but defensive)
		if t.EventKey == nil {
			continue
		}
		mappings = append(mappings, &managev1.EmailEventMapping{
			Event:        *t.EventKey,
			TemplateId:   &t.ID,
			TemplateName: &t.Name,
		})
	}

	return connect.NewResponse(&managev1.GetEventMappingsResponse{
		Mappings: mappings,
	}), nil
}

// UpdateEventMapping updates an event-to-template mapping (admin)
func (s *EmailTemplateService) UpdateEventMapping(
	ctx context.Context,
	req *connect.Request[managev1.UpdateEventMappingRequest],
) (*connect.Response[managev1.SuccessResponse], error) {
	mappingCan, err := policyv1.EmailEventMapping.Update()
	if err != nil {
		return nil, errs.Internal(err)
	}

	eventKey := strings.TrimSpace(req.Msg.Event)
	targetTemplateID := trimmedEmailTemplateID(req.Msg.TemplateId)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		previousTemplateID, changed, err := s.updateEmailEventMappingWithDB(ctx, tx, eventKey, targetTemplateID, time.Now().UTC(), mappingCan)
		if err != nil || !changed {
			return err
		}
		return appendEmailEventMappingAudit(ctx, tx, s.auditWriter, eventKey, previousTemplateID, targetTemplateID)
	})

	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(&managev1.SuccessResponse{Success: true}), nil
}

func (s *EmailTemplateService) updateEmailEventMappingWithDB(
	ctx context.Context,
	tx *gorm.DB,
	eventKey string,
	targetTemplateID string,
	now time.Time,
	mappingCan policyv1.Can,
) (string, bool, error) {
	lockedTemplates, err := lockEmailEventMappingTemplates(ctx, tx, eventKey, targetTemplateID)
	if err != nil {
		return "", false, err
	}
	if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, mappingCan); err != nil {
		return "", false, err
	}
	if err := validateEmailTemplateEventKey(eventKey); err != nil {
		return "", false, errs.InvalidArgument("event", err.Error())
	}
	targetTemplate, err := validateEmailEventMappingTemplates(ctx, tx, s.references, lockedTemplates, eventKey, targetTemplateID)
	if err != nil {
		return "", false, err
	}
	previousTemplateID := ""
	for index := range lockedTemplates {
		template := &lockedTemplates[index]
		if template.EventKey != nil && *template.EventKey == eventKey {
			previousTemplateID = template.ID
			break
		}
	}
	if previousTemplateID == targetTemplateID {
		return previousTemplateID, false, nil
	}
	if err := clearEmailEventMapping(tx, eventKey, now); err != nil {
		return "", false, err
	}
	if targetTemplate == nil {
		return previousTemplateID, true, nil
	}
	if err := setEmailEventMapping(tx, targetTemplate.ID, eventKey, now); err != nil {
		return "", false, err
	}
	return previousTemplateID, true, nil
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
		Clauses(clause.Locking{Strength: "UPDATE"}).
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
			if err := ensureEmailTemplateMutableForActiveDelivery(ctx, tx, references, template.ID); err != nil {
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
		return nil, errs.NotFound("email template", targetTemplateID)
	}
	if !target.IsActive {
		return nil, errs.FailedPrecondition("email template must be active before it can be mapped")
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

// validateEmail validates an email address using net/mail.
