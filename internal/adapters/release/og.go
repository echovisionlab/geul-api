package release

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/og"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
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
	if err := og.NewLifecycle(tx, o.cdnDomain).CancelEntityWithDB(
		ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_RELEASE, releaseID,
	); err != nil {
		return err
	}
	return mediaasset.NewLifecycle(tx, o.cdnDomain).
		ReleasePublicAssetBindings(ctx, "release", releaseID, "og")
}

func (*OG) LocaleAware() bool {
	return og.SupportsLocaleAware("release")
}

type ContentOG struct {
	refresher *og.Refresher
}

func NewContentOG(refresher *og.Refresher) *ContentOG {
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

var _ releasedomain.OG = (*OG)(nil)
var _ releasedomain.ContentOG = (*ContentOG)(nil)
