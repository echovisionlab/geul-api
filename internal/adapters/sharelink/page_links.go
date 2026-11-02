package sharelinkadapter

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/sharelink"
	sharelinkpostgres "github.com/echovisionlab/geul-api/internal/sharelink/postgres"
)

// legacyPageEntityType is the entity type Page links were once stored under.
const legacyPageEntityType sharelink.EntityType = "page"

// PageLinks removes the share links of a deleted Page in the deleting
// transaction.
type PageLinks struct{}

// DeletePageShareLinks deletes every share link of the Page, including links
// stored under the legacy "page" entity type.
func (PageLinks) DeletePageShareLinks(ctx context.Context, tx *gorm.DB, pageID string) error {
	return sharelinkpostgres.NewStore(tx).DeleteTargetLinks(ctx, pageID, legacyPageEntityType, sharelink.EntityPage)
}
