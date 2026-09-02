package aidocumentadapter

import (
	"errors"
	"fmt"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// Project returns exact requested-locale nodes. Missing locale Blocks stay
// absent; no source fallback is introduced at the DCDP boundary.
func (c *RichTextCodec) Project(document *contentv1.LocalizedRichTextDocument) ([]core.Node, error) {
	if document == nil || document.GetBase() == nil || document.GetLocaleOverlay() == nil {
		return nil, errors.New("localized Rich Text document is required")
	}
	if document.GetProfile() != c.profile || document.GetBlockCatalogFingerprint() != c.catalog.Fingerprint {
		return nil, errors.New("localized Rich Text profile or catalog fingerprint mismatch")
	}
	localized := make(map[string]*contentv1.RichTextBlockLocale, len(document.GetLocaleOverlay().GetBlocks()))
	for _, block := range document.GetLocaleOverlay().GetBlocks() {
		localized[block.GetBlockId()] = block
	}
	nodes := make([]core.Node, 0, len(document.GetBase().GetNodes()))
	for _, stored := range document.GetBase().GetNodes() {
		if stored.GetBlock() == nil || stored.GetPlacement() == nil {
			return nil, errors.New("rich Text node requires block and placement")
		}
		kind, base, err := c.blockMessage(stored.GetBlock().ProtoReflect())
		if err != nil {
			return nil, err
		}
		description := c.blocks[kind]
		node := core.Node{ID: core.BlockID(stored.GetBlock().GetId()), Kind: kind, Parent: core.BlockID(stored.GetPlacement().GetParentBlockId()), Order: int(stored.GetPlacement().GetIndex())}
		shared, files, err := projectProps(base, description.Fields)
		if err != nil {
			return nil, fmt.Errorf("project block %q: %w", node.ID, err)
		}
		node.Shared, node.Files = shared, files
		if description.Content == "table" {
			contentField := findMessageField(base, "content")
			if contentField != nil && base.Has(contentField) {
				table, tableErr := projectBaseTable(base.Get(contentField).Message(), c.descriptor.Table)
				if tableErr != nil {
					return nil, fmt.Errorf("project block %q table: %w", node.ID, tableErr)
				}
				node.Shared = append(node.Shared, core.FieldValue{ID: richTextTableField, Value: table})
			}
		}
		if localeBlock := localized[stored.GetBlock().GetId()]; localeBlock != nil {
			localeKind, locale, localeErr := c.localeBlockMessage(localeBlock.ProtoReflect())
			if localeErr != nil || localeKind != kind {
				return nil, fmt.Errorf("project locale block %q: kind mismatch: %w", node.ID, localeErr)
			}
			node.Localized, err = projectLocaleMessage(locale, description, c.descriptor.Table)
			if err != nil {
				return nil, fmt.Errorf("project locale block %q: %w", node.ID, err)
			}
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// Compile applies a validated content-only batch to a clone, flattens it
// through the generated mutation contract, and returns the exact Store batch.
// Domain metadata operations must be removed by the owning registration.
