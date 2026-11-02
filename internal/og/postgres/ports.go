package ogpostgres

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// RenderConfig reads the canonical shared render configuration under the
// planner's target-lock transaction boundary.
type RenderConfig interface {
	Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error)
}

// Projection owns an entity family's current-pointer and public-binding
// transitions. It receives only the durable target identity and asset ID.
type Projection interface {
	Handles(og.Target) bool
	ReleasePending(context.Context, *gorm.DB, og.Target, string) error
	Complete(context.Context, *gorm.DB, og.Target, string, time.Time, string) error
}

// RequestSource owns canonical request snapshots for one entity family.
// It receives the decoded public selection but not lifecycle state.
type RequestSource interface {
	Handles(string) bool
	Resolve(context.Context, *gorm.DB, string, string, *managev1.OgTargetSelection) ([]og.Request, error)
}

// AllRequestSource contributes current requests for global regeneration.
type AllRequestSource interface {
	All(context.Context, *gorm.DB) ([]og.Request, error)
}

// GlobalReconciler lets owning domains repair their own legacy OG source state
// before a global collection without exposing their tables to OG.
type GlobalReconciler interface {
	ReconcileBeforeGlobalGeneration(context.Context, *gorm.DB, string) error
}

// NoopGlobalReconciler is the explicit global-generation policy for Geul's
// active OG projections. There are no removed music-domain projections to
// reconcile before collecting the current set of targets.
type NoopGlobalReconciler struct{}

func (NoopGlobalReconciler) ReconcileBeforeGlobalGeneration(context.Context, *gorm.DB, string) error {
	return nil
}

// AssetReference reports whether one entity family still points at an OG
// asset. The family's owner answers for its own rows.
type AssetReference interface {
	ReferencesAsset(ctx context.Context, tx *gorm.DB, assetID string) (bool, error)
}

// ReloadRequests rereads canonical target state only after every target lock.
type ReloadRequests func(context.Context, *gorm.DB) ([]og.Request, error)

// Resolver routes one generation request to its owning entity source.
type Resolver struct {
	sources []RequestSource
}

func NewResolver(sources ...RequestSource) *Resolver {
	if len(sources) == 0 {
		panic("OG request sources are required")
	}
	return &Resolver{sources: sources}
}

func (r *Resolver) resolve(ctx context.Context, db *gorm.DB, entityType, entityID string, selection og.Selection) ([]og.Request, error) {
	for _, source := range r.sources {
		if source != nil && source.Handles(entityType) {
			requests, err := source.Resolve(ctx, db, entityType, entityID, og.SelectionToProto(selection))
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, og.ErrEntityNotFound
			}
			return requests, err
		}
	}
	return nil, domainerr.InvalidEntityType(entityType)
}

// Collector combines independently owned entity snapshots for global work.
type Collector struct {
	sources []AllRequestSource
}

func NewCollector(sources ...AllRequestSource) *Collector {
	if len(sources) == 0 {
		panic("OG all-request sources are required")
	}
	return &Collector{sources: sources}
}

// Collect returns every source's current requests, once per target.
func (c *Collector) Collect(ctx context.Context, db *gorm.DB) ([]og.Request, error) {
	return og.CollectRequests(ctx, NewStore(db, Ports{AllRequests: c}))
}

func (c *Collector) collect(ctx context.Context, db *gorm.DB) ([]og.Request, error) {
	requests := make([]og.Request, 0)
	for _, source := range c.sources {
		if source == nil {
			continue
		}
		current, err := source.All(ctx, db)
		if err != nil {
			return nil, err
		}
		requests = append(requests, current...)
	}
	return requests, nil
}

// Planner plans OG runs in the transaction of the mutation that changed
// their entities.
type Planner struct {
	ports Ports
}

func NewPlanner(cdnDomain string, config RenderConfig, projections ...Projection) *Planner {
	if config == nil || len(projections) == 0 {
		panic("OG generation planner dependencies are required")
	}
	return &Planner{ports: Ports{CDNDomain: cdnDomain, RenderConfig: config, Projections: projections}}
}

// RequestBulkReloadedWithDB plans requests in tx; reload rereads them once
// every target is locked.
func (p *Planner) RequestBulkReloadedWithDB(
	ctx context.Context,
	tx *gorm.DB,
	triggerKind string,
	reason string,
	requests []og.Request,
	reload ReloadRequests,
) (*og.Plan, error) {
	if reload == nil {
		return nil, domainerr.InvalidField("reload", "canonical OG request reloader is required")
	}
	if tx == nil {
		return nil, domainerr.InvalidField("transaction", "database transaction is required")
	}
	return og.RequestBulk(ctx, NewStore(tx, p.ports), dbtime.Now(), triggerKind, reason, requests,
		func(ctx context.Context, _ og.Store) ([]og.Request, error) { return reload(ctx, tx) })
}

// Refresher requests canonical OG work after an owning entity mutation.
type Refresher struct {
	ports Ports
}

func NewRefresher(planner *Planner, resolver *Resolver) *Refresher {
	if planner == nil || resolver == nil {
		panic("OG refresh dependencies are required")
	}
	ports := planner.ports
	ports.Requests = resolver
	return &Refresher{ports: ports}
}

// RequestCurrentWithDB plans the selected targets of one entity in tx: one
// locale, every locale, or the primary target.
func (r *Refresher) RequestCurrentWithDB(
	ctx context.Context,
	tx *gorm.DB,
	entityType managev1.OgEntityType,
	entityID string,
	locale string,
	allLocales bool,
	reason string,
) (*og.Plan, error) {
	name, err := og.EntityTypeName(entityType)
	if err != nil {
		return nil, err
	}
	return og.RequestCurrent(ctx, NewStore(tx, r.ports), dbtime.Now(), name, entityID, og.RefreshSelection(locale, allLocales), reason)
}

// CancelEntity cancels every active generation of an entity in tx, the
// transaction of the mutation that removed or unpublished it.
func CancelEntity(ctx context.Context, tx *gorm.DB, entityType managev1.OgEntityType, entityID string) error {
	name, err := og.EntityTypeName(entityType)
	if err != nil {
		return err
	}
	return og.CancelEntity(ctx, NewStore(tx, Ports{}), dbtime.Now(), name, entityID)
}

// AssetProtector keeps an OG asset an entity row still points at from public
// asset cleanup.
type AssetProtector struct {
	db         *gorm.DB
	references []AssetReference
}

func NewAssetProtector(db *gorm.DB, references ...AssetReference) *AssetProtector {
	return &AssetProtector{db: db, references: references}
}

func (p *AssetProtector) IsPublicAssetProtected(ctx context.Context, asset mediaasset.Asset) (bool, error) {
	return og.AssetProtected(ctx, NewStore(p.db.WithContext(ctx), Ports{References: p.references}), assetFromModel(mediaassetpostgres.AssetRow(asset)))
}

// ResolveExactLocalizedGeneration classifies the latest generation attached to
// an existing locale target. Source identity is render provenance, not a
// currentness gate.
func ResolveExactLocalizedGeneration(
	ctx context.Context,
	db *gorm.DB,
	entityType, entityID, locale string,
) (og.LocalizedGenerationDisposition, error) {
	var row struct {
		Status string `gorm:"column:status"`
	}
	err := db.WithContext(ctx).Table("og_generation_target AS target").
		Select("generation.status").
		Joins("JOIN og_generation AS generation ON generation.id = target.latest_generation_id").
		Where("target.entity_type = ? AND target.entity_id = ? AND target.target_kind = 'locale' AND target.locale = ?", entityType, entityID, locale).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return og.LocalizedGenerationMissing, nil
	}
	if err != nil {
		return og.LocalizedGenerationMissing, err
	}
	return og.ClassifyLocalizedGeneration(og.GenerationStatus(row.Status)), nil
}
