package emailauthoring

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// RequireTranslationSourceMutable locks the Email Authoring root before
// consulting Campaign's sealed-delivery reference authority.
func RequireTranslationSourceMutable(
	ctx context.Context,
	store Store,
	entityType string,
	entityID string,
) error {
	entityType = strings.TrimSpace(entityType)
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return domainerr.InvalidField("entity_id", "Email Authoring translation source id is required")
	}
	if entityType != "email_template" && entityType != "email_layout" {
		return domainerr.InvalidField("entity_type", "unsupported Email Authoring translation source")
	}
	if err := store.LockTranslationSource(ctx, entityType, entityID); err != nil {
		if errors.Is(err, ErrTemplateNotFound) || errors.Is(err, ErrLayoutNotFound) {
			return domainerr.ResourceNotFound(entityType, entityID)
		}
		return err
	}
	if entityType == "email_template" {
		return store.RequireTemplateMutable(ctx, entityID)
	}
	return store.RequireLayoutMutable(ctx, entityID)
}

// loadLayoutReferenceCounts sets each layout's campaign, template and
// delivery run counts.
func loadLayoutReferenceCounts(ctx context.Context, store Store, layouts []Layout) error {
	ids := make([]string, 0, len(layouts))
	for index := range layouts {
		layouts[index].CampaignCount, layouts[index].TemplateCount, layouts[index].DeliveryRunCount = 0, 0, 0
		ids = append(ids, layouts[index].ID)
	}
	if len(ids) == 0 {
		return nil
	}
	external, err := store.LayoutExternalReferenceCounts(ctx, ids)
	if err != nil {
		return err
	}
	templates, err := store.TemplateCounts(ctx, ids)
	if err != nil {
		return err
	}
	for index := range layouts {
		counts := external[layouts[index].ID]
		layouts[index].CampaignCount = counts.Campaigns
		layouts[index].DeliveryRunCount = counts.DeliveryRuns
		layouts[index].TemplateCount = templates[layouts[index].ID]
	}
	return nil
}
