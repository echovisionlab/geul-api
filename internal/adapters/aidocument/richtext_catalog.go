package aidocumentadapter

import (
	"fmt"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

const (
	richTextContentField     core.FieldID = "content"
	richTextTableField       core.FieldID = "table"
	richTextTableLocaleField core.FieldID = "tableContent"
	richTextTableRowsField   core.FieldID = "rows"
	richTextTableCellsField  core.FieldID = "cells"
	richTextTableHeaderField core.FieldID = "header"
	richTextTableCellContent core.FieldID = "content"
)

// richTextCatalog projects the generated content descriptor into the generic
// DCDP validator. It never restates a concrete block catalog: every block,
// nested field, ownership rule, and stable array identity originates in the
// generated descriptor consumed by storage validation.
func richTextCatalog(descriptor contentv1.RichTextCatalogDescriptor) (core.Catalog, error) {
	catalog := core.Catalog{Fingerprint: descriptor.Fingerprint}
	for _, block := range descriptor.Blocks {
		kind := core.BlockKind(block.Kind)
		catalog.BlockKinds = append(catalog.BlockKinds, kind)
		for _, field := range block.Fields {
			rule, err := contentFieldRule(kind, field)
			if err != nil {
				return core.Catalog{}, fmt.Errorf("block %q field %q: %w", block.Kind, field.Name, err)
			}
			catalog.Fields = append(catalog.Fields, rule)
		}
		switch block.Content {
		case "none":
		case "inline":
			catalog.Fields = append(catalog.Fields, core.FieldRule{
				BlockKind: kind, Field: richTextContentField, ValueKind: core.ValueKindInline,
				Ownership: core.FieldOwnershipLocale, Translatable: true,
			})
		case "locale_text":
			catalog.Fields = append(catalog.Fields, core.FieldRule{
				BlockKind: kind, Field: richTextContentField, ValueKind: core.ValueKindText,
				Ownership: core.FieldOwnershipLocale, Translatable: true,
			})
		case "table":
			shared, localized, err := tableFieldRules(kind, descriptor.Table)
			if err != nil {
				return core.Catalog{}, err
			}
			catalog.Fields = append(catalog.Fields, shared, localized)
		default:
			return core.Catalog{}, fmt.Errorf("block %q has unsupported content shape %q", block.Kind, block.Content)
		}
	}
	return catalog, nil
}

func contentFieldRule(block core.BlockKind, field contentv1.ContentFieldDescriptor) (core.FieldRule, error) {
	schema, err := contentFieldSchema(field)
	if err != nil {
		return core.FieldRule{}, err
	}
	rule := core.FieldRule{
		BlockKind: block, Field: core.FieldID(field.Name), ValueKind: schema.Kind,
		Ownership: schema.Ownership, Translatable: schema.Translatable, File: schema.File,
	}
	if schema.Kind == core.ValueKindList || schema.Kind == core.ValueKindObject {
		rule.Schema = &schema
	}
	return rule, nil
}

func contentFieldSchema(field contentv1.ContentFieldDescriptor) (core.FieldSchema, error) {
	ownership, err := contentOwnership(field.Ownership)
	if err != nil {
		return core.FieldSchema{}, err
	}
	schema := core.FieldSchema{Ownership: ownership, Translatable: field.Translatable}
	switch field.Type {
	case "string", "uri", "uuid", "enum", "editor_color", "hex_color":
		schema.Kind = core.ValueKindText
	case "integer", "number", "enum_int":
		schema.Kind = core.ValueKindNumber
	case "boolean":
		schema.Kind = core.ValueKindBoolean
	case "file_attachment":
		schema.File = true
	case "array":
		schema.Kind = core.ValueKindList
		if field.Item == nil {
			return core.FieldSchema{}, fmt.Errorf("array descriptor has no item shape")
		}
		item, err := contentFieldSchema(*field.Item)
		if err != nil {
			return core.FieldSchema{}, err
		}
		schema.Item = &item
		schema.Identity, err = contentListIdentity(field.ItemIdentity)
		if err != nil {
			return core.FieldSchema{}, err
		}
	case "object":
		schema.Kind = core.ValueKindObject
		for _, field := range field.Fields {
			nested, err := contentFieldSchema(field)
			if err != nil {
				return core.FieldSchema{}, fmt.Errorf("nested field %q: %w", field.Name, err)
			}
			schema.Fields = append(schema.Fields, core.NestedFieldRule{Field: core.FieldID(field.Name), Schema: nested})
		}
	default:
		return core.FieldSchema{}, fmt.Errorf("unsupported generated field type %q", field.Type)
	}
	return schema, nil
}

func contentOwnership(value string) (core.FieldOwnership, error) {
	switch value {
	case "shared":
		return core.FieldOwnershipShared, nil
	case "source":
		return core.FieldOwnershipSource, nil
	case "locale":
		return core.FieldOwnershipLocale, nil
	default:
		return "", fmt.Errorf("unsupported generated ownership %q", value)
	}
}

func contentListIdentity(value *contentv1.ContentArrayItemIdentityDescriptor) (core.ListIdentityRule, error) {
	if value == nil {
		return core.ListIdentityRule{Kind: core.ListIdentityPositional}, nil
	}
	switch value.Strategy {
	case "position":
		return core.ListIdentityRule{Kind: core.ListIdentityPositional}, nil
	case "value":
		return core.ListIdentityRule{Kind: core.ListIdentityValue}, nil
	case "field":
		return core.ListIdentityRule{Kind: core.ListIdentityField, Field: core.FieldID(value.Field)}, nil
	case "fixed":
		identity := core.ListIdentityRule{Kind: core.ListIdentityFixed}
		for _, handle := range value.Values {
			identity.Handles = append(identity.Handles, core.RelationItemID(handle))
		}
		return identity, nil
	default:
		return core.ListIdentityRule{}, fmt.Errorf("unsupported generated item identity %q", value.Strategy)
	}
}

func tableFieldRules(block core.BlockKind, table contentv1.ContentTableDescriptor) (core.FieldRule, core.FieldRule, error) {
	sharedObject := core.FieldSchema{Kind: core.ValueKindObject, Ownership: core.FieldOwnershipShared}
	for _, field := range table.Fields {
		schema, err := contentFieldSchema(field)
		if err != nil {
			return core.FieldRule{}, core.FieldRule{}, err
		}
		sharedObject.Fields = append(sharedObject.Fields, core.NestedFieldRule{Field: core.FieldID(field.Name), Schema: schema})
	}
	cellObject := core.FieldSchema{Kind: core.ValueKindObject, Ownership: core.FieldOwnershipShared}
	cellObject.Fields = append(cellObject.Fields, core.NestedFieldRule{Field: richTextTableHeaderField, Schema: core.FieldSchema{Kind: core.ValueKindBoolean, Ownership: core.FieldOwnershipShared}})
	for _, field := range table.CellFields {
		schema, err := contentFieldSchema(field)
		if err != nil {
			return core.FieldRule{}, core.FieldRule{}, err
		}
		cellObject.Fields = append(cellObject.Fields, core.NestedFieldRule{Field: core.FieldID(field.Name), Schema: schema})
	}
	cellList := core.FieldSchema{Kind: core.ValueKindList, Ownership: core.FieldOwnershipShared, Item: &cellObject, Identity: core.ListIdentityRule{Kind: core.ListIdentityField, Field: core.FieldID(table.CellIdentity.Name)}}
	// The stable handle carries the generated row/cell UUID. Identity fields
	// remain present in each typed object so lossless round trips can be checked
	// against the generated row_id/cell_id contract.
	cellObject.Fields = append(cellObject.Fields, core.NestedFieldRule{Field: core.FieldID(table.CellIdentity.Name), Schema: core.FieldSchema{Kind: core.ValueKindText, Ownership: core.FieldOwnershipShared}})
	cellList.Item = &cellObject
	rowObject := core.FieldSchema{Kind: core.ValueKindObject, Ownership: core.FieldOwnershipShared, Fields: []core.NestedFieldRule{
		{Field: core.FieldID(table.RowIdentity.Name), Schema: core.FieldSchema{Kind: core.ValueKindText, Ownership: core.FieldOwnershipShared}},
		{Field: richTextTableCellsField, Schema: cellList},
	}}
	rowList := core.FieldSchema{Kind: core.ValueKindList, Ownership: core.FieldOwnershipShared, Item: &rowObject, Identity: core.ListIdentityRule{Kind: core.ListIdentityField, Field: core.FieldID(table.RowIdentity.Name)}}
	sharedObject.Fields = append(sharedObject.Fields, core.NestedFieldRule{Field: richTextTableRowsField, Schema: rowList})

	localeCellObject := core.FieldSchema{Kind: core.ValueKindObject, Ownership: core.FieldOwnershipLocale, Translatable: true, Fields: []core.NestedFieldRule{
		{Field: core.FieldID(table.CellIdentity.Name), Schema: core.FieldSchema{Kind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true}},
		{Field: richTextTableCellContent, Schema: core.FieldSchema{Kind: core.ValueKindInline, Ownership: core.FieldOwnershipLocale, Translatable: true}},
	}}
	localeCellList := core.FieldSchema{Kind: core.ValueKindList, Ownership: core.FieldOwnershipLocale, Translatable: true, Item: &localeCellObject, Identity: core.ListIdentityRule{Kind: core.ListIdentityField, Field: core.FieldID(table.CellIdentity.Name)}}
	localeRowObject := core.FieldSchema{Kind: core.ValueKindObject, Ownership: core.FieldOwnershipLocale, Translatable: true, Fields: []core.NestedFieldRule{
		{Field: core.FieldID(table.RowIdentity.Name), Schema: core.FieldSchema{Kind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true}},
		{Field: richTextTableCellsField, Schema: localeCellList},
	}}
	localeRows := core.FieldSchema{Kind: core.ValueKindList, Ownership: core.FieldOwnershipLocale, Translatable: true, Item: &localeRowObject, Identity: core.ListIdentityRule{Kind: core.ListIdentityField, Field: core.FieldID(table.RowIdentity.Name)}}
	localeObject := core.FieldSchema{Kind: core.ValueKindObject, Ownership: core.FieldOwnershipLocale, Translatable: true, Fields: []core.NestedFieldRule{{Field: richTextTableRowsField, Schema: localeRows}}}
	return core.FieldRule{BlockKind: block, Field: richTextTableField, ValueKind: core.ValueKindObject, Ownership: core.FieldOwnershipShared, Schema: &sharedObject},
		core.FieldRule{BlockKind: block, Field: richTextTableLocaleField, ValueKind: core.ValueKindObject, Ownership: core.FieldOwnershipLocale, Translatable: true, Schema: &localeObject}, nil
}
