package emailauthoring

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
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
	entityType = strings.TrimSpace(entityType)
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return domainerr.InvalidField("entity_id", "Email Authoring translation source id is required")
	}
	var table string
	switch entityType {
	case "email_template":
		table = "email_template"
	case "email_layout":
		table = "email_layout"
	default:
		return domainerr.InvalidField("entity_type", "unsupported Email Authoring translation source")
	}
	var root struct {
		ID string `gorm:"column:id"`
	}
	result := tx.WithContext(ctx).Table(table).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id").Where("id = ?", entityID).Take(&root)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound(entityType, entityID)
		}
		return result.Error
	}
	if entityType == "email_template" {
		return references.RequireTemplateMutable(ctx, tx, entityID)
	}
	return references.RequireLayoutMutable(ctx, tx, entityID)
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
