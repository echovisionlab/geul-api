package contentblock

import (
	"encoding/json"
	"sort"

	"github.com/google/uuid"
)

type aggregate struct {
	document Document
	blocks   map[uuid.UUID]FullBlock
	locales  map[uuid.UUID]map[string]json.RawMessage
}

func newAggregate(document Document) aggregate {
	return aggregate{
		document: document,
		blocks:   make(map[uuid.UUID]FullBlock),
		locales:  make(map[uuid.UUID]map[string]json.RawMessage),
	}
}

func (a aggregate) clone() aggregate {
	cloned := newAggregate(a.document)
	for id, block := range a.blocks {
		cloned.blocks[id] = cloneBlock(block)
	}
	for blockID, locales := range a.locales {
		cloned.locales[blockID] = make(map[string]json.RawMessage, len(locales))
		for locale, data := range locales {
			cloned.locales[blockID][locale] = append(json.RawMessage(nil), data...)
		}
	}
	return cloned
}

func cloneBlock(block FullBlock) FullBlock {
	cloned := block
	if block.ParentID != nil {
		parentID := *block.ParentID
		cloned.ParentID = &parentID
	}
	cloned.SharedData = append(json.RawMessage(nil), block.SharedData...)
	cloned.LocalizedData = append(json.RawMessage(nil), block.LocalizedData...)
	cloned.FileReferences = append([]FileReference(nil), block.FileReferences...)
	for index := range cloned.FileReferences {
		cloned.FileReferences[index].AllowedMIMETypes = append(
			[]string(nil),
			cloned.FileReferences[index].AllowedMIMETypes...,
		)
		cloned.FileReferences[index].AllowedMIMEPrefixes = append(
			[]string(nil),
			cloned.FileReferences[index].AllowedMIMEPrefixes...,
		)
	}
	return cloned
}

func (a aggregate) snapshot(sourceLocale string) Snapshot {
	ordered := orderedBlocks(a.blocks)
	blocks := make([]BaseBlock, 0, len(ordered))
	localeNames := make(map[string]struct{})
	for _, block := range ordered {
		blocks = append(blocks, baseBlock(block))
		for locale := range a.locales[block.ID] {
			localeNames[locale] = struct{}{}
		}
	}
	locales := make([]string, 0, len(localeNames))
	for locale := range localeNames {
		locales = append(locales, locale)
	}
	sort.Strings(locales)
	overlays := make([]LocaleOverlay, 0, len(locales))
	for _, locale := range locales {
		overlay := LocaleOverlay{Locale: locale}
		for _, block := range ordered {
			if data, exists := a.locales[block.ID][locale]; exists {
				overlay.Blocks = append(overlay.Blocks, LocaleBlockUpdate{
					BlockID:       block.ID,
					LocalizedData: append(json.RawMessage(nil), data...),
				})
			}
		}
		overlays = append(overlays, overlay)
	}
	return Snapshot{
		Document:       a.document,
		SourceLocale:   sourceLocale,
		Blocks:         blocks,
		LocaleOverlays: overlays,
	}
}

func (a aggregate) localizedBlocks(locale string) []FullBlock {
	blocks := orderedBlocks(a.blocks)
	for index := range blocks {
		blocks[index].LocalizedData = localizedData(a.locales, blocks[index].ID, locale)
	}
	return blocks
}

func baseBlock(block FullBlock) BaseBlock {
	base := block.BaseBlock
	if block.ParentID != nil {
		parentID := *block.ParentID
		base.ParentID = &parentID
	}
	base.SharedData = append(json.RawMessage(nil), block.SharedData...)
	return base
}

func localizedData(
	locales map[uuid.UUID]map[string]json.RawMessage,
	blockID uuid.UUID,
	locale string,
) json.RawMessage {
	if data := locales[blockID][locale]; len(data) > 0 {
		return append(json.RawMessage(nil), data...)
	}
	return json.RawMessage(`{}`)
}

func changedMutationLocales(before, after aggregate, mutation persistedMutation) []string {
	affectedBlocks := make(map[uuid.UUID]struct{}, len(mutation.upsertOrder)+len(mutation.deleteOrder)+len(mutation.localeMutations))
	for _, blockID := range mutation.upsertOrder {
		affectedBlocks[blockID] = struct{}{}
	}
	for _, blockID := range mutation.deleteOrder {
		affectedBlocks[blockID] = struct{}{}
	}
	for _, localeMutation := range mutation.localeMutations {
		affectedBlocks[localeMutation.blockID] = struct{}{}
	}

	localeNames := make(map[string]struct{})
	for blockID := range affectedBlocks {
		locales := before.locales[blockID]
		for locale := range locales {
			localeNames[locale] = struct{}{}
		}
		locales = after.locales[blockID]
		for locale := range locales {
			localeNames[locale] = struct{}{}
		}
	}
	locales := make([]string, 0, len(localeNames))
	for locale := range localeNames {
		locales = append(locales, locale)
	}
	sort.Strings(locales)

	changed := make([]string, 0, len(locales))
	for _, locale := range locales {
		for blockID := range affectedBlocks {
			beforeData, beforeExists := before.locales[blockID][locale]
			afterData, afterExists := after.locales[blockID][locale]
			if beforeExists != afterExists ||
				(beforeExists && !sameCanonicalJSON(beforeData, afterData)) {
				changed = append(changed, locale)
				break
			}
		}
	}
	return changed
}
