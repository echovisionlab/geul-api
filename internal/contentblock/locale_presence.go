package contentblock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
)

const (
	richTextContentField      = "content"
	richTextTableContentField = "tableContent"
)

// PresentRichTextLocaleValues projects only locale-owned leaf values that are
// actually persisted in locale. It deliberately reads sparse storage JSON,
// not a materialized source-fallback document, so explicit empty values remain
// distinguishable from absent values.
func PresentRichTextLocaleValues(snapshot Snapshot, locale string) ([]*managev1.AIDocumentFieldTarget, error) {
	return presentRichTextLocaleValues(snapshot, locale, false)
}

func presentRichTextLocaleValues(
	snapshot Snapshot,
	locale string,
	ignoreNonRichTextBlocks bool,
) ([]*managev1.AIDocumentFieldTarget, error) {
	profile, err := contentv1.ParseRichTextProfileStorageName(snapshot.Document.Profile)
	if err != nil {
		return nil, fmt.Errorf("parse Rich Text profile: %w", err)
	}
	catalog, err := contentv1.DescribeRichTextCatalog(profile)
	if err != nil {
		return nil, fmt.Errorf("describe Rich Text profile: %w", err)
	}
	blocks := make(map[string]contentv1.ContentBlockDescriptor, len(catalog.Blocks))
	for _, block := range catalog.Blocks {
		blocks[block.Kind] = block
	}
	baseKinds := make(map[string]string, len(snapshot.Blocks))
	for _, block := range snapshot.Blocks {
		baseKinds[block.ID.String()] = block.Kind
	}

	var targets []*managev1.AIDocumentFieldTarget
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
			description, ok := blocks[kind]
			if !ok {
				if ignoreNonRichTextBlocks {
					continue
				}
				return nil, fmt.Errorf("locale Block %s has unsupported kind %q", blockID, kind)
			}
			payload, err := richTextLocalePayload(stored.LocalizedData, kind)
			if err != nil {
				return nil, fmt.Errorf("locale Block %s: %w", blockID, err)
			}
			props, err := optionalJSONObject(payload, "props")
			if err != nil {
				return nil, fmt.Errorf("locale Block %s props: %w", blockID, err)
			}
			for _, field := range description.Fields {
				if field.Ownership != "locale" || field.Type == "object" || field.Type == "array" || field.Type == "file_attachment" {
					continue
				}
				if _, present := props[field.Name]; present {
					targets = append(targets, localeValueTarget(blockID, field.Name))
				}
			}
			switch description.Content {
			case "inline", "locale_text":
				if _, present := payload[richTextContentField]; present {
					targets = append(targets, localeValueTarget(blockID, richTextContentField))
				}
			case "table":
				tableTargets, tableErr := presentTableCellLocaleValues(blockID, payload)
				if tableErr != nil {
					return nil, fmt.Errorf("locale Block %s table: %w", blockID, tableErr)
				}
				targets = append(targets, tableTargets...)
			}
		}
	}

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

// RestoreRichTextAffectedLocaleValues validates the exact locale leaf targets
// declared by a collaboration writer and restores protobuf-default values that
// cannot otherwise survive proto JSON flattening (for example an authored
// empty Paragraph content list). The storage batch remains the mutation
// authority; targets that do not belong to one locale upsert are rejected.
func RestoreRichTextAffectedLocaleValues(
	profile contentv1.RichTextProfile,
	locale string,
	storage *contentv1.ContentStorageMutationBatch,
	values []*managev1.AIDocumentFieldTarget,
) error {
	if storage == nil {
		return fmt.Errorf("storage batch is required")
	}
	catalog, err := contentv1.DescribeRichTextCatalog(profile)
	if err != nil {
		return fmt.Errorf("describe Rich Text profile: %w", err)
	}
	blocks := make(map[string]contentv1.ContentBlockDescriptor, len(catalog.Blocks))
	for _, block := range catalog.Blocks {
		blocks[block.Kind] = block
	}
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
	touched := make(map[string]*contentv1.ContentStorageLocaleUpsert)
	for index, value := range values {
		canonical, key, err := validateRichTextLocaleValueTarget(blocks, upserts, value)
		if err != nil {
			return fmt.Errorf("target %d: %w", index, err)
		}
		if index != 0 && key <= previousKey {
			return fmt.Errorf("targets must be canonical-sorted and duplicate-free")
		}
		previousKey = key
		upsert := upserts[canonical.GetBlockHandle()]
		if err := restoreRichTextLocaleValue(upsert, blocks[upsert.ExpectedKind], canonical); err != nil {
			return fmt.Errorf("target %d: %w", index, err)
		}
		touched[upsert.BlockID] = upsert
	}
	for _, upsert := range touched {
		normalized, err := contentv1.NormalizeContentStorageLocale(
			catalog.Profile,
			upsert.ExpectedKind,
			upsert.LocalizedData,
			contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
		)
		if err != nil {
			return fmt.Errorf("normalize locale Block %s: %w", upsert.BlockID, err)
		}
		upsert.LocalizedData = normalized
	}
	return nil
}

func validateRichTextLocaleValueTarget(
	blocks map[string]contentv1.ContentBlockDescriptor,
	upserts map[string]*contentv1.ContentStorageLocaleUpsert,
	value *managev1.AIDocumentFieldTarget,
) (*managev1.AIDocumentFieldTarget, string, error) {
	if value == nil {
		return nil, "", fmt.Errorf("target is required")
	}
	blockID := value.GetBlockHandle()
	parsed, err := uuid.Parse(blockID)
	if err != nil || parsed.String() != blockID {
		return nil, "", fmt.Errorf("block handle must be a canonical UUID")
	}
	upsert := upserts[blockID]
	if upsert == nil {
		return nil, "", fmt.Errorf("block %s has no locale upsert", blockID)
	}
	description, ok := blocks[upsert.ExpectedKind]
	if !ok {
		return nil, "", fmt.Errorf("block %s has unsupported kind %q", blockID, upsert.ExpectedKind)
	}
	field := strings.TrimSpace(value.GetFieldHandle())
	if field == "" {
		return nil, "", fmt.Errorf("field handle is required")
	}
	path := make([]*managev1.AIDocumentFieldPathSegment, 0, len(value.GetPath()))
	for _, segment := range value.GetPath() {
		if segment == nil {
			return nil, "", fmt.Errorf("path segment is required")
		}
		switch selector := segment.GetSelector().(type) {
		case *managev1.AIDocumentFieldPathSegment_FieldHandle:
			if strings.TrimSpace(selector.FieldHandle) == "" {
				return nil, "", fmt.Errorf("path field handle is required")
			}
			path = append(path, fieldPath(selector.FieldHandle))
		case *managev1.AIDocumentFieldPathSegment_ItemHandle:
			if strings.TrimSpace(selector.ItemHandle) == "" {
				return nil, "", fmt.Errorf("path item handle is required")
			}
			path = append(path, itemPath(selector.ItemHandle))
		default:
			return nil, "", fmt.Errorf("path selector is required")
		}
	}
	canonical := localeValueTarget(blockID, field, path...)
	if err := validateRichTextLocaleValueShape(description, canonical); err != nil {
		return nil, "", err
	}
	return canonical, localeValueTargetKey(canonical), nil
}

func validateRichTextLocaleValueShape(
	description contentv1.ContentBlockDescriptor,
	target *managev1.AIDocumentFieldTarget,
) error {
	field := target.GetFieldHandle()
	switch field {
	case richTextContentField:
		if len(target.GetPath()) != 0 || (description.Content != "inline" && description.Content != "locale_text") {
			return fmt.Errorf("content is not a locale leaf for kind %s", description.Kind)
		}
		return nil
	case richTextTableContentField:
		if description.Content != "table" || !isTableCellContentPath(target.GetPath()) {
			return fmt.Errorf("table content path is invalid for kind %s", description.Kind)
		}
		return nil
	}
	if len(target.GetPath()) != 0 {
		return fmt.Errorf("scalar locale field %s cannot carry a path", field)
	}
	for _, candidate := range description.Fields {
		if candidate.Name != field {
			continue
		}
		if candidate.Ownership != "locale" || candidate.Type == "object" || candidate.Type == "array" || candidate.Type == "file_attachment" {
			return fmt.Errorf("field %s is not a locale scalar leaf", field)
		}
		return nil
	}
	return fmt.Errorf("field %s is not declared by kind %s", field, description.Kind)
}

func isTableCellContentPath(path []*managev1.AIDocumentFieldPathSegment) bool {
	if len(path) != 5 {
		return false
	}
	return path[0].GetFieldHandle() == "rows" && path[1].GetItemHandle() != "" &&
		path[2].GetFieldHandle() == "cells" && path[3].GetItemHandle() != "" &&
		path[4].GetFieldHandle() == richTextContentField
}

func restoreRichTextLocaleValue(
	upsert *contentv1.ContentStorageLocaleUpsert,
	description contentv1.ContentBlockDescriptor,
	target *managev1.AIDocumentFieldTarget,
) error {
	root, err := decodeJSONAnyObject(upsert.LocalizedData)
	if err != nil {
		return err
	}
	payload, ok := root[protoJSONKind(upsert.ExpectedKind)].(map[string]any)
	if !ok {
		return fmt.Errorf("locale payload kind %s is required", upsert.ExpectedKind)
	}
	switch target.GetFieldHandle() {
	case richTextContentField:
		if _, present := payload[richTextContentField]; !present {
			if description.Content == "inline" {
				payload[richTextContentField] = []any{}
			} else {
				payload[richTextContentField] = ""
			}
		}
	case richTextTableContentField:
		if err := restoreTableCellLocaleValue(payload, target.GetPath()); err != nil {
			return err
		}
	default:
		props, ok := payload["props"].(map[string]any)
		if !ok {
			return fmt.Errorf("locale props are required")
		}
		if _, present := props[target.GetFieldHandle()]; !present {
			return fmt.Errorf("locale field %s is missing from mutation", target.GetFieldHandle())
		}
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return err
	}
	upsert.LocalizedData = encoded
	return nil
}

func restoreTableCellLocaleValue(
	payload map[string]any,
	path []*managev1.AIDocumentFieldPathSegment,
) error {
	content, ok := payload[richTextContentField].(map[string]any)
	if !ok {
		return fmt.Errorf("table locale content is required")
	}
	rows, ok := content["rows"].([]any)
	if !ok {
		return fmt.Errorf("table locale rows are required")
	}
	rowID := path[1].GetItemHandle()
	cellID := path[3].GetItemHandle()
	for _, rawRow := range rows {
		row, _ := rawRow.(map[string]any)
		if row["rowId"] != rowID {
			continue
		}
		cells, ok := row["cells"].([]any)
		if !ok {
			return fmt.Errorf("table locale row %s cells are required", rowID)
		}
		for _, rawCell := range cells {
			cell, _ := rawCell.(map[string]any)
			if cell["cellId"] != cellID {
				continue
			}
			if _, present := cell[richTextContentField]; !present {
				cell[richTextContentField] = []any{}
			}
			return nil
		}
		return fmt.Errorf("table locale cell %s is missing", cellID)
	}
	return fmt.Errorf("table locale row %s is missing", rowID)
}

func decodeJSONAnyObject(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, fmt.Errorf("must be a JSON object: %w", err)
	}
	if object == nil {
		return nil, fmt.Errorf("must be a JSON object")
	}
	return object, nil
}

func richTextLocalePayload(data json.RawMessage, kind string) (map[string]json.RawMessage, error) {
	root, err := decodeJSONObject(data)
	if err != nil {
		return nil, err
	}
	expected := protoJSONKind(kind)
	if len(root) != 1 {
		return nil, fmt.Errorf("must contain exactly one Block kind")
	}
	raw, ok := root[expected]
	if !ok {
		return nil, fmt.Errorf("kind is %q, want %q", firstJSONKey(root), expected)
	}
	return decodeJSONObject(raw)
}

func presentTableCellLocaleValues(blockID string, payload map[string]json.RawMessage) ([]*managev1.AIDocumentFieldTarget, error) {
	rawContent, present := payload[richTextContentField]
	if !present {
		return nil, nil
	}
	content, err := decodeJSONObject(rawContent)
	if err != nil {
		return nil, err
	}
	rawRows, present := content["rows"]
	if !present {
		return nil, nil
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(rawRows, &rows); err != nil {
		return nil, fmt.Errorf("rows must be an array: %w", err)
	}
	type tableCell struct {
		identity string
		content  bool
	}
	type tableRow struct {
		identity string
		cells    []tableCell
	}
	parsedRows := make([]tableRow, 0, len(rows))
	rowIdentities := tableIdentityState{}
	cellIdentities := tableIdentityState{}
	for _, rawRow := range rows {
		row, err := decodeJSONObject(rawRow)
		if err != nil {
			return nil, fmt.Errorf("row: %w", err)
		}
		rowID, durable, err := tableIdentity(row, "rowId")
		if err != nil {
			return nil, err
		}
		rowIdentities.observe(durable)
		rawCells, ok := row["cells"]
		if !ok {
			return nil, fmt.Errorf("table row cells are required")
		}
		var cells []json.RawMessage
		if err := json.Unmarshal(rawCells, &cells); err != nil {
			return nil, fmt.Errorf("table row cells must be an array: %w", err)
		}
		parsed := tableRow{identity: rowID, cells: make([]tableCell, 0, len(cells))}
		for _, rawCell := range cells {
			cell, err := decodeJSONObject(rawCell)
			if err != nil {
				return nil, fmt.Errorf("table row cell: %w", err)
			}
			cellID, durable, err := tableIdentity(cell, "cellId")
			if err != nil {
				return nil, err
			}
			cellIdentities.observe(durable)
			_, contentPresent := cell[richTextContentField]
			parsed.cells = append(parsed.cells, tableCell{identity: cellID, content: contentPresent})
		}
		parsedRows = append(parsedRows, parsed)
	}
	allIdentities := tableIdentityState{
		durableCount: rowIdentities.durableCount + cellIdentities.durableCount,
		legacyCount:  rowIdentities.legacyCount + cellIdentities.legacyCount,
	}
	if allIdentities.mixed() {
		return nil, fmt.Errorf("table locale contains partially migrated durable identities")
	}
	// Legacy table payloads predate durable row/cell identities. The Web codec
	// pairs them positionally and writes stable identities on the first table
	// edit. Until then there is no canonical path that can safely identify an
	// explicit cell value, so omit only those table presence targets.
	if allIdentities.legacy() {
		return nil, nil
	}
	var targets []*managev1.AIDocumentFieldTarget
	for _, row := range parsedRows {
		for _, cell := range row.cells {
			if !cell.content {
				continue
			}
			targets = append(targets, localeValueTarget(blockID, richTextTableContentField,
				fieldPath("rows"), itemPath(row.identity), fieldPath("cells"), itemPath(cell.identity), fieldPath(richTextContentField),
			))
		}
	}
	return targets, nil
}

type tableIdentityState struct {
	durableCount int
	legacyCount  int
}

func (state *tableIdentityState) observe(durable bool) {
	if durable {
		state.durableCount++
	} else {
		state.legacyCount++
	}
}

func (state tableIdentityState) mixed() bool {
	return state.durableCount != 0 && state.legacyCount != 0
}

func (state tableIdentityState) legacy() bool {
	return state.legacyCount != 0 && state.durableCount == 0
}

func tableIdentity(object map[string]json.RawMessage, field string) (string, bool, error) {
	raw, present := object[field]
	if !present || string(raw) == "null" {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, fmt.Errorf("%s must be a string", field)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false, nil
	}
	return value, true, nil
}

func localeValueTarget(blockID, field string, path ...*managev1.AIDocumentFieldPathSegment) *managev1.AIDocumentFieldTarget {
	return &managev1.AIDocumentFieldTarget{
		Owner:       &managev1.AIDocumentFieldTarget_BlockHandle{BlockHandle: blockID},
		FieldHandle: field,
		Path:        path,
	}
}

func fieldPath(field string) *managev1.AIDocumentFieldPathSegment {
	return &managev1.AIDocumentFieldPathSegment{
		Selector: &managev1.AIDocumentFieldPathSegment_FieldHandle{FieldHandle: field},
	}
}

func itemPath(item string) *managev1.AIDocumentFieldPathSegment {
	return &managev1.AIDocumentFieldPathSegment{
		Selector: &managev1.AIDocumentFieldPathSegment_ItemHandle{ItemHandle: item},
	}
}

func localeValueTargetKey(target *managev1.AIDocumentFieldTarget) string {
	path := make([][2]string, 0, len(target.GetPath()))
	for _, segment := range target.GetPath() {
		switch selector := segment.GetSelector().(type) {
		case *managev1.AIDocumentFieldPathSegment_FieldHandle:
			path = append(path, [2]string{"field", selector.FieldHandle})
		case *managev1.AIDocumentFieldPathSegment_ItemHandle:
			path = append(path, [2]string{"item", selector.ItemHandle})
		}
	}
	encoded, _ := json.Marshal([]any{target.GetBlockHandle(), target.GetFieldHandle(), path})
	return string(encoded)
}

func decodeJSONObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil {
		return nil, fmt.Errorf("must be a JSON object: %w", err)
	}
	if object == nil {
		return nil, fmt.Errorf("must be a JSON object")
	}
	return object, nil
}

func optionalJSONObject(object map[string]json.RawMessage, field string) (map[string]json.RawMessage, error) {
	raw, ok := object[field]
	if !ok {
		return map[string]json.RawMessage{}, nil
	}
	return decodeJSONObject(raw)
}

func requiredJSONString(object map[string]json.RawMessage, field string) (string, error) {
	raw, ok := object[field]
	if !ok {
		return "", fmt.Errorf("%s is required", field)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must be a nonblank string", field)
	}
	return value, nil
}

func protoJSONKind(kind string) string {
	parts := strings.Split(kind, "-")
	var result strings.Builder
	result.WriteString(parts[0])
	for _, part := range parts[1:] {
		if part == "" {
			continue
		}
		result.WriteString(strings.ToUpper(part[:1]))
		result.WriteString(part[1:])
	}
	return result.String()
}

func firstJSONKey(object map[string]json.RawMessage) string {
	for key := range object {
		return key
	}
	return ""
}
