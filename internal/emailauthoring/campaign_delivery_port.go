package emailauthoring

import (
	"context"

	"gorm.io/gorm"
)

// LayoutExternalReferenceCounts contains references owned outside Email
// Authoring. Template-to-layout references remain owned and counted locally.
type LayoutExternalReferenceCounts struct {
	Campaigns    int32
	DeliveryRuns int32
}

// CampaignDeliveryReferences is the consumer-owned boundary for Campaign and
// delivery history that can freeze or retain an Email Authoring resource.
type CampaignDeliveryReferences interface {
	TemplateDeliveryRunCounts(context.Context, *gorm.DB, []string) (map[string]int32, error)
	LayoutExternalReferenceCounts(context.Context, *gorm.DB, []string) (map[string]LayoutExternalReferenceCounts, error)
	RequireTemplateMutable(context.Context, *gorm.DB, string) error
	RequireLayoutMutable(context.Context, *gorm.DB, string) error
	DetachTemplateHistory(context.Context, *gorm.DB, string) error
	DetachLayoutHistory(context.Context, *gorm.DB, string) error
}
