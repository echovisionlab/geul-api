package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	translationapp "github.com/echovisionlab/geul-api/internal/translation/application"
)

// translationUnitMarkupMaxBytes bounds one unit target.
const translationUnitMarkupMaxBytes = 100000

type translationUnitsGetArguments struct {
	translationTargetArguments
	TargetLocale core.Locale `json:"l"`
	UnitHandles  []string    `json:"u,omitempty"`
}

type translationUnitsPutArguments struct {
	translationTargetArguments
	TargetLocale           core.Locale       `json:"l"`
	Targets                map[string]string `json:"u"`
	ExpectedTargetRevision *string           `json:"er,omitempty"`
}

func (tools *TranslationTools) getTranslationUnits(ctx context.Context, _ string, call toolCall[translationUnitsGetArguments]) (mcpserver.ToolResult, error) {
	if err := rejectNullArguments(call.Arguments, "u"); err != nil {
		return executionError(err)
	}
	var input translationUnitsGetArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	target, err := translationTarget(input.translationTargetArguments)
	if err != nil {
		return executionError(err)
	}
	if err := validateCompactLocale(input.TargetLocale); err != nil {
		return executionError(err)
	}
	if len(input.UnitHandles) > 1000 {
		return executionError(errors.New("at most 1000 unit handles can be read at once"))
	}
	if err := validateStableUnitHandleSet(input.UnitHandles); err != nil {
		return executionError(err)
	}
	units, err := tools.application.GetEntityTranslationUnits(ctx, translationapp.GetTranslationUnitsQuery{
		Target: translationapp.TargetFromMessage(target), TargetLocale: string(input.TargetLocale),
		UnitHandles: append([]string(nil), input.UnitHandles...),
	})
	if err != nil {
		return translationCallError(err)
	}
	return translationStructuredResult(encodeTranslationUnits(units))
}

func (tools *TranslationTools) putTranslationUnits(ctx context.Context, _ string, call toolCall[translationUnitsPutArguments]) (mcpserver.ToolResult, error) {
	if err := rejectNullArguments(call.Arguments, "er"); err != nil {
		return executionError(err)
	}
	var input translationUnitsPutArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	target, err := translationTarget(input.translationTargetArguments)
	if err != nil {
		return executionError(err)
	}
	if err := validateCompactLocale(input.TargetLocale); err != nil {
		return executionError(err)
	}
	if err := validateTranslationUnitTargets(input.Targets); err != nil {
		return executionError(err)
	}
	if input.ExpectedTargetRevision != nil {
		if err := validateCompactOpaque("expected target revision", *input.ExpectedTargetRevision, 256); err != nil {
			return executionError(err)
		}
	}
	targets := make(map[string]string, len(input.Targets))
	for handle, markup := range input.Targets {
		targets[handle] = markup
	}
	written, err := tools.application.PutEntityTranslationUnits(ctx, translationapp.PutTranslationUnitsCommand{
		Target: translationapp.TargetFromMessage(target), TargetLocale: string(input.TargetLocale),
		ExpectedTargetRevision: input.ExpectedTargetRevision, Targets: targets,
	})
	if err != nil {
		return translationCallError(err)
	}
	return translationStructuredResult(encodeXLIFFImport(written))
}

func validateTranslationUnitTargets(targets map[string]string) error {
	if len(targets) == 0 || len(targets) > 1000 {
		return errors.New("u must contain between 1 and 1000 unit targets")
	}
	for handle, markup := range targets {
		if err := validateStableUnitHandle(handle); err != nil {
			return err
		}
		if len(markup) > translationUnitMarkupMaxBytes {
			return fmt.Errorf("unit %q target exceeds %d bytes", handle, translationUnitMarkupMaxBytes)
		}
	}
	return nil
}

type compactTranslationUnit struct {
	ID      string  `json:"i"`
	GroupID string  `json:"g"`
	Context *string `json:"c,omitempty"`
	Source  string  `json:"s"`
	Target  *string `json:"t,omitempty"`
}

func encodeTranslationUnits(units translationapp.TranslationUnits) ([]byte, error) {
	for _, locale := range []string{units.SourceLocale, units.TargetLocale} {
		if err := validateCompactLocale(core.Locale(locale)); err != nil {
			return nil, fmt.Errorf("translation application returned an invalid locale: %w", err)
		}
	}
	compact := make([]compactTranslationUnit, 0, len(units.Units))
	for _, unit := range units.Units {
		compact = append(compact, compactTranslationUnit{
			ID: unit.ID, GroupID: unit.GroupID, Context: unit.Context, Source: unit.Source, Target: unit.Target,
		})
	}
	return json.Marshal(struct {
		SourceLocale   string                   `json:"s"`
		TargetLocale   string                   `json:"l"`
		TargetRevision *string                  `json:"r,omitempty"`
		Units          []compactTranslationUnit `json:"u"`
	}{units.SourceLocale, units.TargetLocale, units.TargetRevision, compact})
}
