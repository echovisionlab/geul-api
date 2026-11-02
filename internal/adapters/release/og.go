package release

import (
	"context"
	"strings"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

type OG struct {
	cdnDomain string
}

func NewOG(cdnDomain string) *OG {
	return &OG{cdnDomain: strings.TrimSpace(cdnDomain)}
}

func (o *OG) CancelAndRelease(ctx context.Context, tx *gorm.DB, releaseID string) error {
	if err := ogpostgres.CancelEntity(
		ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_RELEASE, releaseID,
	); err != nil {
		return err
	}
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(tx), o.cdnDomain).
		ReleasePublicAssetBindings(ctx, "release", releaseID, "og")
}

type ContentOG struct {
	refresher *ogpostgres.Refresher
}

func NewContentOG(refresher *ogpostgres.Refresher) *ContentOG {
	if refresher == nil {
		panic("Release OG refresher is required")
	}
	return &ContentOG{refresher: refresher}
}

func (o *ContentOG) RequestCurrent(
	ctx context.Context,
	tx *gorm.DB,
	releaseID, reason string,
) error {
	_, err := o.refresher.RequestCurrentWithDB(
		ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_RELEASE,
		releaseID, "", false, reason,
	)
	return err
}

var _ releasepostgres.OG = (*OG)(nil)
var _ releasepostgres.ContentOG = (*ContentOG)(nil)
