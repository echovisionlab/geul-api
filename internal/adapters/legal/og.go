// Package legal adapts Privacy and Terms persistence to Open Graph requests.
package legal

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Current is the canonical Privacy or Terms document selected by its public
// route. Scheduled content is selected only when there is no effective active
// content, matching the public legal route.
type Current struct {
	ID    string `gorm:"column:id"`
	Title string `gorm:"column:title"`
}

// Requests owns manual and global Privacy/Terms Open Graph snapshots.
type Requests struct{}

func NewRequests() *Requests { return &Requests{} }

func (*Requests) Handles(entityType string) bool {
	return entityType == "privacy" || entityType == "terms"
}

func (*Requests) Resolve(
	ctx context.Context,
	db *gorm.DB,
	kind string,
	entityID string,
	selection *managev1.OgTargetSelection,
) ([]og.Request, error) {
	if RouteID(kind) == "" {
		return nil, errs.InvalidEntityType(kind)
	}
	// Legal versions are content history. The generated target is always the
	// stable public route, so a supplied version ID is intentionally ignored.
	_ = entityID
	if selection == nil || selection.GetTarget() == nil {
		return nil, errs.Required("selection")
	}
	current, err := CurrentForRoute(ctx, db, kind)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, errs.NotFound(kind, RouteID(kind))
	}
	backgroundFileID, err := BackgroundFileID(ctx, db, kind)
	if err != nil {
		return nil, err
	}
	if selection.GetAllLocales() != nil {
		return RequestsForCurrent(ctx, db, kind, current, "", true, backgroundFileID)
	}
	locale := strings.TrimSpace(selection.GetLocale())
	if selection.GetPrimary() != nil {
		locale, err = enabledDefaultLocale(ctx, db)
		if err != nil {
			return nil, err
		}
	}
	if locale == "" {
		return nil, errs.InvalidArgument("selection", "legal OG target selection is invalid")
	}
	return RequestsForCurrent(ctx, db, kind, current, locale, false, backgroundFileID)
}

func (*Requests) All(ctx context.Context, db *gorm.DB) ([]og.Request, error) {
	requests := make([]og.Request, 0)
	for _, kind := range []string{"privacy", "terms"} {
		backgroundFileID, err := BackgroundFileID(ctx, db, kind)
		if err != nil {
			return nil, err
		}
		current, err := CurrentRequests(ctx, db, kind, backgroundFileID)
		if err != nil {
			return nil, err
		}
		requests = append(requests, current...)
	}
	return requests, nil
}

// RouteID returns the stable public Open Graph route identity for a legal
// policy type.
func RouteID(kind string) string {
	switch strings.TrimSpace(kind) {
	case "privacy":
		return og.PrivacyRouteEntityID
	case "terms":
		return og.TermsRouteEntityID
	default:
		return ""
	}
}

// CurrentForRoute reads the document currently represented by a legal public
// route. It deliberately does not use a policy version ID as the OG target.
func CurrentForRoute(ctx context.Context, db *gorm.DB, kind string) (*Current, error) {
	table, active, scheduled, err := historySpec(kind)
	if err != nil {
		return nil, err
	}

	var row Current
	result := db.WithContext(ctx).Table(table).
		Select("id, title").
		Where("status = ? AND (effective_from IS NULL OR effective_from <= ?)", active, time.Now().UTC()).
		Order("effective_from DESC, version DESC").Take(&row)
	if result.Error == nil {
		return &row, nil
	}
	if result.Error != gorm.ErrRecordNotFound {
		return nil, result.Error
	}

	result = db.WithContext(ctx).Table(table).
		Select("id, title").Where("status = ?", scheduled).
		Order("effective_from ASC, version DESC").Take(&row)
	if result.Error == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if result.Error != nil {
		return nil, result.Error
	}
	return &row, nil
}

// Title resolves a legal route title for one enabled locale. A missing target
// title uses the canonical policy title, exactly as the public legal route.
func Title(
	ctx context.Context,
	db *gorm.DB,
	kind, documentID, locale, fallback string,
) (string, error) {
	var translated struct {
		Title *string `gorm:"column:title"`
	}
	result := db.WithContext(ctx).Table(strings.TrimSpace(kind)+"_translation").
		Select("title").
		Where("entity_id = ? AND locale = ?", documentID, locale).
		Take(&translated)
	if result.Error != nil && result.Error != gorm.ErrRecordNotFound {
		return "", result.Error
	}
	if translated.Title != nil {
		return *translated.Title, nil
	}
	return strings.TrimSpace(fallback), nil
}

// BackgroundFileID reads the Site Settings slot that supplies a legal route's
// optional source image.
func BackgroundFileID(ctx context.Context, db *gorm.DB, kind string) (*string, error) {
	column := backgroundColumn(kind)
	if column == "" {
		return nil, fmt.Errorf("unsupported legal OG entity type %q", kind)
	}

	var result struct {
		FileID *string `gorm:"column:file_id"`
	}
	if err := db.WithContext(ctx).Table("site_settings").
		Select(column + " AS file_id").Where("site_settings.id = 1").Scan(&result).Error; err != nil {
		return nil, err
	}
	return optionalString(result.FileID), nil
}

// LoadRequests builds locale-scoped Open Graph target snapshots from one canonical
// legal document. Callers must use CurrentForRoute to establish route authority
// before requesting work.
func LoadRequests(
	ctx context.Context,
	db *gorm.DB,
	kind, documentID, fallbackTitle, requestedLocale string,
	allLocales bool,
	backgroundFileID *string,
) ([]og.Request, error) {
	if RouteID(kind) == "" {
		return nil, fmt.Errorf("unsupported legal OG entity type %q", kind)
	}
	locales, err := enabledLocales(ctx, db)
	if err != nil {
		return nil, err
	}
	requestedLocale = strings.TrimSpace(requestedLocale)
	requests := make([]og.Request, 0, len(locales))
	for _, localeValue := range locales {
		locale := strings.TrimSpace(localeValue)
		if locale == "" || (!allLocales && requestedLocale != "" && locale != requestedLocale) {
			continue
		}
		title, err := Title(ctx, db, kind, documentID, locale, fallbackTitle)
		if err != nil {
			return nil, err
		}
		requests = append(requests, og.Request{
			Target: og.Target{
				EntityType: kind, EntityID: RouteID(kind), Locale: &locale, Kind: "locale",
			},
			Title: title, FeaturedImageFileID: optionalString(backgroundFileID),
		})
	}
	if len(requests) == 0 {
		return nil, fmt.Errorf("legal OG locale %q is not enabled", requestedLocale)
	}
	return requests, nil
}

func RequestsForCurrent(
	ctx context.Context,
	db *gorm.DB,
	kind string,
	current *Current,
	requestedLocale string,
	allLocales bool,
	backgroundFileID *string,
) ([]og.Request, error) {
	if current == nil {
		return nil, nil
	}
	return LoadRequests(
		ctx,
		db,
		kind,
		current.ID,
		current.Title,
		requestedLocale,
		allLocales,
		backgroundFileID,
	)
}

// CurrentRequests builds all enabled locale requests for the current legal
// public route. An empty route has no target and therefore no request.
func CurrentRequests(
	ctx context.Context,
	db *gorm.DB,
	kind string,
	backgroundFileID *string,
) ([]og.Request, error) {
	current, err := CurrentForRoute(ctx, db, kind)
	if err != nil || current == nil {
		return nil, err
	}
	return LoadRequests(ctx, db, kind, current.ID, current.Title, "", true, backgroundFileID)
}

// RequestSaved plans OG work only when the saved legal version is currently
// selected by its fixed public route.
func RequestSaved(
	ctx context.Context,
	tx *gorm.DB,
	planner *og.Planner,
	kind string,
	documentID string,
	requestedLocale string,
	allLocales bool,
	reason string,
) (*og.Plan, error) {
	table, active, scheduled, err := historySpec(kind)
	if err != nil {
		return nil, err
	}
	var saved struct {
		Status string `gorm:"column:status"`
		Title  string `gorm:"column:title"`
	}
	if err := tx.WithContext(ctx).Table(table).
		Select("status, title").Where("id = ?", documentID).Take(&saved).Error; err != nil {
		return nil, err
	}
	if saved.Status != active && saved.Status != scheduled {
		return nil, nil
	}
	if planner == nil {
		return nil, errs.DependencyUnavailable("legal OG planner")
	}
	current, err := CurrentForRoute(ctx, tx, kind)
	if err != nil || current == nil || current.ID != documentID {
		return nil, err
	}
	backgroundFileID, err := BackgroundFileID(ctx, tx, kind)
	if err != nil {
		return nil, err
	}
	requests, err := LoadRequests(
		ctx,
		tx,
		kind,
		current.ID,
		current.Title,
		requestedLocale,
		allLocales,
		backgroundFileID,
	)
	if err != nil {
		return nil, err
	}
	return planner.RequestBulkReloadedWithDB(
		ctx,
		tx,
		"automatic",
		reason,
		requests,
		func(reloadCtx context.Context, reloadTx *gorm.DB) ([]og.Request, error) {
			reloaded, reloadErr := CurrentForRoute(reloadCtx, reloadTx, kind)
			if reloadErr != nil {
				return nil, reloadErr
			}
			if reloaded == nil || reloaded.ID != documentID {
				return nil, fmt.Errorf("canonical %s OG content changed while taking its snapshot", kind)
			}
			reloadedBackgroundFileID, backgroundErr := BackgroundFileID(reloadCtx, reloadTx, kind)
			if backgroundErr != nil {
				return nil, backgroundErr
			}
			return LoadRequests(
				reloadCtx,
				reloadTx,
				kind,
				reloaded.ID,
				reloaded.Title,
				requestedLocale,
				allLocales,
				reloadedBackgroundFileID,
			)
		},
	)
}

// ReleaseAssets removes static legal OG bindings after the legal owner has
// deleted a policy version. The durable OG projection owns pointer clearing.
func ReleaseAssets(ctx context.Context, db *gorm.DB, cdnDomain, kind, documentID string) error {
	return mediaasset.NewLifecycle(db, cdnDomain).
		ReleasePublicAssetBindings(ctx, strings.TrimSpace(kind), documentID, "og")
}

// LockActivation serializes activation for the one public legal route.
func LockActivation(ctx context.Context, tx *gorm.DB, kind string) error {
	if kind != "privacy" && kind != "terms" {
		return fmt.Errorf("unsupported legal OG entity type %q", kind)
	}
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.WithContext(ctx).
		Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "geul:legal-activation:"+kind).
		Error
}

// Projection owns the static legal OG bindings. Legal public routes have no
// entity pointer: metadata resolves the ready route target directly.
type Projection struct{}

func NewProjection() *Projection { return &Projection{} }

func (*Projection) Handles(target og.Target) bool {
	return target.EntityType == "privacy" || target.EntityType == "terms"
}

func (p *Projection) ReleasePending(
	ctx context.Context,
	tx *gorm.DB,
	target og.Target,
	cdnDomain string,
) error {
	if err := validateTarget(target); err != nil {
		return err
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).ReleaseExactPublicAssetBindings(
		ctx, target.EntityType, target.EntityID, []string{bindingKey(target.Locale)},
	)
}

func (p *Projection) Complete(
	ctx context.Context,
	tx *gorm.DB,
	target og.Target,
	assetID string,
	_ time.Time,
	cdnDomain string,
) error {
	if err := validateTarget(target); err != nil {
		return err
	}
	current, err := CurrentForRoute(ctx, tx, target.EntityType)
	if err != nil {
		return err
	}
	if current == nil {
		return og.ErrTranslationTargetMissing
	}
	locale := optionalString(target.Locale)
	if locale == nil {
		return og.ErrTranslationTargetMissing
	}
	var matched int64
	query := tx.WithContext(ctx).
		Table(target.EntityType+"_translation").
		Where("entity_id = ? AND locale = ?", current.ID, *locale)
	if err := query.Count(&matched).Error; err != nil {
		return err
	}
	if matched != 1 {
		return og.ErrTranslationTargetMissing
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).BindPublicAsset(ctx, mediaasset.Binding{
		AssetID: assetID, OwnerType: target.EntityType, OwnerID: target.EntityID,
		BindingKey: bindingKey(target.Locale),
	})
}

func historySpec(kind string) (table, active, scheduled string, err error) {
	switch strings.TrimSpace(kind) {
	case "privacy":
		return "privacy_history", managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(),
			managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(), nil
	case "terms":
		return "terms_history", managev1.TermsStatus_TERMS_STATUS_ACTIVE.String(),
			managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(), nil
	default:
		return "", "", "", fmt.Errorf("unsupported legal OG entity type %q", kind)
	}
}

func backgroundColumn(kind string) string {
	switch strings.TrimSpace(kind) {
	case "privacy":
		return "privacy_og_background_file_id"
	case "terms":
		return "terms_og_background_file_id"
	default:
		return ""
	}
}

func enabledLocales(ctx context.Context, db *gorm.DB) ([]string, error) {
	runtimeLocales, err := localization.NewCatalog(db).Enabled(ctx)
	if err != nil {
		return nil, err
	}
	locales := make([]string, 0, len(runtimeLocales))
	for _, locale := range runtimeLocales {
		locales = append(locales, locale.Code)
	}
	return locales, nil
}

func enabledDefaultLocale(ctx context.Context, db *gorm.DB) (string, error) {
	var settings model.TranslationSettings
	if err := db.WithContext(ctx).First(&settings).Error; err != nil {
		return "", err
	}
	locale := strings.TrimSpace(settings.DefaultLocale)
	runtimeLocale, err := localization.NewCatalog(db).Find(ctx, locale)
	if err != nil {
		return "", err
	}
	if !runtimeLocale.Enabled {
		return "", errs.FailedPrecondition("default translation locale is not enabled")
	}
	return locale, nil
}

func optionalString(value *string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}

func validateTarget(target og.Target) error {
	if (target.EntityType != "privacy" && target.EntityType != "terms") || target.EntityID != RouteID(target.EntityType) {
		return errs.FailedPrecondition("legal OG target does not use the canonical route identity")
	}
	if optionalString(target.Locale) == nil {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func bindingKey(locale *string) string {
	if normalized := optionalString(locale); normalized != nil {
		return "og:" + *normalized
	}
	return "og"
}

var _ og.Projection = (*Projection)(nil)
