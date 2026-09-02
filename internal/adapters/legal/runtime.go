package legal

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/campaign"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"gorm.io/gorm"
)

// Runtime adapts legal-route OG persistence to the Legal domain port.
type Runtime struct {
	cdnDomain string
	planner   *og.Planner
}

func NewRuntime() *Runtime { return &Runtime{} }

func NewOGRuntime(cdnDomain string, planner *og.Planner) *Runtime {
	return &Runtime{cdnDomain: cdnDomain, planner: planner}
}

func (*Runtime) LockActivation(ctx context.Context, tx *gorm.DB, kind string) error {
	return LockActivation(ctx, tx, kind)
}

func (*Runtime) CurrentForRoute(ctx context.Context, db *gorm.DB, kind string) (*legaldomain.CurrentRoute, error) {
	current, err := CurrentForRoute(ctx, db, kind)
	if err != nil || current == nil {
		return nil, err
	}
	return &legaldomain.CurrentRoute{ID: current.ID, Title: current.Title}, nil
}

func (r *Runtime) RequestSaved(
	ctx context.Context,
	tx *gorm.DB,
	kind, documentID, requestedLocale string,
	allLocales bool,
	reason string,
) error {
	_, err := RequestSaved(ctx, tx, r.planner, kind, documentID, requestedLocale, allLocales, reason)
	return err
}

func (*Runtime) RouteID(kind string) string { return RouteID(kind) }

func (r *Runtime) ReleaseAssets(ctx context.Context, db *gorm.DB, kind, documentID string) error {
	return ReleaseAssets(ctx, db, r.cdnDomain, kind, documentID)
}

func (r *Runtime) CancelAndRelease(
	ctx context.Context,
	tx *gorm.DB,
	kind, ownerID string,
) error {
	if err := og.NewLifecycle(tx, r.cdnDomain).
		CancelEntityWithDB(ctx, tx, og.EntityTypeForName(kind), ownerID); err != nil {
		return err
	}
	return mediaasset.NewLifecycle(tx, r.cdnDomain).
		ReleasePublicAssetBindings(ctx, kind, ownerID, "og")
}

func (*Runtime) LocalizedOGDisposition(
	ctx context.Context,
	db *gorm.DB,
	kind, routeID, locale string,
) (legaldomain.OGDisposition, error) {
	disposition, err := og.ResolveExactLocalizedGeneration(ctx, db, kind, routeID, locale)
	if err != nil {
		return legaldomain.OGUnavailable, err
	}
	switch disposition {
	case og.LocalizedGenerationPending:
		return legaldomain.OGPending, nil
	case og.LocalizedGenerationReady:
		return legaldomain.OGReady, nil
	default:
		return legaldomain.OGUnavailable, nil
	}
}

func (r *Runtime) ReadyLocalizedOGAsset(
	ctx context.Context,
	db *gorm.DB,
	kind, routeID, locale string,
) (*commonv1.AssetRef, error) {
	var binding struct {
		AssetID string `gorm:"column:asset_id"`
	}
	err := db.WithContext(ctx).Table("public_asset_binding").
		Select("asset_id").
		Where(
			"owner_type = ? AND owner_id = ? AND binding_key = ?",
			kind,
			routeID,
			"og:"+strings.TrimSpace(locale),
		).
		Take(&binding).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ref, err := mediaasset.NewLifecycle(db, r.cdnDomain).ReadyAssetRef(ctx, binding.AssetID)
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return ref, err
}

func (*Runtime) PrepareAutomaticPreviewShareLink(
	ctx context.Context,
	tx *gorm.DB,
	run model.CampaignDeliveryRun,
	now time.Time,
) error {
	return legaldomain.PrepareAutomaticNoticePreviewShareLink(ctx, tx, run, now)
}

var _ legaldomain.OG = (*Runtime)(nil)
var _ legaldomain.PublicMedia = (*Runtime)(nil)
var _ campaign.LegalNoticeDeliveryPort = (*Runtime)(nil)
