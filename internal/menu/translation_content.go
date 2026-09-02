// Package menu owns Menu structure and locale-label projection rules.
package menu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// BuildTranslationExtractionPlan extracts the Menu labels visible to one
// target locale. Source structure remains outside the provider contract.
func BuildTranslationExtractionPlan(
	menuID string,
	sourceLocale string,
	targetLocale string,
	source *translation.SourceDocument,
) (*translation.ExtractionPlan, error) {
	if len(source.ContentJSON) == 0 {
		return nil, translation.ErrNoTranslatableUnits
	}
	var items []model.MenuItem
	if err := json.Unmarshal(source.ContentJSON, &items); err != nil {
		return nil, fmt.Errorf("failed to parse menu items: %w", err)
	}

	units := make([]translation.Unit, 0)
	collectTranslationUnits(menuID, sourceLocale, targetLocale, items, &units)
	if len(units) == 0 {
		return nil, translation.ErrNoTranslatableUnits
	}
	bundle := translation.Bundle{
		BundleID: "entity:main", EntityType: "menu", EntityID: menuID,
		SourceLocale: sourceLocale, TargetLocale: targetLocale,
		BundleType: translation.BundleTypeEntity, SequenceTotal: 1, Units: units,
	}
	return &translation.ExtractionPlan{
		EntityType: "menu", EntityID: menuID,
		SourceLocale: sourceLocale, TargetLocale: targetLocale,
		Units: units, Bundles: []translation.Bundle{bundle},
	}, nil
}

// LoadTranslationSourceDocument loads the source-owned Menu document exposed
// to the translation pipeline.
func LoadTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	menuID string,
) (*translation.SourceDocument, error) {
	var root struct {
		Items        []byte `gorm:"column:items"`
		SourceLocale string `gorm:"column:source_locale"`
	}
	result := db.WithContext(ctx).Table("menu").Select("items, source_locale").Where("id = ?", menuID).Limit(1).Scan(&root)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	root.SourceLocale = strings.TrimSpace(root.SourceLocale)
	if root.SourceLocale == "" {
		return nil, errs.FailedPrecondition("Menu source locale is not initialized")
	}

	var localeRow struct {
		ItemsJSON []byte `gorm:"column:items_json"`
	}
	localeResult := db.WithContext(ctx).Table("menu_translation").Select("items_json").
		Where("entity_id = ? AND locale = ?", menuID, root.SourceLocale).Take(&localeRow)
	if localeResult.Error != nil {
		if errors.Is(localeResult.Error, gorm.ErrRecordNotFound) {
			return nil, errs.FailedPrecondition("Menu source locale values are not initialized")
		}
		return nil, localeResult.Error
	}
	items, err := decodeSourceItems(root.Items)
	if err != nil {
		return nil, err
	}
	labels, err := DecodeTranslationLabelValues(localeRow.ItemsJSON)
	if err != nil {
		return nil, errs.Internal(err)
	}
	applyMenuLocaleLabels(items, root.SourceLocale, labels)
	body, err := json.Marshal(items)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return &translation.SourceDocument{ContentJSON: body}, nil
}

func applyMenuLocaleLabels(items []model.MenuItem, locale string, labels map[string]string) {
	for i := range items {
		if ShouldTranslateItemLabel(&items[i], locale) {
			items[i].Label = labels[items[i].ID]
		}
		applyMenuLocaleLabels(items[i].Children, locale, labels)
	}
}

// BuildTranslationCandidate emits only translated label values keyed by stable
// source item ID. Missing results remain absent and an explicit empty result is
// retained as a present empty value.
func BuildTranslationCandidate(
	source *translation.SourceDocument,
	results map[string]translation.UnitResult,
) (*translation.Candidate, error) {
	var items []model.MenuItem
	if err := json.Unmarshal(source.ContentJSON, &items); err != nil {
		return nil, fmt.Errorf("failed to parse menu items: %w", err)
	}
	labels := make(map[string]string)
	collectTranslationResultLabels(items, results, labels)
	body, err := EncodeTranslationLabelValues(labels)
	if err != nil {
		return nil, fmt.Errorf("failed to encode translated menu items: %w", err)
	}
	return &translation.Candidate{ContentJSON: body}, nil
}

func collectTranslationUnits(
	menuID string,
	sourceLocale string,
	targetLocale string,
	items []model.MenuItem,
	units *[]translation.Unit,
) {
	for _, item := range items {
		label := strings.TrimSpace(item.Label)
		if label != "" && ShouldTranslateItemLabel(&item, targetLocale) {
			path := fmt.Sprintf("item:%s:label", item.ID)
			*units = append(*units, translation.Unit{
				UnitID: path, EntityType: "menu", EntityID: menuID, Path: path,
				ContainerType: translation.ContainerTypeBlock, ContainerID: item.ID,
				FieldName: "label", SourceText: label,
				SourceFormat: translation.SourceFormatPlainText, SourceLocale: sourceLocale,
				Context: stringPointer(strings.TrimSpace(item.LinkType)),
			})
		}
		collectTranslationUnits(menuID, sourceLocale, targetLocale, item.Children, units)
	}
}

func collectTranslationResultLabels(
	items []model.MenuItem,
	results map[string]translation.UnitResult,
	labels map[string]string,
) {
	for index := range items {
		item := &items[index]
		if result, ok := results[fmt.Sprintf("item:%s:label", item.ID)]; ok {
			labels[item.ID] = strings.TrimSpace(result.TranslatedText)
		}
		collectTranslationResultLabels(item.Children, results, labels)
	}
}

func stringPointer(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	copy := value
	return &copy
}
