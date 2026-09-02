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

type emailTemplateUpdatePlan struct {
	updates           structured.Fields
	isActive          *bool
	layoutMutation    bool
	requestedLayoutID string
	now               time.Time
}

func buildEmailTemplateUpdatePlan(
	request *managev1.UpdateEmailTemplateRequest,
	now time.Time,
) (emailTemplateUpdatePlan, error) {
	plan := emailTemplateUpdatePlan{
		updates:        structured.Fields{},
		isActive:       request.IsActive,
		layoutMutation: request.LayoutId != nil,
		now:            now,
	}
	if request.Name != nil {
		if err := validateTemplateName(*request.Name); err != nil {
			return plan, errs.InvalidArgument("name", err.Error())
		}
		plan.updates["name"] = strings.TrimSpace(*request.Name)
	}
	if err := plan.addDescription(request.Description); err != nil {
		return plan, err
	}
	if request.IsActive != nil {
		plan.updates["is_active"] = *request.IsActive
	}
	plan.addLayout(request.LayoutId)
	return plan, nil
}

func (plan *emailTemplateUpdatePlan) addDescription(description *string) error {
	if description == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*description)
	if len(trimmed) > 1000 {
		return errs.InvalidArgument("description", "must be at most 1000 characters")
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

func (s *EmailTemplateService) applyEmailTemplateUpdate(
	ctx context.Context,
	request *managev1.UpdateEmailTemplateRequest,
	templateCan policyv1.Can,
) (*model.EmailTemplate, bool, error) {
	var template model.EmailTemplate
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		observedLayoutID, lockedLayouts, err := observeAndLockEmailTemplateLayouts(
			ctx,
			tx,
			request.Id,
			request.LayoutId,
		)
		if err != nil {
			return err
		}
		if err := lockEmailTemplateForUpdate(ctx, tx, request.Id, &template); err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, templateCan); err != nil {
			return err
		}
		plan, err := buildEmailTemplateUpdatePlan(request, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := validateEmailTemplateUpdate(ctx, tx, s.references, &template, plan, observedLayoutID, lockedLayouts); err != nil {
			return err
		}
		metadataFields := emailTemplateMetadataChangedFields(template, plan)
		previousLayoutID := strings.TrimSpace(ptrStringValue(template.LayoutID))
		layoutChanged := plan.layoutMutation && previousLayoutID != plan.requestedLayoutID
		if len(metadataFields) > 0 || layoutChanged {
			changed = true
			plan.updates["updated_at"] = plan.now
			if err := tx.Model(&template).Updates(plan.updates).Error; err != nil {
				return err
			}
			applyEmailTemplateUpdateFields(&template, plan)
			updatedAt := plan.now
			template.UpdatedAt = &updatedAt
		}
		if err := appendEmailTemplateMetadataAudit(ctx, tx, s.auditWriter, template.ID, metadataFields); err != nil {
			return err
		}
		if err := appendEmailTemplateLayoutAudit(ctx, tx, s.auditWriter, template.ID, previousLayoutID, plan.requestedLayoutID); err != nil {
			return err
		}
		return nil
	})
	return &template, changed, err
}

func applyEmailTemplateUpdateFields(template *model.EmailTemplate, plan emailTemplateUpdatePlan) {
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

func emailTemplateMetadataChangedFields(template model.EmailTemplate, plan emailTemplateUpdatePlan) []string {
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
	tx *gorm.DB,
	templateID string,
	requestedLayoutID *string,
) (string, map[string]model.EmailLayout, error) {
	if requestedLayoutID == nil {
		return "", nil, nil
	}
	var observed struct {
		LayoutID *string `gorm:"column:layout_id"`
	}
	if err := tx.WithContext(ctx).
		Model(&model.EmailTemplate{}).
		Select("layout_id").
		First(&observed, "id = ?", templateID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", nil, errs.NotFound("email template", templateID)
		}
		return "", nil, err
	}
	observedLayoutID := strings.TrimSpace(ptrStringValue(observed.LayoutID))
	locked, err := lockEmailLayoutsForRelationMutation(
		ctx,
		tx,
		observedLayoutID,
		strings.TrimSpace(*requestedLayoutID),
	)
	return observedLayoutID, locked, err
}

func lockEmailTemplateForUpdate(
	ctx context.Context,
	tx *gorm.DB,
	templateID string,
	template *model.EmailTemplate,
) error {
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(template, "id = ?", templateID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.NotFound("email template", templateID)
		}
		return err
	}
	return nil
}

func validateEmailTemplateUpdate(
	ctx context.Context,
	tx *gorm.DB,
	references CampaignDeliveryReferences,
	template *model.EmailTemplate,
	plan emailTemplateUpdatePlan,
	observedLayoutID string,
	lockedLayouts map[string]model.EmailLayout,
) error {
	if plan.layoutMutation && strings.TrimSpace(ptrStringValue(template.LayoutID)) != observedLayoutID {
		return errs.FailedPrecondition("email template layout assignment changed; retry")
	}
	if err := ensureEmailTemplateMutableForActiveDelivery(ctx, tx, references, template.ID); err != nil {
		return err
	}
	if plan.isActive != nil && !*plan.isActive && strings.TrimSpace(ptrStringValue(template.EventKey)) != "" {
		return errs.FailedPrecondition(
			"mapped email template must be explicitly unmapped before deactivation",
		)
	}
	if !plan.layoutMutation || plan.requestedLayoutID == "" {
		return nil
	}
	if _, ok := lockedLayouts[plan.requestedLayoutID]; !ok {
		return errs.NotFoundMsg("email layout not found")
	}
	return nil
}

func mapEmailTemplateUpdateError(err error, templateID string) error {
	if connectErr, ok := err.(*connect.Error); ok {
		return connectErr
	}
	if err == gorm.ErrRecordNotFound {
		return errs.NotFound("email template", templateID)
	}
	return errs.Internal(err)
}
