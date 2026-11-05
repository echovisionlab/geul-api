package menu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
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
	var items []Item
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
// to the translation pipeline, through store bound to its transaction. A
// missing Menu answers ErrMenuNotFound.
func LoadTranslationSourceDocument(
	ctx context.Context,
	store Store,
	menuID string,
) (*translation.SourceDocument, error) {
	root, err := store.Menu(ctx, menuID)
	if err != nil {
		return nil, err
	}
	sourceLocale := strings.TrimSpace(root.SourceLocale)
	if sourceLocale == "" {
		return nil, domainerr.Precondition("Menu source locale is not initialized")
	}
	values, err := store.Locale(ctx, menuID, sourceLocale)
	if err != nil {
		if errors.Is(err, ErrLocaleNotFound) {
			return nil, domainerr.Precondition("Menu source locale values are not initialized")
		}
		return nil, err
	}
	items, err := decodeSourceItems(root.ItemsJSON)
	if err != nil {
		return nil, err
	}
	applyMenuLocaleLabels(items, sourceLocale, values.Labels)
	body, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	return &translation.SourceDocument{ContentJSON: body}, nil
}

func applyMenuLocaleLabels(items []Item, locale string, labels map[string]string) {
	for i := range items {
		if items[i].ownsLabel(locale) {
			items[i].Label = labels[items[i].ID]
		}
		applyMenuLocaleLabels(items[i].Children, locale, labels)
	}
}

func collectTranslationUnits(
	menuID string,
	sourceLocale string,
	targetLocale string,
	items []Item,
	units *[]translation.Unit,
) {
	for _, item := range items {
		label := strings.TrimSpace(item.Label)
		if label != "" && item.ownsLabel(targetLocale) {
			path := fmt.Sprintf("item:%s:label", item.ID)
			*units = append(*units, translation.Unit{
				UnitID: path, EntityType: "menu", EntityID: menuID, Path: path,
				ContainerType: translation.ContainerTypeBlock, ContainerID: item.ID,
				FieldName: "label", SourceText: label,
				SourceFormat: translation.SourceFormatPlainText, SourceLocale: sourceLocale,
				Context: stringPointer(strings.TrimSpace(string(item.LinkType))),
			})
		}
		collectTranslationUnits(menuID, sourceLocale, targetLocale, item.Children, units)
	}
}

func stringPointer(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	copy := value
	return &copy
}
