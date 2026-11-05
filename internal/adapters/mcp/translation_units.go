package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/translation"
	translationapp "github.com/echovisionlab/geul-api/internal/translation/application"
)

// translationUnitMarkupMaxBytes bounds one unit target.
const translationUnitMarkupMaxBytes = 100000

type translationUnitsGetArguments struct {
	translationTargetArguments
	TargetLocale core.Locale   `json:"l,omitempty"`
	Locales      []core.Locale `json:"ls,omitempty"`
	UnitHandles  []string      `json:"u,omitempty"`
	Blocks       []string      `json:"b,omitempty"`
	Query        string        `json:"q,omitempty"`
}

type translationUnitsPutArguments struct {
	translationTargetArguments
	TargetLocale           core.Locale       `json:"l,omitempty"`
	Targets                map[string]string `json:"u,omitempty"`
	ExpectedTargetRevision *string           `json:"er,omitempty"`
	Edits                  []translationEdit `json:"e,omitempty"`
}

type translationEdit struct {
	Source  *string           `json:"s"`
	Unit    string            `json:"i,omitempty"`
	Block   string            `json:"b,omitempty"`
	Targets map[string]string `json:"t"`
}

func (tools *TranslationTools) getTranslationUnits(ctx context.Context, _ string, call toolCall[translationUnitsGetArguments]) (mcpserver.ToolResult, error) {
	if err := rejectNullArguments(call.Arguments, "l", "ls", "u", "b", "q"); err != nil {
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
	if input.TargetLocale != "" && len(input.Locales) != 0 {
		return executionError(errors.New("l and ls cannot be used together"))
	}
	if len(input.UnitHandles) > 1000 {
		return executionError(errors.New("at most 1000 unit handles can be read at once"))
	}
	if err := validateStableUnitHandleSet(input.UnitHandles); err != nil {
		return executionError(err)
	}
	selection := translationUnitSelection{blocks: input.Blocks, query: input.Query}
	if input.TargetLocale != "" {
		if err := validateCompactLocale(input.TargetLocale); err != nil {
			return executionError(err)
		}
		units, err := tools.application.GetEntityTranslationUnits(ctx, translationapp.GetTranslationUnitsQuery{
			Target: translationapp.TargetFromMessage(target), TargetLocale: string(input.TargetLocale),
			UnitHandles: append([]string(nil), input.UnitHandles...),
		})
		if err != nil {
			return translationCallError(err)
		}
		units.Units = selection.filter(units.Units)
		return translationStructuredResult(encodeTranslationUnits(units))
	}
	if len(input.UnitHandles) == 0 && len(input.Blocks) == 0 && input.Query == "" {
		return executionError(errors.New("reading several locales needs b, q or u to keep the result small; pass l to read one whole locale"))
	}
	for _, locale := range input.Locales {
		if err := validateCompactLocale(locale); err != nil {
			return executionError(err)
		}
	}
	entity := translationapp.TargetFromMessage(target)
	locales, err := tools.translationUnitLocales(ctx, entity, input.Locales)
	if err != nil {
		return translationCallError(err)
	}
	return tools.readUnitsAcrossLocales(ctx, entity, locales, input.UnitHandles, selection)
}

// translationUnitLocales is the requested locales, or every existing target.
func (tools *TranslationTools) translationUnitLocales(ctx context.Context, target *translationapp.Target, requested []core.Locale) ([]string, error) {
	if len(requested) != 0 {
		locales := make([]string, 0, len(requested))
		for _, locale := range requested {
			locales = append(locales, string(locale))
		}
		return locales, nil
	}
	listed, err := tools.application.ListEntityTranslations(ctx, translationapp.ListEntityTranslationsQuery{Target: target})
	if err != nil {
		return nil, err
	}
	locales := make([]string, 0, len(listed.Entries))
	for _, entry := range listed.Entries {
		if entry.Locale != listed.SourceLocale {
			locales = append(locales, entry.Locale)
		}
	}
	return locales, nil
}

func (tools *TranslationTools) readUnitsAcrossLocales(ctx context.Context, target *translationapp.Target, locales, handles []string, selection translationUnitSelection) (mcpserver.ToolResult, error) {
	output := compactLocaleUnits{Revisions: map[string]string{}}
	byID := map[string]*compactLocaleUnit{}
	for _, locale := range locales {
		units, err := tools.application.GetEntityTranslationUnits(ctx, translationapp.GetTranslationUnitsQuery{
			Target: target, TargetLocale: locale, UnitHandles: append([]string(nil), handles...),
		})
		if err != nil {
			return translationCallError(err)
		}
		output.SourceLocale = units.SourceLocale
		if units.TargetRevision != nil {
			output.Revisions[locale] = *units.TargetRevision
		}
		for _, unit := range selection.filter(units.Units) {
			merged, seen := byID[unit.ID]
			if !seen {
				merged = newCompactLocaleUnit(unit)
				byID[unit.ID] = merged
				output.Units = append(output.Units, merged)
			}
			if unit.Target != nil {
				merged.Targets[locale] = translation.XLIFFSimplifiedMarkup(*unit.Target)
			}
		}
	}
	encoded, err := json.Marshal(output)
	return translationStructuredResult(encoded, err)
}

// translationUnitSelection narrows units by the Block they belong to and by
// text in any locale, ignoring case and markup.
type translationUnitSelection struct {
	blocks []string
	query  string
}

func (selection translationUnitSelection) filter(units []translation.XLIFFUnitMarkup) []translation.XLIFFUnitMarkup {
	if len(selection.blocks) == 0 && selection.query == "" {
		return units
	}
	return slices.DeleteFunc(slices.Clone(units), func(unit translation.XLIFFUnitMarkup) bool {
		return !selection.matches(unit)
	})
}

func (selection translationUnitSelection) matches(unit translation.XLIFFUnitMarkup) bool {
	if len(selection.blocks) != 0 && !slices.ContainsFunc(selection.blocks, func(block string) bool {
		return translationapp.UnitBelongsToBlock(unit, block)
	}) {
		return false
	}
	if selection.query == "" {
		return true
	}
	needle := strings.ToLower(selection.query)
	if strings.Contains(strings.ToLower(translation.XLIFFMarkupPlainText(unit.Source)), needle) {
		return true
	}
	return unit.Target != nil && strings.Contains(strings.ToLower(translation.XLIFFMarkupPlainText(*unit.Target)), needle)
}

func (tools *TranslationTools) putTranslationUnits(ctx context.Context, _ string, call toolCall[translationUnitsPutArguments]) (mcpserver.ToolResult, error) {
	if err := rejectNullArguments(call.Arguments, "l", "u", "er", "e"); err != nil {
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
	if len(input.Edits) != 0 {
		if input.TargetLocale != "" || input.Targets != nil || input.ExpectedTargetRevision != nil {
			return executionError(errors.New("e cannot be used together with l, u or er"))
		}
		return tools.editTranslationUnits(ctx, translationapp.TargetFromMessage(target), input.Edits)
	}
	if input.TargetLocale == "" || input.Targets == nil {
		return executionError(errors.New("pass e to set units by their source text, or l with u to set them by unit ID"))
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

func (tools *TranslationTools) editTranslationUnits(ctx context.Context, target *translationapp.Target, edits []translationEdit) (mcpserver.ToolResult, error) {
	if len(edits) > 1000 {
		return executionError(errors.New("e must contain at most 1000 edits"))
	}
	command := translationapp.EditTranslationUnitsCommand{Target: target, Edits: make([]translationapp.TranslationUnitEdit, 0, len(edits))}
	for index, edit := range edits {
		if edit.Source == nil {
			return executionError(fmt.Errorf("edit %d needs s, the unit's current source text", index))
		}
		if len(edit.Targets) == 0 {
			return executionError(fmt.Errorf("edit %d needs t, a target per locale", index))
		}
		for locale, text := range edit.Targets {
			if err := validateCompactLocale(core.Locale(locale)); err != nil {
				return executionError(fmt.Errorf("edit %d: %w", index, err))
			}
			if len(text) > translationUnitMarkupMaxBytes {
				return executionError(fmt.Errorf("edit %d target %s exceeds %d bytes", index, locale, translationUnitMarkupMaxBytes))
			}
		}
		command.Edits = append(command.Edits, translationapp.TranslationUnitEdit{
			Source: *edit.Source, Unit: edit.Unit, Block: edit.Block, Targets: edit.Targets,
		})
	}
	results, err := tools.application.EditEntityTranslationUnits(ctx, command)
	if err != nil {
		return translationCallError(err)
	}
	output := struct {
		Revisions map[string]string `json:"r"`
		Changed   map[string]bool   `json:"c"`
	}{map[string]string{}, map[string]bool{}}
	for locale, result := range results {
		output.Revisions[locale] = result.TargetRevision
		output.Changed[locale] = result.Changed
	}
	encoded, err := json.Marshal(output)
	return translationStructuredResult(encoded, err)
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

// compactLocaleUnits is the several-locale read: each unit appears once with
// its target per locale. A one-run unit reads as plain text, and plain text is
// what translation_units_put e accepts for it.
type compactLocaleUnits struct {
	SourceLocale string               `json:"s"`
	Revisions    map[string]string    `json:"r"`
	Units        []*compactLocaleUnit `json:"u"`
}

type compactLocaleUnit struct {
	ID      string            `json:"i"`
	GroupID string            `json:"g"`
	Context *string           `json:"c,omitempty"`
	Source  string            `json:"s"`
	Plain   string            `json:"p,omitempty"`
	Targets map[string]string `json:"t"`
}

func newCompactLocaleUnit(unit translation.XLIFFUnitMarkup) *compactLocaleUnit {
	compact := &compactLocaleUnit{
		ID: unit.ID, GroupID: unit.GroupID, Context: unit.Context,
		Source: translation.XLIFFSimplifiedMarkup(unit.Source), Targets: map[string]string{},
	}
	if plain := translation.XLIFFMarkupPlainText(unit.Source); plain != compact.Source {
		compact.Plain = plain
	}
	return compact
}
