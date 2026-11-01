package sharelinkadapter

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	sharelinkpostgres "github.com/echovisionlab/geul-api/internal/sharelink/postgres"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// ValidateForEntity verifies a link proof for the private reads of the
// domains that own link targets (sharelink.VerifyLink) on db. Every proof
// that opens nothing answers gorm.ErrRecordNotFound, as their ports expect.
func ValidateForEntity(ctx context.Context, db *gorm.DB, token, password string, entityType managev1.ShareLinkEntityType, entityID string) (*model.ShareLink, error) {
	stored, ok := sharelink.EntityTypeFromProto(entityType)
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	link, err := sharelink.VerifyLink(ctx, sharelinkpostgres.NewStore(db), token, password, stored, entityID)
	if err != nil {
		if errors.Is(err, sharelink.ErrLinkNotFound) {
			return nil, gorm.ErrRecordNotFound
		}
		return nil, err
	}
	row := sharelinkpostgres.RowOf(link)
	return &row, nil
}
