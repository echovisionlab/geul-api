package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func translationStructuredResult(encoded []byte, err error) (mcpserver.ToolResult, error) {
	if err != nil {
		return mcpserver.ToolResult{}, err
	}
	return structuredResult(encoded, false)
}

func compactTimestamp(value *timestamppb.Timestamp) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if err := value.CheckValid(); err != nil {
		return nil, fmt.Errorf("translation application returned an invalid timestamp: %w", err)
	}
	formatted := value.AsTime().UTC().Format(time.RFC3339Nano)
	return &formatted, nil
}

func encodeTranslationEntries(target *managev1.TranslationTarget, sourceLocale string, entries []*managev1.TranslationEntry) ([]byte, error) {
	if err := validateCompactLocale(core.Locale(sourceLocale)); err != nil {
		return nil, fmt.Errorf("translation application returned invalid source locale: %w", err)
	}
	compact := make([][2]any, 0, len(entries))
	for _, entry := range entries {
		metadata, err := compactTranslationEntry(target, entry)
		if err != nil {
			return nil, err
		}
		compact = append(compact, [2]any{metadata.Locale, metadata.UpdatedAt})
	}
	return json.Marshal(map[string]any{"s": sourceLocale, "e": compact})
}

type compactEntry struct {
	Profile   string  `json:"p"`
	Document  string  `json:"d"`
	Locale    string  `json:"l"`
	UpdatedAt *string `json:"u,omitempty"`
}

func compactTranslationEntry(expected *managev1.TranslationTarget, entry *managev1.TranslationEntry) (compactEntry, error) {
	if entry == nil || entry.Target == nil || expected == nil ||
		entry.Target.EntityType != expected.EntityType || entry.Target.EntityId != expected.EntityId {
		return compactEntry{}, errors.New("translation application returned a different locale target")
	}
	profile, ok := translationProfile(entry.Target.EntityType)
	if !ok {
		return compactEntry{}, errors.New("translation application returned an unsupported locale target")
	}
	if err := validateCompactLocale(core.Locale(entry.Locale)); err != nil {
		return compactEntry{}, err
	}
	updatedAt, err := compactTimestamp(entry.UpdatedAt)
	if err != nil {
		return compactEntry{}, err
	}
	return compactEntry{Profile: string(profile), Document: entry.Target.EntityId, Locale: entry.Locale, UpdatedAt: updatedAt}, nil
}

func encodeTranslationEntry(expected *managev1.TranslationTarget, entry *managev1.TranslationEntry) ([]byte, error) {
	metadata, err := compactTranslationEntry(expected, entry)
	if err != nil {
		return nil, err
	}
	return json.Marshal(metadata)
}
