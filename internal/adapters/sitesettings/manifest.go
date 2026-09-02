package sitesettings

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/localization"
	menudomain "github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// ManifestMenus adapts Menu and Page read models to the public Site Settings
// manifest port. It owns no mutation or lifecycle state.
type ManifestMenus struct{}

type manifestMenuTranslationRow struct {
	Locale    string `gorm:"column:locale"`
	ItemsJSON []byte `gorm:"column:items_json"`
}

func (ManifestMenus) Localize(
	ctx context.Context,
	db *gorm.DB,
	menuID string,
	sourceItems []model.MenuItem,
	acceptLanguage string,
) ([]model.MenuItem, error) {
	settings, err := translation.LoadRuntimeSettings(ctx, db)
	if err != nil {
		settings = translation.DefaultRuntimeSettings()
	}
	requested := settings.DefaultLocale
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		requested = *locale
	}
	sourceLocale := settings.DefaultLocale
	var source struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := db.WithContext(ctx).Table("menu").Select("source_locale").Where("id = ?", menuID).Take(&source).Error; err == nil {
		sourceLocale = localization.NormalizeWithDefault(source.SourceLocale, settings.DefaultLocale)
	} else if err != gorm.ErrRecordNotFound {
		return sourceItems, err
	}

	locales := collectManifestMenuLocales(sourceItems, requested, sourceLocale)
	rows, err := loadManifestMenuRows(ctx, db, menuID, locales)
	if err != nil {
		return sourceItems, err
	}
	if _, exists := rows[sourceLocale]; !exists {
		return sourceItems, fmt.Errorf("menu source locale %q values are not initialized", sourceLocale)
	}
	labels, err := manifestMenuLabels(rows)
	if err != nil {
		return sourceItems, err
	}
	storedLocales := make(map[string]struct{}, len(rows))
	for locale := range rows {
		storedLocales[locale] = struct{}{}
	}
	decision := localization.Select(requested, sourceLocale, storedLocales, localization.ServingPolicy{DefaultLocale: settings.DefaultLocale})
	return projectManifestMenuLabels(sourceItems, labels, storedLocales, decision.DisplayedLocale, sourceLocale), nil
}

func collectManifestMenuLocales(items []model.MenuItem, requested, source string) []string {
	seen := map[string]struct{}{source: {}, requested: {}}
	var walk func([]model.MenuItem)
	walk = func(current []model.MenuItem) {
		for i := range current {
			if fixed := menudomain.NormalizeItemFixedLocale(current[i].FixedLocale); fixed != nil {
				seen[*fixed] = struct{}{}
			}
			walk(current[i].Children)
		}
	}
	walk(items)
	locales := make([]string, 0, len(seen))
	for locale := range seen {
		locales = append(locales, locale)
	}
	return locales
}

func loadManifestMenuRows(
	ctx context.Context,
	db *gorm.DB,
	menuID string,
	locales []string,
) (map[string]manifestMenuTranslationRow, error) {
	spec := publiccontent.Spec{
		EntityType: "menu", TableName: "menu_translation",
		SelectClause: "locale, items_json",
	}
	query, err := publiccontent.Rows(ctx, db, spec)
	if err != nil {
		return nil, err
	}
	var rows []manifestMenuTranslationRow
	if err := query.Select(spec.SelectClause).Where("entity_id = ? AND locale IN ?", menuID, locales).Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]manifestMenuTranslationRow, len(rows))
	for _, row := range rows {
		result[row.Locale] = row
	}
	return result, nil
}

func manifestMenuLabels(rows map[string]manifestMenuTranslationRow) (map[string]map[string]string, error) {
	result := make(map[string]map[string]string, len(rows))
	for locale, row := range rows {
		labels, err := menudomain.DecodeTranslationLabelValues(row.ItemsJSON)
		if err != nil {
			return nil, fmt.Errorf("decode stored Menu locale %q: %w", locale, err)
		}
		result[locale] = labels
	}
	return result, nil
}

func projectManifestMenuLabels(
	items []model.MenuItem,
	labels map[string]map[string]string,
	storedLocales map[string]struct{},
	displayLocale string,
	sourceLocale string,
) []model.MenuItem {
	projected := make([]model.MenuItem, len(items))
	for i := range items {
		item := items[i]
		sourceLabel := item.Label
		if menudomain.ShouldTranslateItemLabel(&item, sourceLocale) {
			sourceLabel = labels[sourceLocale][item.ID]
		}
		locale := displayLocale
		if menudomain.NormalizeItemLocalizationMode(item.LocalizationMode, item.FixedLocale) == model.MenuItemLocalizationModeFixedLocale {
			locale = sourceLocale
			if fixed := menudomain.NormalizeItemFixedLocale(item.FixedLocale); fixed != nil && *fixed != sourceLocale {
				if _, exists := storedLocales[*fixed]; exists {
					locale = *fixed
				}
			}
		}
		item.Label = sourceLabel
		if locale != sourceLocale {
			if label, present := labels[locale][item.ID]; present {
				item.Label = label
			}
		}
		item.Children = projectManifestMenuLabels(item.Children, labels, storedLocales, displayLocale, sourceLocale)
		projected[i] = item
	}
	return projected
}

type publicPageMenuTarget struct {
	ID     string  `gorm:"column:id"`
	Slug   *string `gorm:"column:slug"`
	Status string  `gorm:"column:status"`
}

func (ManifestMenus) PublishedPageTargets(ctx context.Context, db *gorm.DB, items []model.MenuItem) ([]model.MenuItem, error) {
	ids, slugs := collectPageTargetKeys(items)
	if len(ids) == 0 && len(slugs) == 0 {
		return items, nil
	}
	query := db.WithContext(ctx).Table("page").Select("CAST(id AS TEXT) AS id", "slug", "status")
	if len(ids) > 0 && len(slugs) > 0 {
		query = query.Where("CAST(id AS TEXT) IN ? OR slug IN ?", ids, slugs)
	} else if len(ids) > 0 {
		query = query.Where("CAST(id AS TEXT) IN ?", ids)
	} else {
		query = query.Where("slug IN ?", slugs)
	}
	var rows []publicPageMenuTarget
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load public Page menu targets: %w", err)
	}
	byID, bySlug := make(map[string]publicPageMenuTarget, len(rows)), make(map[string]publicPageMenuTarget, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
		if row.Slug != nil {
			bySlug[strings.TrimSpace(*row.Slug)] = row
		}
	}
	return filterPublishedPageTargets(items, byID, bySlug), nil
}

func collectPageTargetKeys(items []model.MenuItem) ([]string, []string) {
	idSet, slugSet := map[string]struct{}{}, map[string]struct{}{}
	var walk func([]model.MenuItem)
	walk = func(current []model.MenuItem) {
		for i := range current {
			item := &current[i]
			if item.LinkType == "page" {
				if item.TargetID != nil && strings.TrimSpace(*item.TargetID) != "" {
					idSet[strings.TrimSpace(*item.TargetID)] = struct{}{}
				} else if item.TargetSlug != nil && strings.TrimSpace(*item.TargetSlug) != "" {
					slugSet[strings.TrimSpace(*item.TargetSlug)] = struct{}{}
				}
			}
			walk(item.Children)
		}
	}
	walk(items)
	ids, slugs := make([]string, 0, len(idSet)), make([]string, 0, len(slugSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	for slug := range slugSet {
		slugs = append(slugs, slug)
	}
	return ids, slugs
}

func filterPublishedPageTargets(items []model.MenuItem, byID, bySlug map[string]publicPageMenuTarget) []model.MenuItem {
	result := make([]model.MenuItem, 0, len(items))
	for i := range items {
		item := items[i]
		if item.LinkType == "page" {
			var target publicPageMenuTarget
			var ok bool
			if item.TargetID != nil && strings.TrimSpace(*item.TargetID) != "" {
				target, ok = byID[strings.TrimSpace(*item.TargetID)]
			} else if item.TargetSlug != nil {
				target, ok = bySlug[strings.TrimSpace(*item.TargetSlug)]
			}
			if !ok || target.Status != managev1.PageStatus_PAGE_STATUS_PUBLISHED.String() {
				continue
			}
			route := target.ID
			if target.Slug != nil && strings.TrimSpace(*target.Slug) != "" {
				route = strings.TrimSpace(*target.Slug)
			}
			item.TargetSlug = &route
		}
		item.Children = filterPublishedPageTargets(item.Children, byID, bySlug)
		result = append(result, item)
	}
	return result
}

func (ManifestMenus) TargetSlug(ctx context.Context, db *gorm.DB, item *model.MenuItem) *string {
	if item == nil {
		return nil
	}
	if item.LinkType == "page" && item.TargetSlug != nil && strings.TrimSpace(*item.TargetSlug) != "" {
		return item.TargetSlug
	}
	if item.TargetID == nil || strings.TrimSpace(*item.TargetID) == "" {
		return item.TargetSlug
	}
	table := map[string]string{"page": "page", "category": "category", "tag": "tag", "series": "series"}[item.LinkType]
	if table == "" {
		return item.TargetSlug
	}
	var row struct {
		Slug *string `gorm:"column:slug"`
	}
	if err := db.WithContext(ctx).Table(table).Select("slug").Where("id = ?", strings.TrimSpace(*item.TargetID)).Take(&row).Error; err != nil || row.Slug == nil || strings.TrimSpace(*row.Slug) == "" {
		return item.TargetSlug
	}
	slug := strings.TrimSpace(*row.Slug)
	return &slug
}
