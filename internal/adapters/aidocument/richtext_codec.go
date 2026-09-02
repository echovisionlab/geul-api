package aidocumentadapter

import (
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// RichTextCodec is the small public facade for the generated-catalog adapter
// shared by every DCDP domain backed by a Content Block Rich Text aggregate.
// Projection, operation compilation, typed value conversion, inline/table
// conversion, and protobuf reflection are private components in this package.
type RichTextCodec struct {
	profile    contentv1.RichTextProfile
	descriptor contentv1.RichTextCatalogDescriptor
	catalog    core.Catalog
	blocks     map[core.BlockKind]contentv1.ContentBlockDescriptor
	protoCases map[core.BlockKind]string
	fieldRules map[string]core.FieldRule
}

func NewRichTextCodec(profile contentv1.RichTextProfile) (*RichTextCodec, error) {
	descriptor, err := contentv1.DescribeRichTextCatalog(profile)
	if err != nil {
		return nil, err
	}
	catalog, err := richTextCatalog(descriptor)
	if err != nil {
		return nil, err
	}
	codec := &RichTextCodec{
		profile: profile, descriptor: descriptor, catalog: catalog,
		blocks:     make(map[core.BlockKind]contentv1.ContentBlockDescriptor, len(descriptor.Blocks)),
		protoCases: make(map[core.BlockKind]string, len(descriptor.Blocks)),
		fieldRules: make(map[string]core.FieldRule, len(catalog.Fields)),
	}
	for _, block := range descriptor.Blocks {
		kind := core.BlockKind(block.Kind)
		codec.blocks[kind] = block
		codec.protoCases[kind] = lowerCamelKind(block.Kind)
	}
	for _, rule := range catalog.Fields {
		codec.fieldRules[richTextFieldKey(rule.BlockKind, rule.Field)] = rule
	}
	return codec, nil
}

func (c *RichTextCodec) Catalog() core.Catalog { return c.catalog }

// Project returns exact requested-locale nodes. Missing locale Blocks stay
// absent; no source fallback is introduced at the DCDP boundary.
