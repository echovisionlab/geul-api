package sharelinkadapter

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sharelink"
)

// EntityLinks removes the share links of one entity type's deleted
// resource, in the deleting domain's transaction.
type EntityLinks struct {
	Type sharelink.EntityType
}

func (l EntityLinks) DeleteEntityShareLinks(ctx context.Context, tx *gorm.DB, entityID string) error {
	return tx.WithContext(ctx).
		Where("entity_type = ? AND entity_id = ?", string(l.Type), entityID).
		Delete(&model.ShareLink{}).Error
}
