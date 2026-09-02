package emailauthoring

import (
	"context"
	"errors"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RequireTranslationSourceMutable locks the Email Authoring root before
// consulting Campaign's sealed-delivery reference authority.
func RequireTranslationSourceMutable(
	ctx context.Context,
	tx *gorm.DB,
	references CampaignDeliveryReferences,
	entityType string,
	entityID string,
) error {
	if tx == nil {
		return errs.InternalMsg("Email Authoring transaction is required")
	}
	entityType = strings.TrimSpace(entityType)
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return errs.InvalidArgument("entity_id", "Email Authoring translation source id is required")
	}
	table := ""
	switch entityType {
	case "email_template":
		table = "email_template"
	case "email_layout":
		table = "email_layout"
	default:
		return errs.InvalidArgument("entity_type", "unsupported Email Authoring translation source")
	}
	var root struct {
		ID string `gorm:"column:id"`
	}
	result := tx.WithContext(ctx).Table(table).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").Where("id = ?", entityID).Take(&root)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return errs.NotFound(entityType, entityID)
		}
		return errs.Internal(result.Error)
	}
	if entityType == "email_template" {
		return ensureEmailTemplateMutableForActiveDelivery(ctx, tx, references, entityID)
	}
	return ensureEmailLayoutMutableForActiveDelivery(ctx, tx, references, entityID)
}

func loadEmailLayoutReferenceCounts(
	ctx context.Context,
	db *gorm.DB,
	references CampaignDeliveryReferences,
	layouts []*model.EmailLayout,
) error {
	ids := make([]string, 0, len(layouts))
	byID := make(map[string]*model.EmailLayout, len(layouts))
	for _, layout := range layouts {
		if layout == nil {
			continue
		}
		layout.CampaignCount, layout.TemplateCount, layout.DeliveryRunCount = 0, 0, 0
		ids, byID[layout.ID] = append(ids, layout.ID), layout
	}
	if len(ids) == 0 {
		return nil
	}
	if references == nil {
		return errs.InternalMsg("Campaign delivery references are not configured")
	}
	external, err := references.LayoutExternalReferenceCounts(ctx, db, ids)
	if err != nil {
		return err
	}
	for id, counts := range external {
		if layout := byID[id]; layout != nil {
			layout.CampaignCount = counts.Campaigns
			layout.DeliveryRunCount = counts.DeliveryRuns
		}
	}
	var rows []struct {
		ID    string
		Count int64
	}
	if err := db.WithContext(ctx).Table("email_template").
		Select("layout_id AS id, COUNT(*) AS count").
		Where("layout_id IN ?", ids).
		Group("layout_id").
		Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if layout := byID[row.ID]; layout != nil {
			layout.TemplateCount = int32(row.Count)
		}
	}
	return nil
}

func loadEmailTemplateReferenceCounts(
	ctx context.Context,
	db *gorm.DB,
	references CampaignDeliveryReferences,
	templates []*model.EmailTemplate,
) error {
	ids := make([]string, 0, len(templates))
	byID := make(map[string]*model.EmailTemplate, len(templates))
	for _, template := range templates {
		if template == nil {
			continue
		}
		template.DeliveryRunCount = 0
		ids, byID[template.ID] = append(ids, template.ID), template
	}
	if len(ids) == 0 {
		return nil
	}
	if references == nil {
		return errs.InternalMsg("Campaign delivery references are not configured")
	}
	counts, err := references.TemplateDeliveryRunCounts(ctx, db, ids)
	if err != nil {
		return err
	}
	for id, count := range counts {
		if template := byID[id]; template != nil {
			template.DeliveryRunCount = count
		}
	}
	return nil
}

func ensureEmailTemplateMutableForActiveDelivery(
	ctx context.Context,
	tx *gorm.DB,
	references CampaignDeliveryReferences,
	templateID string,
) error {
	if references == nil {
		return errs.InternalMsg("Campaign delivery references are not configured")
	}
	return references.RequireTemplateMutable(ctx, tx, templateID)
}

func ensureEmailLayoutMutableForActiveDelivery(
	ctx context.Context,
	tx *gorm.DB,
	references CampaignDeliveryReferences,
	layoutID string,
) error {
	if references == nil {
		return errs.InternalMsg("Campaign delivery references are not configured")
	}
	return references.RequireLayoutMutable(ctx, tx, layoutID)
}
