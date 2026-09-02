package contentblock

import (
	"encoding/json"
	"fmt"
	"sort"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
)

const pageLocaleDataField = "locale-data"

// PresentPageLocaleValues projects every exact locale-owned leaf persisted by
// a Page: nested Rich Text values, Section props, and immersive unit props.
// It reads sparse storage rather than the source-fallback resident document so
// an explicit empty value remains distinguishable from an absent value.
func PresentPageLocaleValues(snapshot Snapshot, locale string) ([]*managev1.AIDocumentFieldTarget, error) {
	if snapshot.Document.Profile != "page" {
		return nil, fmt.Errorf("page locale presence requires page profile, got %q", snapshot.Document.Profile)
	}
	targets, err := presentRichTextLocaleValues(snapshot, locale, true)
	if err != nil {
		return nil, err
	}
	sections, immersiveFields := pageLocaleCatalog()
	baseKinds := make(map[string]string, len(snapshot.Blocks))
	baseImmersiveUnits := make(map[string]map[string]struct{})
	for _, block := range snapshot.Blocks {
		blockID := block.ID.String()
		baseKinds[blockID] = block.Kind
		if block.Kind != "immersive-scene" {
			continue
		}
		units, unitsErr := pageImmersiveUnitIDs(block.SharedData, block.Kind, "id")
		if unitsErr != nil {
			return nil, fmt.Errorf("page section %s base units: %w", blockID, unitsErr)
		}
		baseImmersiveUnits[blockID] = units
	}
	for _, overlay := range snapshot.LocaleOverlays {
		if overlay.Locale != locale {
			continue
		}
		for _, stored := range overlay.Blocks {
			blockID := stored.BlockID.String()
			kind, ok := baseKinds[blockID]
			if !ok {
				return nil, fmt.Errorf("locale Block %s has no base Block", blockID)
			}
			section, ok := sections[kind]
			if !ok {
				continue
			}
			payload, payloadErr := richTextLocalePayload(stored.LocalizedData, kind)
			if payloadErr != nil {
				return nil, fmt.Errorf("page section %s locale payload: %w", blockID, payloadErr)
			}
			props, propsErr := optionalJSONObject(payload, "props")
			if propsErr != nil {
				return nil, fmt.Errorf("page section %s locale props: %w", blockID, propsErr)
			}
			for _, field := range pageScalarLocaleFields(section.Fields) {
				if _, present := props[field.Name]; present {
					targets = append(targets, pageSectionLocaleValueTarget(blockID, field.Name))
				}
			}
			if kind != "immersive-scene" {
				continue
			}
			unitTargets, unitsErr := presentPageImmersiveUnitLocaleValues(
				blockID, payload, baseImmersiveUnits[blockID], immersiveFields,
			)
			if unitsErr != nil {
				return nil, fmt.Errorf("page section %s locale units: %w", blockID, unitsErr)
			}
			targets = append(targets, unitTargets...)
		}
	}
	return canonicalSortedLocaleValueTargets(targets)
}

// RestorePageAffectedLocaleValues validates every exact Page locale leaf
// declared by a collaboration writer and restores protobuf-default values that
// disappear from flattened mutation JSON.
func RestorePageAffectedLocaleValues(
	locale string,
	storage *contentv1.ContentStorageMutationBatch,
	values []*managev1.AIDocumentFieldTarget,
) error {
	if storage == nil {
		return fmt.Errorf("storage batch is required")
	}
	sections, immersiveFields := pageLocaleCatalog()
	upserts := make(map[string]*contentv1.ContentStorageLocaleUpsert)
	for groupIndex := range storage.LocaleGroups {
		group := &storage.LocaleGroups[groupIndex]
		if group.Locale != locale {
			continue
		}
		for upsertIndex := range group.Upserts {
			upsert := &group.Upserts[upsertIndex]
			if _, exists := upserts[upsert.BlockID]; exists {
				return fmt.Errorf("duplicate locale upsert for Block %s", upsert.BlockID)
			}
			upserts[upsert.BlockID] = upsert
		}
	}

	var previousKey string
	var richTextValues []*managev1.AIDocumentFieldTarget
	touchedSections := make(map[string]*contentv1.ContentStorageLocaleUpsert)
	for index, value := range values {
		if value == nil {
			return fmt.Errorf("target %d: target is required", index)
		}
		key := localeValueTargetKey(value)
		if index != 0 && key <= previousKey {
			return fmt.Errorf("targets must be canonical-sorted and duplicate-free")
		}
		previousKey = key
		upsert := upserts[value.GetBlockHandle()]
		if upsert == nil {
			return fmt.Errorf("target %d: Block %s has no locale upsert", index, value.GetBlockHandle())
		}
		section, isPageSection := sections[upsert.ExpectedKind]
		if !isPageSection {
			richTextValues = append(richTextValues, value)
			continue
		}
		canonical, canonicalKey, field, unitID, err := validatePageLocaleValueTarget(
			upsert, section, immersiveFields, value,
		)
		if err != nil {
			return fmt.Errorf("target %d: %w", index, err)
		}
		if canonicalKey != key {
			return fmt.Errorf("target %d is not canonical", index)
		}
		if err := restorePageLocaleValue(upsert, canonical, field, unitID); err != nil {
			return fmt.Errorf("target %d: %w", index, err)
		}
		touchedSections[upsert.BlockID] = upsert
	}
	if err := RestoreRichTextAffectedLocaleValues(
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_PAGE,
		locale,
		storage,
		richTextValues,
	); err != nil {
		return err
	}
	for _, upsert := range touchedSections {
		normalized, err := contentv1.NormalizeContentStorageLocale(
			"page",
			upsert.ExpectedKind,
			upsert.LocalizedData,
			contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
		)
		if err != nil {
			return fmt.Errorf("normalize Page Section %s locale: %w", upsert.BlockID, err)
		}
		upsert.LocalizedData = normalized
	}
	return nil
}

func pageLocaleCatalog() (
	map[string]contentv1.PageSectionDescriptor,
	map[string]contentv1.ContentFieldDescriptor,
) {
	descriptor := contentv1.DescribePageCatalog()
	sections := make(map[string]contentv1.PageSectionDescriptor, len(descriptor.Sections))
	for _, section := range descriptor.Sections {
		sections[section.Kind] = section
	}
	immersiveFields := make(map[string]contentv1.ContentFieldDescriptor, len(descriptor.ImmersiveUnitFields))
	for _, field := range descriptor.ImmersiveUnitFields {
		immersiveFields[field.Name] = field
	}
	return sections, immersiveFields
}

func pageScalarLocaleFields(fields []contentv1.ContentFieldDescriptor) []contentv1.ContentFieldDescriptor {
	result := make([]contentv1.ContentFieldDescriptor, 0, len(fields))
	for _, field := range fields {
		if field.Ownership == "locale" && field.Type != "object" && field.Type != "array" && field.Type != "file_attachment" {
			result = append(result, field)
		}
	}
	return result
}

func presentPageImmersiveUnitLocaleValues(
	blockID string,
	payload map[string]json.RawMessage,
	baseUnits map[string]struct{},
	fields map[string]contentv1.ContentFieldDescriptor,
) ([]*managev1.AIDocumentFieldTarget, error) {
	rawUnits, present := payload["units"]
	if !present {
		return nil, nil
	}
	var units []json.RawMessage
	if err := json.Unmarshal(rawUnits, &units); err != nil {
		return nil, fmt.Errorf("units must be an array: %w", err)
	}
	var targets []*managev1.AIDocumentFieldTarget
	for _, rawUnit := range units {
		unit, err := decodeJSONObject(rawUnit)
		if err != nil {
			return nil, err
		}
		unitID, err := requiredCanonicalUUIDString(unit, "unitId")
		if err != nil {
			return nil, err
		}
		if _, exists := baseUnits[unitID]; !exists {
			return nil, fmt.Errorf("unit %s has no base unit", unitID)
		}
		props, err := optionalJSONObject(unit, "props")
		if err != nil {
			return nil, fmt.Errorf("unit %s props: %w", unitID, err)
		}
		for _, field := range pageScalarLocaleFieldMap(fields) {
			if _, present := props[field.Name]; present {
				targets = append(targets, pageImmersiveLocaleValueTarget(blockID, unitID, field.Name))
			}
		}
	}
	return targets, nil
}

func pageScalarLocaleFieldMap(fields map[string]contentv1.ContentFieldDescriptor) []contentv1.ContentFieldDescriptor {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]contentv1.ContentFieldDescriptor, 0, len(names))
	for _, name := range names {
		field := fields[name]
		if field.Ownership == "locale" && field.Type != "object" && field.Type != "array" && field.Type != "file_attachment" {
			result = append(result, field)
		}
	}
	return result
}

func pageImmersiveUnitIDs(data json.RawMessage, kind, identityField string) (map[string]struct{}, error) {
	root, err := decodeJSONObject(data)
	if err != nil {
		return nil, err
	}
	expected := protoJSONKind(kind)
	rawPayload, ok := root[expected]
	if !ok {
		return nil, fmt.Errorf("kind is %q, want %q", firstJSONKey(root), expected)
	}
	payload, err := decodeJSONObject(rawPayload)
	if err != nil {
		return nil, err
	}
	rawUnits, present := payload["units"]
	if !present {
		return map[string]struct{}{}, nil
	}
	var units []json.RawMessage
	if err := json.Unmarshal(rawUnits, &units); err != nil {
		return nil, fmt.Errorf("units must be an array: %w", err)
	}
	result := make(map[string]struct{}, len(units))
	for _, rawUnit := range units {
		unit, err := decodeJSONObject(rawUnit)
		if err != nil {
			return nil, err
		}
		unitID, err := requiredCanonicalUUIDString(unit, identityField)
		if err != nil {
			return nil, err
		}
		if _, duplicate := result[unitID]; duplicate {
			return nil, fmt.Errorf("duplicate unit %s", unitID)
		}
		result[unitID] = struct{}{}
	}
	return result, nil
}

func validatePageLocaleValueTarget(
	upsert *contentv1.ContentStorageLocaleUpsert,
	section contentv1.PageSectionDescriptor,
	immersiveFields map[string]contentv1.ContentFieldDescriptor,
	value *managev1.AIDocumentFieldTarget,
) (*managev1.AIDocumentFieldTarget, string, contentv1.ContentFieldDescriptor, string, error) {
	blockID := value.GetBlockHandle()
	parsed, err := uuid.Parse(blockID)
	if err != nil || parsed.String() != blockID {
		return nil, "", contentv1.ContentFieldDescriptor{}, "", fmt.Errorf("block handle must be a canonical UUID")
	}
	if value.GetFieldHandle() != pageLocaleDataField {
		return nil, "", contentv1.ContentFieldDescriptor{}, "", fmt.Errorf("page locale field handle must be %q", pageLocaleDataField)
	}
	path := value.GetPath()
	if len(path) == 2 && path[0].GetFieldHandle() == "props" && path[1].GetFieldHandle() != "" {
		field, ok := pageLocaleScalarField(section.Fields, path[1].GetFieldHandle())
		if !ok {
			return nil, "", contentv1.ContentFieldDescriptor{}, "", fmt.Errorf("field %s is not a Page Section locale scalar leaf", path[1].GetFieldHandle())
		}
		canonical := pageSectionLocaleValueTarget(blockID, field.Name)
		return canonical, localeValueTargetKey(canonical), field, "", nil
	}
	if section.Kind != "immersive-scene" || len(path) != 4 || path[0].GetFieldHandle() != "units" || path[1].GetItemHandle() == "" || path[2].GetFieldHandle() != "props" || path[3].GetFieldHandle() == "" {
		return nil, "", contentv1.ContentFieldDescriptor{}, "", fmt.Errorf("page locale value path is invalid for kind %s", upsert.ExpectedKind)
	}
	unitID := path[1].GetItemHandle()
	parsedUnitID, err := uuid.Parse(unitID)
	if err != nil || parsedUnitID.String() != unitID {
		return nil, "", contentv1.ContentFieldDescriptor{}, "", fmt.Errorf("page immersive unit handle must be a canonical UUID")
	}
	field, ok := immersiveFields[path[3].GetFieldHandle()]
	if !ok || !isPageLocaleScalar(field) {
		return nil, "", contentv1.ContentFieldDescriptor{}, "", fmt.Errorf("field %s is not a Page immersive locale scalar leaf", path[3].GetFieldHandle())
	}
	canonical := pageImmersiveLocaleValueTarget(blockID, unitID, field.Name)
	return canonical, localeValueTargetKey(canonical), field, unitID, nil
}

func restorePageLocaleValue(
	upsert *contentv1.ContentStorageLocaleUpsert,
	target *managev1.AIDocumentFieldTarget,
	field contentv1.ContentFieldDescriptor,
	unitID string,
) error {
	root, err := decodeJSONAnyObject(upsert.LocalizedData)
	if err != nil {
		return err
	}
	payload, ok := root[protoJSONKind(upsert.ExpectedKind)].(map[string]any)
	if !ok {
		return fmt.Errorf("locale payload kind %s is required", upsert.ExpectedKind)
	}
	if unitID == "" {
		props, ok := payload["props"].(map[string]any)
		if !ok {
			return fmt.Errorf("page section locale props are required")
		}
		if _, present := props[field.Name]; !present {
			if !field.HasDefault {
				return fmt.Errorf("page section locale field %s is missing from mutation", field.Name)
			}
			props[field.Name] = field.Default
		}
	} else {
		units, ok := payload["units"].([]any)
		if !ok {
			return fmt.Errorf("page immersive locale units are required")
		}
		found := false
		for _, rawUnit := range units {
			unit, _ := rawUnit.(map[string]any)
			if unit["unitId"] != unitID {
				continue
			}
			props, ok := unit["props"].(map[string]any)
			if !ok {
				return fmt.Errorf("page immersive unit %s locale props are required", unitID)
			}
			if _, present := props[field.Name]; !present {
				if !field.HasDefault {
					return fmt.Errorf("page immersive unit locale field %s is missing from mutation", field.Name)
				}
				props[field.Name] = field.Default
			}
			found = true
			break
		}
		if !found {
			return fmt.Errorf("page immersive locale unit %s is missing", unitID)
		}
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return err
	}
	upsert.LocalizedData = encoded
	return nil
}

func pageLocaleScalarField(fields []contentv1.ContentFieldDescriptor, name string) (contentv1.ContentFieldDescriptor, bool) {
	for _, field := range fields {
		if field.Name == name && isPageLocaleScalar(field) {
			return field, true
		}
	}
	return contentv1.ContentFieldDescriptor{}, false
}

func isPageLocaleScalar(field contentv1.ContentFieldDescriptor) bool {
	return field.Ownership == "locale" && field.Type != "object" && field.Type != "array" && field.Type != "file_attachment"
}

func pageSectionLocaleValueTarget(blockID, field string) *managev1.AIDocumentFieldTarget {
	return localeValueTarget(blockID, pageLocaleDataField, fieldPath("props"), fieldPath(field))
}

func pageImmersiveLocaleValueTarget(blockID, unitID, field string) *managev1.AIDocumentFieldTarget {
	return localeValueTarget(
		blockID,
		pageLocaleDataField,
		fieldPath("units"), itemPath(unitID), fieldPath("props"), fieldPath(field),
	)
}

func canonicalSortedLocaleValueTargets(targets []*managev1.AIDocumentFieldTarget) ([]*managev1.AIDocumentFieldTarget, error) {
	sort.Slice(targets, func(left, right int) bool {
		return localeValueTargetKey(targets[left]) < localeValueTargetKey(targets[right])
	})
	for index := 1; index < len(targets); index++ {
		if localeValueTargetKey(targets[index-1]) == localeValueTargetKey(targets[index]) {
			return nil, fmt.Errorf("duplicate persisted locale value target %s", localeValueTargetKey(targets[index]))
		}
	}
	return targets, nil
}

func requiredCanonicalUUIDString(object map[string]json.RawMessage, field string) (string, error) {
	value, err := requiredJSONString(object, field)
	if err != nil {
		return "", err
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return "", fmt.Errorf("%s must be a canonical UUID", field)
	}
	return parsed.String(), nil
}
