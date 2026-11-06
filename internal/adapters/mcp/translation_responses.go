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

func timestampText(value *timestamppb.Timestamp) (*string, error) {
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
	if err := validateLocaleCode(core.Locale(sourceLocale)); err != nil {
		return nil, fmt.Errorf("translation application returned invalid source locale: %w", err)
	}
	locales := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		metadata, err := translationEntryOutput(target, entry)
		if err != nil {
			return nil, err
		}
		locales = append(locales, map[string]any{"locale": metadata.Locale, "updated_at": metadata.UpdatedAt})
	}
	return json.Marshal(map[string]any{"source_locale": sourceLocale, "locales": locales})
}

type translationEntryFields struct {
	DocumentType string  `json:"document_type"`
	DocumentID   string  `json:"document_id"`
	Locale       string  `json:"locale"`
	UpdatedAt    *string `json:"updated_at,omitempty"`
}

func translationEntryOutput(expected *managev1.TranslationTarget, entry *managev1.TranslationEntry) (translationEntryFields, error) {
	if entry == nil || entry.Target == nil || expected == nil ||
		entry.Target.EntityType != expected.EntityType || entry.Target.EntityId != expected.EntityId {
		return translationEntryFields{}, errors.New("translation application returned a different locale target")
	}
	profile, ok := translationProfile(entry.Target.EntityType)
	if !ok {
		return translationEntryFields{}, errors.New("translation application returned an unsupported locale target")
	}
	if err := validateLocaleCode(core.Locale(entry.Locale)); err != nil {
		return translationEntryFields{}, err
	}
	updatedAt, err := timestampText(entry.UpdatedAt)
	if err != nil {
		return translationEntryFields{}, err
	}
	return translationEntryFields{DocumentType: string(profile), DocumentID: entry.Target.EntityId, Locale: entry.Locale, UpdatedAt: updatedAt}, nil
}
