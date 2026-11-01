package legal

import (
	"context"
	"errors"
	"strings"
	"time"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	campaignpostgres "github.com/echovisionlab/geul-api/internal/campaign/postgres"

	mediaassetadapter "github.com/echovisionlab/geul-api/internal/adapters/mediaasset"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/campaign"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	legalpostgres "github.com/echovisionlab/geul-api/internal/legal/postgres"
	legalpublic "github.com/echovisionlab/geul-api/internal/legal/public"
	legalpublicpostgres "github.com/echovisionlab/geul-api/internal/legal/public/postgres"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/og"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"gorm.io/gorm"
)

// Runtime adapts legal-route OG persistence to the Legal domain port.
type Runtime struct {
	cdnDomain string
	planner   *ogpostgres.Planner
}

func NewRuntime() *Runtime { return &Runtime{} }

func NewOGRuntime(cdnDomain string, planner *ogpostgres.Planner) *Runtime {
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

func (r *Runtime) CancelAndRelease(
	ctx context.Context,
	tx *gorm.DB,
	kind, ownerID string,
) error {
	if err := ogpostgres.CancelEntity(ctx, tx, og.EntityTypeForName(kind), ownerID); err != nil {
		return err
	}
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(tx), r.cdnDomain).
		ReleasePublicAssetBindings(ctx, kind, ownerID, "og")
}

func (*Runtime) LocalizedOGDisposition(
	ctx context.Context,
	db *gorm.DB,
	kind, routeID, locale string,
) (legalpublic.OGDisposition, error) {
	disposition, err := ogpostgres.ResolveExactLocalizedGeneration(ctx, db, kind, routeID, locale)
	if err != nil {
		return legalpublic.OGUnavailable, err
	}
	switch disposition {
	case og.LocalizedGenerationPending:
		return legalpublic.OGPending, nil
	case og.LocalizedGenerationReady:
		return legalpublic.OGReady, nil
	default:
		return legalpublic.OGUnavailable, nil
	}
}

func (r *Runtime) ReadyLocalizedOGAsset(
	ctx context.Context,
	db *gorm.DB,
	kind, routeID, locale string,
) (*commonv1.AssetRef, error) {
	assetID, bound, err := mediaassetadapter.BoundPublicAssetID(ctx, db, kind, routeID, "og_"+strings.TrimSpace(locale))
	if err != nil || !bound {
		return nil, err
	}
	ref, err := mediaasset.ReferenceResultProto(mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), r.cdnDomain).ReadyAssetRef(ctx, assetID))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return ref, err
}

// PrepareAutomaticPreviewShareLink materializes the preview link of a
// sealed update notice in Campaign's dispatch transaction.
func (*Runtime) PrepareAutomaticPreviewShareLink(
	ctx context.Context,
	tx *gorm.DB,
	run campaign.DeliveryRun,
	now time.Time,
) error {
	links := legalpostgres.Bind(tx, legalpostgres.Ports{PreviewLinks: sharelinkadapter.LegalPreviewLinks{}}, nil)
	return legaldomain.PrepareAutomaticNoticePreviewShareLink(ctx, links.Store, legalpostgres.NoticeRun(campaignpostgres.DeliveryRunToRow(run)), now)
}

// OGAssets releases, on db, the OG images of a deleted version.
type OGAssets struct {
	db        *gorm.DB
	cdnDomain string
}

// NewOGAssets returns the OG image release of deleted versions over db.
func NewOGAssets(db *gorm.DB, cdnDomain string) OGAssets {
	return OGAssets{db: db, cdnDomain: cdnDomain}
}

func (a OGAssets) ReleaseOGAssets(ctx context.Context, kind legaldomain.PolicyKind, id string) error {
	return ReleaseAssets(ctx, a.db, a.cdnDomain, string(kind), id)
}

var _ legalpostgres.Routes = (*Runtime)(nil)
var _ legalpublicpostgres.Media = (*Runtime)(nil)
var _ campaignpostgres.LegalNoticeDeliveryPort = (*Runtime)(nil)
var _ legaldomain.OGAssets = OGAssets{}
