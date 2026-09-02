package og

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// LocalizedRequestSpec declares one domain's localized entity and title SQL.
// It contains no cross-domain routing; each owner provides exactly its own
// table and source-title expression.
type LocalizedRequestSpec struct {
	EntityType              string
	Table                   string
	TranslationTable        string
	SourceTitleExpression   string
	FeaturedImageExpression string
}

// LocalizedRequests resolves a single owner's entity and locale projections.
type LocalizedRequests struct {
	spec LocalizedRequestSpec
}

func NewLocalizedRequests(spec LocalizedRequestSpec) *LocalizedRequests {
	return &LocalizedRequests{spec: spec}
}

func (r *LocalizedRequests) Resolve(
	ctx context.Context,
	db *gorm.DB,
	entityID string,
	selection *managev1.OgTargetSelection,
) ([]Request, error) {
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return nil, errs.Required("entity_id")
	}
	if selection == nil || selection.GetTarget() == nil {
		return nil, errs.Required("selection")
	}
	base, source, err := r.base(ctx, db, entityID)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, errs.FailedPrecondition("translation source locale is not configured")
	}
	if selection.GetPrimary() != nil {
		return []Request{r.request(entityID, source.SourceLocale, base)}, nil
	}
	if locale := strings.TrimSpace(selection.GetLocale()); locale != "" {
		if locale == source.SourceLocale {
			return []Request{r.request(entityID, locale, base)}, nil
		}
		target, err := r.translation(ctx, db, entityID, locale)
		if err != nil {
			return nil, err
		}
		if target == nil {
			return nil, errs.NotFound(r.spec.EntityType+"_translation", entityID+":"+locale)
		}
		if target.Title != nil {
			base.title = *target.Title
		}
		return []Request{r.request(entityID, locale, base)}, nil
	}
	if selection.GetAllLocales() == nil {
		return nil, errs.InvalidArgument("selection", "translated OG target selection is invalid")
	}
	requests := []Request{r.request(entityID, source.SourceLocale, base)}
	targets, err := r.translations(ctx, db, entityID)
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		localized := base
		if target.Title != nil {
			localized.title = *target.Title
		}
		requests = append(requests, r.request(entityID, target.Locale, localized))
	}
	return requests, nil
}

func (r *LocalizedRequests) All(ctx context.Context, db *gorm.DB) ([]Request, error) {
	rows, err := r.rows(ctx, db)
	if err != nil {
		return nil, err
	}
	requests := make([]Request, 0, len(rows))
	for _, row := range rows {
		if row.SourceLocale == nil || strings.TrimSpace(*row.SourceLocale) == "" {
			return nil, fmt.Errorf("%s %s has no translation source locale", r.spec.EntityType, row.ID)
		}
		sourceLocale := strings.TrimSpace(*row.SourceLocale)
		base := localizedSnapshot{
			title: row.Title, featuredImageFileID: optionalLocalizedFileID(row.FeaturedImageFileID),
		}
		requests = append(requests, r.request(row.ID, sourceLocale, base))
		translations, err := r.translations(ctx, db, row.ID)
		if err != nil {
			return nil, err
		}
		for _, translation := range translations {
			localized := base
			if translation.Title != nil {
				localized.title = *translation.Title
			}
			requests = append(requests, r.request(row.ID, translation.Locale, localized))
		}
	}
	return requests, nil
}

type localizedSnapshot struct {
	title               string
	featuredImageFileID *string
}
type localizedRow struct {
	ID                  string  `gorm:"column:id"`
	Title               string  `gorm:"column:title"`
	FeaturedImageFileID *string `gorm:"column:featured_image_file_id"`
	SourceLocale        *string `gorm:"column:source_locale"`
}
type localizedTranslation struct {
	Locale string  `gorm:"column:locale"`
	Title  *string `gorm:"column:title"`
}

type localizedSourceState struct {
	SourceLocale string `gorm:"column:source_locale"`
}

func (r *LocalizedRequests) base(
	ctx context.Context,
	db *gorm.DB,
	entityID string,
) (localizedSnapshot, *localizedSourceState, error) {
	var row localizedRow
	featuredImageExpression := r.featuredImageExpression()
	result := db.WithContext(ctx).Table(r.spec.Table).
		Select(r.spec.SourceTitleExpression+" AS title, "+featuredImageExpression+" AS featured_image_file_id, id, "+r.spec.Table+".source_locale AS source_locale").
		Where(r.spec.Table+".id = ?", entityID).Take(&row)
	if result.Error != nil {
		return localizedSnapshot{}, nil, result.Error
	}
	if row.SourceLocale == nil || strings.TrimSpace(*row.SourceLocale) == "" {
		return localizedSnapshot{
			title: row.Title, featuredImageFileID: optionalLocalizedFileID(row.FeaturedImageFileID),
		}, nil, nil
	}
	return localizedSnapshot{
		title:               row.Title,
		featuredImageFileID: optionalLocalizedFileID(row.FeaturedImageFileID),
	}, &localizedSourceState{SourceLocale: strings.TrimSpace(*row.SourceLocale)}, nil
}

func (r *LocalizedRequests) rows(ctx context.Context, db *gorm.DB) ([]localizedRow, error) {
	selectClause := fmt.Sprintf(
		"%s.id, %s AS title, %s AS featured_image_file_id, %s.source_locale AS source_locale",
		r.spec.Table,
		r.spec.SourceTitleExpression,
		r.featuredImageExpression(),
		r.spec.Table,
	)
	var rows []localizedRow
	if err := db.WithContext(ctx).Table(r.spec.Table).Select(selectClause).
		Order(r.spec.Table + ".id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *LocalizedRequests) featuredImageExpression() string {
	if expression := strings.TrimSpace(r.spec.FeaturedImageExpression); expression != "" {
		return expression
	}
	return r.spec.Table + ".featured_image_file_id"
}

func (r *LocalizedRequests) translations(
	ctx context.Context,
	db *gorm.DB,
	entityID string,
) ([]localizedTranslation, error) {
	var rows []localizedTranslation
	query, err := r.targetTranslationsQuery(ctx, db, entityID)
	if err != nil {
		return nil, err
	}
	if err := query.
		Order("locale").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		row.Locale = strings.TrimSpace(row.Locale)
	}
	filtered := make([]localizedTranslation, 0, len(rows))
	for _, row := range rows {
		if row.Locale != "" {
			filtered = append(filtered, row)
		}
	}
	return filtered, nil
}

func (r *LocalizedRequests) translation(
	ctx context.Context,
	db *gorm.DB,
	entityID string,
	locale string,
) (*localizedTranslation, error) {
	var row localizedTranslation
	query, err := r.targetTranslationsQuery(ctx, db, entityID)
	if err != nil {
		return nil, err
	}
	err = query.Where("translation.locale = ?", locale).Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *LocalizedRequests) targetTranslationsQuery(
	ctx context.Context,
	db *gorm.DB,
	entityID string,
) (*gorm.DB, error) {
	return db.WithContext(ctx).Table(r.spec.TranslationTable+" AS translation").
		Select("translation.locale, translation.title").
		Joins("JOIN "+r.spec.Table+" AS source_root ON source_root.id = translation.entity_id").
		Where(`translation.entity_id = ?
			AND translation.locale <> source_root.source_locale`, entityID), nil
}

func (r *LocalizedRequests) request(entityID, locale string, snapshot localizedSnapshot) Request {
	return Request{
		Target: Target{
			EntityType: r.spec.EntityType, EntityID: entityID, Locale: &locale, Kind: "locale",
		},
		Title:               snapshot.title,
		FeaturedImageFileID: snapshot.featuredImageFileID,
	}
}

func optionalLocalizedFileID(value *string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}
