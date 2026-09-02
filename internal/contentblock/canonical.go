package contentblock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

func snapshotDigest(state aggregate) (string, error) {
	blocks := make([]FullBlock, 0, len(state.blocks))
	for _, block := range state.blocks {
		blocks = append(blocks, block)
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].ID.String() < blocks[j].ID.String() })
	rows := make([]contentv1.ContentStorageRow, 0, len(blocks))
	for _, block := range blocks {
		sharedData, err := canonicalObject(block.SharedData)
		if err != nil {
			return "", fmt.Errorf("canonicalize Block %s shared data: %w", block.ID, err)
		}
		entry := contentv1.ContentStorageRow{
			BlockID:       block.ID.String(),
			ContainerSlot: block.ContainerSlot,
			Position:      int32(block.Position),
			Kind:          block.Kind,
			SharedData:    sharedData,
			Locales:       make([]contentv1.ContentStorageLocale, 0, len(state.locales[block.ID])),
		}
		if block.ParentID != nil {
			entry.ParentBlockID = block.ParentID.String()
		}
		locales := state.locales[block.ID]
		localeNames := make([]string, 0, len(locales))
		for locale := range locales {
			localeNames = append(localeNames, locale)
		}
		sort.Strings(localeNames)
		for _, locale := range localeNames {
			data, err := canonicalObject(locales[locale])
			if err != nil {
				return "", fmt.Errorf("canonicalize Block %s locale %s: %w", block.ID, locale, err)
			}
			entry.Locales = append(entry.Locales, contentv1.ContentStorageLocale{
				Locale:        locale,
				LocalizedData: data,
			})
		}
		rows = append(rows, entry)
	}
	return contentv1.ContentStorageCanonicalHash(state.document.Profile, rows)
}

func canonicalObject(data json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("must be a JSON object: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("must be a JSON object")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("must contain one JSON object")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode JSON object: %w", err)
	}
	return encoded, nil
}

func sameCanonicalJSON(left, right json.RawMessage) bool {
	leftCanonical, leftErr := canonicalObject(left)
	rightCanonical, rightErr := canonicalObject(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}

func orderedBlocks(blocks map[uuid.UUID]FullBlock) []FullBlock {
	result := make([]FullBlock, 0, len(blocks))
	for _, block := range blocks {
		result = append(result, cloneBlock(block))
	}
	sort.Slice(result, func(i, j int) bool {
		leftParent := ""
		rightParent := ""
		if result[i].ParentID != nil {
			leftParent = result[i].ParentID.String()
		}
		if result[j].ParentID != nil {
			rightParent = result[j].ParentID.String()
		}
		if leftParent != rightParent {
			return leftParent < rightParent
		}
		if result[i].ContainerSlot != result[j].ContainerSlot {
			return result[i].ContainerSlot < result[j].ContainerSlot
		}
		if result[i].Position != result[j].Position {
			return result[i].Position < result[j].Position
		}
		return result[i].ID.String() < result[j].ID.String()
	})
	return result
}
