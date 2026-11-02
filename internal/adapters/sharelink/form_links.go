package sharelinkadapter

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	sharelinkpostgres "github.com/echovisionlab/geul-api/internal/sharelink/postgres"
)

// FormLinks removes the share links of a deleted Form in the deleting
// transaction.
type FormLinks struct{}

// DeleteFormShareLinks deletes the Form's submission and dashboard share
// links.
func (FormLinks) DeleteFormShareLinks(ctx context.Context, tx *gorm.DB, formID string) error {
	return sharelinkpostgres.NewStore(tx).DeleteTargetLinks(ctx, formID, sharelink.EntityForm, sharelink.EntityFormDashboard)
}

// FindShareLink returns the share link of token, or gorm.ErrRecordNotFound.
func (FormLinks) FindShareLink(ctx context.Context, db *gorm.DB, token string) (*model.ShareLink, error) {
	link, err := sharelinkpostgres.NewStore(db).LinkByToken(ctx, token)
	if err != nil {
		if errors.Is(err, sharelink.ErrLinkNotFound) {
			return nil, gorm.ErrRecordNotFound
		}
		return nil, err
	}
	row := sharelinkpostgres.RowOf(link)
	return &row, nil
}
