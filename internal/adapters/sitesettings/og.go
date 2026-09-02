package sitesettings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	settingsdomain "github.com/echovisionlab/geul-api/internal/sitesettings"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const siteRouteID = og.SiteEntityID

// RequestCollector gathers all current OG targets after a global or content
// invalidation. It is injected because Site Settings does not own those rows.
type RequestCollector func(context.Context, *gorm.DB) ([]og.Request, error)

// LegalRequestCollector gathers current Privacy or Terms route targets for a
// changed legal background. It is injected to keep Site Settings independent
// of legal persistence.
type LegalRequestCollector func(context.Context, *gorm.DB, string, *string) ([]og.Request, error)

// Invalidator translates an atomic Site Settings mutation into durable OG work.
type Invalidator struct {
	planner *og.Planner
	all     RequestCollector
	legal   LegalRequestCollector
}

func NewInvalidator(
	planner *og.Planner,
	all RequestCollector,
	legal LegalRequestCollector,
) *Invalidator {
	if planner == nil || all == nil || legal == nil {
		panic("site settings OG invalidator dependencies are required")
	}
	return &Invalidator{planner: planner, all: all, legal: legal}
}

// Requests owns the Site Settings target used by manual and global Open Graph
// regeneration.
type Requests struct{}

func NewRequests() *Requests { return &Requests{} }

func (*Requests) Handles(entityType string) bool { return entityType == "site" }

func (*Requests) Resolve(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
	selection *managev1.OgTargetSelection,
) ([]og.Request, error) {
	if entityType != "site" {
		return nil, errs.InvalidEntityType(entityType)
	}
	if selection == nil || selection.GetPrimary() == nil {
		return nil, errs.InvalidArgument("selection", "site OG generation requires primary target")
	}
	entityID = strings.TrimSpace(entityID)
	if entityID != "" && entityID != siteRouteID {
		return nil, errs.InvalidArgument("entity_id", "site OG target must use the canonical site identity")
	}
	return siteRequest(ctx, db)
}

func (*Requests) All(ctx context.Context, db *gorm.DB) ([]og.Request, error) {
	return siteRequest(ctx, db)
}

func siteRequest(ctx context.Context, db *gorm.DB) ([]og.Request, error) {
	var settings model.SiteSettings
	if err := db.WithContext(ctx).Select("site_og_background_file_id").First(&settings, "id = 1").Error; err != nil {
		return nil, err
	}
	return []og.Request{{
		Target: og.Target{EntityType: "site", EntityID: siteRouteID, Kind: "entity"},
		Title:  "Home", FeaturedImageFileID: optionalString(settings.SiteOgBackgroundFileID),
	}}, nil
}

func (i *Invalidator) Request(
	ctx context.Context,
	tx *gorm.DB,
	before *model.SiteSettings,
	after *model.SiteSettings,
	keys []string,
) (*string, error) {
	invalidation := settingsdomain.ClassifyOGInvalidation(before, after, keys)
	if !invalidation.All && !invalidation.Site && !invalidation.Content && !invalidation.Privacy && !invalidation.Terms {
		return nil, nil
	}
	requests, err := i.requests(ctx, tx, after, invalidation)
	if err != nil {
		return nil, err
	}
	if len(requests) == 0 {
		return nil, nil
	}
	plan, err := i.planner.RequestBulkReloadedWithDB(
		ctx, tx, "automatic", "site_settings_updated", requests,
		func(reloadCtx context.Context, reloadTx *gorm.DB) ([]og.Request, error) {
			return i.requests(reloadCtx, reloadTx, after, invalidation)
		},
	)
	if err != nil || plan == nil {
		return nil, err
	}
	return &plan.RunID, nil
}

func (i *Invalidator) requests(
	ctx context.Context,
	tx *gorm.DB,
	after *model.SiteSettings,
	invalidation settingsdomain.OGInvalidation,
) ([]og.Request, error) {
	var requests []og.Request
	if invalidation.All || invalidation.Content {
		all, err := i.all(ctx, tx)
		if err != nil {
			return nil, err
		}
		for _, request := range all {
			if invalidation.Content && !invalidation.All && request.EntityType == "site" {
				continue
			}
			requests = append(requests, request)
		}
	}
	if !invalidation.All && invalidation.Site {
		requests = append(requests, og.Request{
			Target: og.Target{EntityType: "site", EntityID: siteRouteID, Kind: "entity"},
			Title:  "Home", FeaturedImageFileID: optionalString(after.SiteOgBackgroundFileID),
		})
	}
	if invalidation.All || invalidation.Content {
		return dedupe(requests), nil
	}
	for _, legal := range []struct {
		kind       string
		changed    bool
		background *string
	}{
		{kind: "privacy", changed: invalidation.Privacy, background: after.PrivacyOgBackgroundFileID},
		{kind: "terms", changed: invalidation.Terms, background: after.TermsOgBackgroundFileID},
	} {
		if !legal.changed {
			continue
		}
		current, err := i.legal(ctx, tx, legal.kind, legal.background)
		if err != nil {
			return nil, err
		}
		requests = append(requests, current...)
	}
	return dedupe(requests), nil
}

// RenderConfig supplies the run-wide Site Settings render snapshot.
type RenderConfig struct{}

func NewRenderConfig() *RenderConfig { return &RenderConfig{} }

func (*RenderConfig) Snapshot(ctx context.Context, db *gorm.DB, cdnDomain string) ([]byte, string, error) {
	var settings model.SiteSettings
	if err := db.WithContext(ctx).First(&settings, "id = 1").Error; err != nil {
		return nil, "", err
	}
	snapshot := renderSnapshot{SiteTitle: settings.SiteTitle, PrimaryColor: settings.PrimaryColor}
	if len(settings.OGImageConfig) > 0 {
		if !json.Valid(settings.OGImageConfig) {
			return nil, "", fmt.Errorf("site OG image config is not valid JSON")
		}
		var config structured.Value
		if err := json.Unmarshal(settings.OGImageConfig, &config); err != nil {
			return nil, "", err
		}
		canonical, err := json.Marshal(config)
		if err != nil {
			return nil, "", err
		}
		snapshot.OGImageConfig = canonical
	}
	if settings.LogoLightFileID != nil {
		asset, err := mediaasset.NewLifecycle(db, cdnDomain).
			ReadyAssetRefForSourceFile(ctx, *settings.LogoLightFileID, "logo")
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", err
		}
		if err == nil {
			snapshot.LogoAsset = snapshotRef(asset)
		}
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(payload)
	return payload, hex.EncodeToString(digest[:]), nil
}

// Projection owns Site Settings' current OG pointer and public binding.
type Projection struct{}

func NewProjection() *Projection { return &Projection{} }

func (*Projection) Handles(target og.Target) bool { return target.EntityType == "site" }

func (p *Projection) ReleasePending(
	ctx context.Context,
	tx *gorm.DB,
	target og.Target,
	cdnDomain string,
) error {
	if err := validateTarget(target); err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Table("site_settings").Where("id = ?", 1).
		Update("site_og_asset_id", nil).Error; err != nil {
		return err
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).ReleaseExactPublicAssetBindings(
		ctx, "site_settings", "1", []string{"og"},
	)
}

func (p *Projection) Complete(
	ctx context.Context,
	tx *gorm.DB,
	target og.Target,
	assetID string,
	now time.Time,
	cdnDomain string,
) error {
	if err := validateTarget(target); err != nil {
		return err
	}
	result := tx.WithContext(ctx).Table("site_settings").Where("id = ?", 1).
		Updates(structured.Fields{"site_og_asset_id": assetID, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).BindPublicAsset(ctx, mediaasset.Binding{
		AssetID: assetID, OwnerType: "site_settings", OwnerID: "1", BindingKey: "og",
	})
}

type renderSnapshot struct {
	SiteTitle     string          `json:"site_title"`
	PrimaryColor  string          `json:"primary_color"`
	LogoAsset     *assetSnapshot  `json:"logo_asset,omitempty"`
	OGImageConfig json.RawMessage `json:"og_image_config,omitempty"`
}

type assetSnapshot struct {
	AssetID   string `json:"asset_id"`
	URL       string `json:"url"`
	Extension string `json:"extension"`
	MimeType  string `json:"mime_type"`
}

func snapshotRef(asset interface {
	GetAssetId() string
	GetUrl() string
	GetExtension() string
	GetMimeType() string
}) *assetSnapshot {
	if asset == nil {
		return nil
	}
	return &assetSnapshot{
		AssetID: asset.GetAssetId(), URL: asset.GetUrl(), Extension: asset.GetExtension(), MimeType: asset.GetMimeType(),
	}
}

func dedupe(requests []og.Request) []og.Request {
	result := make([]og.Request, 0, len(requests))
	seen := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		locale := ""
		if request.Locale != nil {
			locale = strings.TrimSpace(*request.Locale)
		}
		key := strings.TrimSpace(request.EntityType) + "\x00" + strings.TrimSpace(request.EntityID) + "\x00" + locale
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, request)
	}
	return result
}

func optionalString(value *string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}

func validateTarget(target og.Target) error {
	if target.EntityType != "site" || target.EntityID != siteRouteID {
		return errs.FailedPrecondition("site OG target does not use the canonical site identity")
	}
	if optionalString(target.Locale) != nil {
		return gorm.ErrRecordNotFound
	}
	return nil
}

var _ settingsdomain.OGInvalidator = (*Invalidator)(nil)
var _ og.RenderConfig = (*RenderConfig)(nil)
var _ og.Projection = (*Projection)(nil)
