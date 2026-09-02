package formog

import (
	"context"
	"strings"

	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

// OG adapts Form persistence to the shared Open Graph lifecycle.
type OG struct {
	cdnDomain string
	refresher *og.Refresher
}

func NewOG(cdnDomain string, refresher *og.Refresher) *OG {
	if refresher == nil {
		panic("Form OG refresher is required")
	}
	return &OG{cdnDomain: cdnDomain, refresher: refresher}
}

func (o *OG) Request(
	ctx context.Context,
	tx *gorm.DB,
	formID, _ string,
	locale string,
	allLocales bool,
	reason string,
) (string, error) {
	plan, err := o.refresher.RequestCurrentWithDB(
		ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_FORM, formID, locale, allLocales, reason,
	)
	if err != nil || plan == nil {
		return "", err
	}
	return plan.RunID, nil
}

func (o *OG) RequestAfterMutation(
	ctx context.Context,
	tx *gorm.DB,
	formID, locale string,
	allLocales bool,
	reason string,
) (*string, error) {
	plan, err := o.refresher.RequestCurrentWithDB(
		ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_FORM, formID, locale, allLocales, reason,
	)
	if err != nil || plan == nil || strings.TrimSpace(plan.RunID) == "" {
		return nil, err
	}
	return &plan.RunID, nil
}

func (o *OG) CancelAndRelease(ctx context.Context, tx *gorm.DB, formID string) error {
	if err := og.NewLifecycle(tx, o.cdnDomain).
		CancelEntityWithDB(ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_FORM, formID); err != nil {
		return err
	}
	return mediaasset.NewLifecycle(tx, o.cdnDomain).
		ReleasePublicAssetBindings(ctx, "form", formID, "og")
}

func (*OG) BaseTitle(ctx context.Context, db *gorm.DB, formID string) (string, error) {
	var row struct {
		Title string `gorm:"column:title"`
	}
	err := db.WithContext(ctx).Table("form").
		Select(formdomain.FormSourceTitleSQL("form")+" AS title").
		Where("form.id = ?", formID).
		Take(&row).Error
	return row.Title, err
}

func (o *OG) ReadyAsset(
	ctx context.Context,
	db *gorm.DB,
	localizedID, sourceID *string,
) (*commonv1.AssetRef, error) {
	return ReadyAsset(ctx, db, o.cdnDomain, localizedID, sourceID)
}

func ReadyAsset(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	localizedID, sourceID *string,
) (*commonv1.AssetRef, error) {
	for _, candidateID := range []*string{localizedID, sourceID} {
		if candidateID == nil || strings.TrimSpace(*candidateID) == "" {
			continue
		}
		var asset model.PublicAsset
		err := db.WithContext(ctx).
			Where("id = ? AND status = ?", strings.TrimSpace(*candidateID), model.PublicAssetStatusReady).
			Take(&asset).Error
		if err == gorm.ErrRecordNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		if asset.FileSize == nil || len(asset.SHA256) != 32 {
			continue
		}
		return mediaasset.NewLifecycle(db, cdnDomain).AssetRef(asset)
	}
	return nil, nil
}

var _ formdomain.OG = (*OG)(nil)
