package og

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// entityBindingKey is the public binding of an entity-row OG image.
const entityBindingKey = "og"

// LocalelessTarget says how a translation-row family answers a target that
// carries no locale. Each family keeps the answer it had before locale targets.
type LocalelessTarget uint8

const (
	// LocalelessMissing completes no row: the family never kept an entity-row
	// pointer, so completion reports the target as gone.
	LocalelessMissing LocalelessTarget = iota
	// LocalelessEntityRow completes the entity row's own "og" pointer.
	LocalelessEntityRow
	// LocalelessRejected fails before any write.
	LocalelessRejected
)

// TranslationProjectionSpec declares an entity family whose OG pointers live on
// its translation rows (`<entity>_translation.og_asset_id`), bound per locale.
type TranslationProjectionSpec struct {
	EntityType string
	// EntityTitle names the family in client messages.
	EntityTitle string
	Localeless  LocalelessTarget
}

// TranslationProjection owns one translation-row family's OG pointers and
// public bindings.
type TranslationProjection struct {
	spec TranslationProjectionSpec
}

func NewTranslationProjection(spec TranslationProjectionSpec) *TranslationProjection {
	return &TranslationProjection{spec: spec}
}

func (p *TranslationProjection) Handles(target Target) bool {
	return target.EntityType == p.spec.EntityType
}

func (p *TranslationProjection) ReleasePending(ctx context.Context, tx *gorm.DB, target Target, cdnDomain string) error {
	locale, err := p.locale(target)
	if err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Table(p.translationTable()).
		Where("entity_id = ? AND locale = ?", target.EntityID, locale).
		Update("og_asset_id", nil).Error; err != nil {
		return err
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).ReleaseExactPublicAssetBindings(
		ctx, target.EntityType, target.EntityID, []string{localeBindingKey(locale)},
	)
}

func (p *TranslationProjection) Complete(
	ctx context.Context,
	tx *gorm.DB,
	target Target,
	assetID string,
	now time.Time,
	cdnDomain string,
) error {
	locale, err := p.locale(target)
	if err != nil {
		return err
	}
	switch {
	case locale != "":
		err = updateOGPointer(ctx, tx, p.translationTable(), "entity_id = ? AND locale = ?", assetID, now, ErrTranslationTargetMissing, target.EntityID, locale)
	case p.spec.Localeless == LocalelessEntityRow:
		err = updateOGPointer(ctx, tx, p.spec.EntityType, "id = ?", assetID, now, gorm.ErrRecordNotFound, target.EntityID)
	default:
		return gorm.ErrRecordNotFound
	}
	if err != nil {
		return err
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).BindPublicAsset(ctx, mediaasset.Binding{
		AssetID: assetID, OwnerType: target.EntityType, OwnerID: target.EntityID, BindingKey: localeBindingKey(locale),
	})
}

func (p *TranslationProjection) locale(target Target) (string, error) {
	if !p.Handles(target) {
		return "", errs.InvalidEntityType(target.EntityType)
	}
	locale, err := target.CanonicalLocale()
	if err != nil {
		return "", err
	}
	if locale == "" && p.spec.Localeless == LocalelessRejected {
		return "", errs.FailedPrecondition(p.spec.EntityTitle + " OG target locale is required")
	}
	return locale, nil
}

func (p *TranslationProjection) translationTable() string {
	return p.spec.EntityType + "_translation"
}

func localeBindingKey(locale string) string {
	if locale == "" {
		return entityBindingKey
	}
	return entityBindingKey + "_" + locale
}

// EntityProjection owns the single, non-localized OG pointer that an entity
// family keeps on its own row (`<entity>.og_asset_id`, binding "og").
type EntityProjection struct {
	entityType string
}

func NewEntityProjection(entityType string) *EntityProjection {
	return &EntityProjection{entityType: entityType}
}

func (p *EntityProjection) Handles(target Target) bool {
	return target.EntityType == p.entityType
}

func (p *EntityProjection) ReleasePending(ctx context.Context, tx *gorm.DB, target Target, cdnDomain string) error {
	if !p.Handles(target) {
		return errs.InvalidEntityType(target.EntityType)
	}
	if err := tx.WithContext(ctx).Table(p.entityType).
		Where("id = ?", target.EntityID).
		Update("og_asset_id", nil).Error; err != nil {
		return err
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).ReleaseExactPublicAssetBindings(
		ctx, p.entityType, target.EntityID, []string{entityBindingKey},
	)
}

func (p *EntityProjection) Complete(
	ctx context.Context,
	tx *gorm.DB,
	target Target,
	assetID string,
	now time.Time,
	cdnDomain string,
) error {
	if !p.Handles(target) {
		return errs.InvalidEntityType(target.EntityType)
	}
	if target.Locale != nil && strings.TrimSpace(*target.Locale) != "" {
		return gorm.ErrRecordNotFound
	}
	if err := updateOGPointer(ctx, tx, p.entityType, "id = ?", assetID, now, gorm.ErrRecordNotFound, target.EntityID); err != nil {
		return err
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).BindPublicAsset(ctx, mediaasset.Binding{
		AssetID: assetID, OwnerType: p.entityType, OwnerID: target.EntityID, BindingKey: entityBindingKey,
	})
}

// updateOGPointer points one projection row at the generated asset and
// answers missing when no row matched.
func updateOGPointer(
	ctx context.Context,
	tx *gorm.DB,
	table string,
	where string,
	assetID string,
	now time.Time,
	missing error,
	args ...any,
) error {
	result := tx.WithContext(ctx).Table(table).Where(where, args...).
		Updates(structured.Fields{"og_asset_id": assetID, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return missing
	}
	return nil
}

var (
	_ Projection = (*TranslationProjection)(nil)
	_ Projection = (*EntityProjection)(nil)
)
