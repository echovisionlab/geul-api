package aidocumentadapter

import (
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

func enrichPageCatalogConstraints(catalog *core.Catalog) {
	generated := contentv1.DescribePageCatalog()
	for index := range catalog.Fields {
		rule := &catalog.Fields[index]
		if rule.Schema == nil || (rule.Field != pageSectionDataField && rule.Field != pageSectionLocaleField) {
			continue
		}
		for _, section := range generated.Sections {
			kind := core.BlockKind(section.Kind)
			// Section names that collide with rich block names carry a Page prefix.
			if rule.BlockKind != kind && rule.BlockKind != "page-"+kind {
				continue
			}
			enrichPageProps(rule.Schema, section.Fields)
			if section.Kind == "immersive-scene" {
				for index := range rule.Schema.Fields {
					field := &rule.Schema.Fields[index]
					if field.Field == "units" && field.Schema.Item != nil {
						enrichPageProps(field.Schema.Item, generated.ImmersiveUnitFields)
					}
				}
			}
		}
	}
}

func enrichPageProps(schema *core.FieldSchema, fields []contentv1.ContentFieldDescriptor) {
	for index := range schema.Fields {
		nested := &schema.Fields[index]
		if nested.Field == "props" {
			enrichContentShape(&nested.Schema, fields)
		}
	}
}

func enrichContentShape(schema *core.FieldSchema, fields []contentv1.ContentFieldDescriptor) {
	for index := range schema.Fields {
		nested := &schema.Fields[index]
		for _, field := range fields {
			if nested.Field != core.FieldID(field.Name) {
				continue
			}
			applyContentConstraints(&nested.Schema, field)
			if nested.Schema.Item != nil && field.Item != nil {
				applyContentConstraints(nested.Schema.Item, *field.Item)
				enrichContentShape(nested.Schema.Item, field.Item.Fields)
			}
			enrichContentShape(&nested.Schema, field.Fields)
		}
	}
}
