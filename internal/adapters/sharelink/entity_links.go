package sharelinkadapter

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/sharelink"
	sharelinkpostgres "github.com/echovisionlab/geul-api/internal/sharelink/postgres"
)

// EntityLinks removes the share links of one entity type's deleted
// resource, in the deleting domain's transaction.
type EntityLinks struct {
	Type sharelink.EntityType
}

func (l EntityLinks) DeleteEntityShareLinks(ctx context.Context, tx *gorm.DB, entityID string) error {
	return sharelinkpostgres.NewStore(tx).DeleteTargetLinks(ctx, entityID, l.Type)
}
