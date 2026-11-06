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
	TargetLocale core.Locale   `json:"locale,omitempty"`
	Locales      []core.Locale `json:"locales,omitempty"`
	UnitIDs      []string      `json:"unit_ids,omitempty"`
	Blocks       []string      `json:"block_ids,omitempty"`
	Query        string        `json:"query,omitempty"`
}

type translationUnitsPutArguments struct {
	Documents []translationDocumentWrite `json:"documents"`
}

type translationEdit struct {
	Source  *string           `json:"source"`
	Unit    string            `json:"unit_id,omitempty"`
	Block   string            `json:"block_id,omitempty"`
	Targets map[string]string `json:"targets"`
}

func (tools *TranslationTools) getTranslationUnits(ctx context.Context, _ string, call toolCall[translationUnitsGetArguments]) (mcpserver.ToolResult, error) {
	if err := rejectNullArguments(call.Arguments, "locale", "locales", "unit_ids", "block_ids", "query"); err != nil {
		return executionError(err)
	}
	var input translationUnitsGetArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	targetMessage, err := translationTarget(input.translationTargetArguments)
	if err != nil {
		return executionError(err)
	}
	if input.TargetLocale != "" && len(input.Locales) != 0 {
		return executionError(errors.New("locale and locales cannot be used together"))
	}
	if len(input.UnitIDs) > 1000 {
		return executionError(errors.New("at most 1000 unit_ids can be read at once"))
	}
	if err := validateStableUnitHandleSet(input.UnitIDs); err != nil {
		return executionError(err)
	}
	target := translationapp.TargetFromMessage(targetMessage)
	selection := translationUnitSelection{blocks: input.Blocks, query: input.Query}
	if input.TargetLocale != "" {
		if err := validateLocaleCode(input.TargetLocale); err != nil {
			return executionError(err)
		}
		return tools.readUnitsInLocale(ctx, target, input, selection)
	}
	if len(input.UnitIDs) == 0 && len(input.Blocks) == 0 && input.Query == "" {
		return executionError(errors.New("reading several locales needs block_ids, query or unit_ids to keep the result small; pass locale to read one whole locale"))
	}
	for _, locale := range input.Locales {
		if err := validateLocaleCode(locale); err != nil {
			return executionError(err)
		}
	}
	locales, err := tools.translationUnitLocales(ctx, target, input.Locales)
	if err != nil {
		return translationCallError(err)
	}
	return tools.readUnitsAcrossLocales(ctx, target, locales, input.UnitIDs, selection)
}

func (tools *TranslationTools) readUnitsInLocale(ctx context.Context, target *translationapp.Target, input translationUnitsGetArguments, selection translationUnitSelection) (mcpserver.ToolResult, error) {
	units, err := tools.application.GetEntityTranslationUnits(ctx, translationapp.GetTranslationUnitsQuery{
		Target: target, TargetLocale: string(input.TargetLocale), UnitHandles: append([]string(nil), input.UnitIDs...),
	})
	if err != nil {
		return translationCallError(err)
	}
	units.Units = selection.filter(units.Units)
	return translationStructuredResult(encodeTranslationUnits(units))
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
	output := unitsAcrossLocalesOutput{Revisions: map[string]string{}}
	byID := map[string]*unitAcrossLocales{}
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
				merged = newUnitAcrossLocales(unit)
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

// translationDocumentWrite is one document's translations in a call over many.
type translationDocumentWrite struct {
	translationTargetArguments
	TargetLocale           core.Locale       `json:"locale,omitempty"`
	UnitTargets            map[string]string `json:"unit_targets,omitempty"`
	ExpectedTargetRevision *string           `json:"expected_target_revision,omitempty"`
	Edits                  []translationEdit `json:"edits,omitempty"`
}

// translationDocumentResult is one document's outcome. With edits, Locales holds
// each locale; with unit_targets, the rest describes the one locale written.
type translationDocumentResult struct {
	documentOutcome
	Locales        map[string]translationEditLocaleResult `json:"locales,omitempty"`
	TargetRevision string                                 `json:"target_revision,omitempty"`
	Changed        *bool                                  `json:"changed,omitempty"`
	UnitIDs        []string                               `json:"unit_ids,omitempty"`
}

func (tools *TranslationTools) putTranslationUnits(ctx context.Context, _ string, call toolCall[translationUnitsPutArguments]) (mcpserver.ToolResult, error) {
	var input translationUnitsPutArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if len(input.Documents) == 0 || len(input.Documents) > documentBatchMaxDocuments {
		return executionError(fmt.Errorf("documents must name 1 to %d documents", documentBatchMaxDocuments))
	}
	for index, document := range input.Documents {
		if err := validateTranslationDocumentWrite(document); err != nil {
			return executionError(fmt.Errorf("document %d: %w", index, err))
		}
	}
	results := make([]translationDocumentResult, 0, len(input.Documents))
	for _, document := range input.Documents {
		results = append(results, tools.writeTranslationDocument(ctx, document))
	}
	return encodeDocumentBatch(results)
}

// validateTranslationDocumentWrite refuses a malformed document before any
// document of the call is written.
func validateTranslationDocumentWrite(document translationDocumentWrite) error {
	if _, err := translationTarget(document.translationTargetArguments); err != nil {
		return err
	}
	if len(document.Edits) != 0 {
		if document.TargetLocale != "" || document.UnitTargets != nil || document.ExpectedTargetRevision != nil {
			return errors.New("edits cannot be used together with locale, unit_targets or expected_target_revision")
		}
		return validateTranslationEdits(document.Edits)
	}
	if document.TargetLocale == "" || document.UnitTargets == nil {
		return errors.New("pass edits to set units by their source text, or locale with unit_targets to set them by unit ID")
	}
	if err := validateLocaleCode(document.TargetLocale); err != nil {
		return err
	}
	if err := validateTranslationUnitTargets(document.UnitTargets); err != nil {
		return err
	}
	if document.ExpectedTargetRevision != nil {
		return validateOpaqueValue("expected target revision", *document.ExpectedTargetRevision, 256)
	}
	return nil
}

func (tools *TranslationTools) writeTranslationDocument(ctx context.Context, document translationDocumentWrite) translationDocumentResult {
	result := translationDocumentResult{documentOutcome: documentOutcome{DocumentType: document.DocumentType, DocumentID: document.DocumentID, Status: documentStatusApplied}}
	// validateTranslationDocumentWrite already accepted this target.
	targetMessage, _ := translationTarget(document.translationTargetArguments)
	target := translationapp.TargetFromMessage(targetMessage)
	var err error
	if len(document.Edits) != 0 {
		err = tools.editTranslationUnits(ctx, target, document.Edits, &result)
	} else {
		err = tools.putTranslationUnitTargets(ctx, target, document, &result)
	}
	if err != nil {
		_, callErr := translationCallError(err)
		result.fail(ctx, callErr)
	}
	return result
}

// editTranslationUnits sets units by their source text, in any number of locales.
func (tools *TranslationTools) editTranslationUnits(ctx context.Context, target *translationapp.Target, edits []translationEdit, result *translationDocumentResult) error {
	unitEdits := make([]translationapp.TranslationUnitEdit, 0, len(edits))
	for _, edit := range edits {
		unitEdits = append(unitEdits, translationapp.TranslationUnitEdit{Source: *edit.Source, Unit: edit.Unit, Block: edit.Block, Targets: edit.Targets})
	}
	written, err := tools.application.EditEntityTranslationUnits(ctx, translationapp.EditTranslationUnitsCommand{Target: target, Edits: unitEdits})
	if err != nil {
		return err
	}
	result.Locales = translationEditLocaleResults(written)
	return nil
}

// putTranslationUnitTargets sets units of one locale by unit ID.
func (tools *TranslationTools) putTranslationUnitTargets(ctx context.Context, target *translationapp.Target, document translationDocumentWrite, result *translationDocumentResult) error {
	written, err := tools.application.PutEntityTranslationUnits(ctx, translationapp.PutTranslationUnitsCommand{
		Target: target, TargetLocale: string(document.TargetLocale),
		ExpectedTargetRevision: document.ExpectedTargetRevision, Targets: document.UnitTargets,
	})
	if err != nil {
		return err
	}
	result.TargetRevision, result.Changed, result.UnitIDs = written.TargetRevision, &written.Changed, append([]string{}, written.AffectedUnitHandles...)
	return nil
}

func validateTranslationEdits(edits []translationEdit) error {
	if len(edits) > 1000 {
		return errors.New("edits must contain at most 1000 edits")
	}
	for index, edit := range edits {
		if edit.Source == nil {
			return fmt.Errorf("edit %d needs source, the unit's current source text", index)
		}
		if len(edit.Targets) == 0 {
			return fmt.Errorf("edit %d needs targets, a target per locale", index)
		}
		for locale, text := range edit.Targets {
			if err := validateLocaleCode(core.Locale(locale)); err != nil {
				return fmt.Errorf("edit %d: %w", index, err)
			}
			if len(text) > translationUnitMarkupMaxBytes {
				return fmt.Errorf("edit %d target %s exceeds %d bytes", index, locale, translationUnitMarkupMaxBytes)
			}
		}
	}
	return nil
}

type translationEditLocaleResult struct {
	TargetRevision string `json:"target_revision,omitempty"`
	Changed        bool   `json:"changed"`
}

func translationEditLocaleResults(results map[string]translationapp.XLIFFImport) map[string]translationEditLocaleResult {
	locales := make(map[string]translationEditLocaleResult, len(results))
	for locale, result := range results {
		locales[locale] = translationEditLocaleResult{TargetRevision: result.TargetRevision, Changed: result.Changed}
	}
	return locales
}

func validateTranslationUnitTargets(targets map[string]string) error {
	if len(targets) == 0 || len(targets) > 1000 {
		return errors.New("unit_targets must contain between 1 and 1000 unit targets")
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

type translationUnitOutput struct {
	ID      string  `json:"unit_id"`
	GroupID string  `json:"group_id"`
	Context *string `json:"context,omitempty"`
	Source  string  `json:"source"`
	Target  *string `json:"target,omitempty"`
}

func encodeTranslationUnits(units translationapp.TranslationUnits) ([]byte, error) {
	for _, locale := range []string{units.SourceLocale, units.TargetLocale} {
		if err := validateLocaleCode(core.Locale(locale)); err != nil {
			return nil, fmt.Errorf("translation application returned an invalid locale: %w", err)
		}
	}
	compact := make([]translationUnitOutput, 0, len(units.Units))
	for _, unit := range units.Units {
		compact = append(compact, translationUnitOutput{
			ID: unit.ID, GroupID: unit.GroupID, Context: unit.Context, Source: unit.Source, Target: unit.Target,
		})
	}
	return json.Marshal(struct {
		SourceLocale   string                  `json:"source_locale"`
		TargetLocale   string                  `json:"locale"`
		TargetRevision *string                 `json:"target_revision,omitempty"`
		Units          []translationUnitOutput `json:"units"`
	}{units.SourceLocale, units.TargetLocale, units.TargetRevision, compact})
}

// unitsAcrossLocalesOutput is the several-locale read: each unit appears once with
// its target per locale. A one-run unit reads as plain text, and plain text is
// what translation_units_put edits accepts for it.
type unitsAcrossLocalesOutput struct {
	SourceLocale string               `json:"source_locale"`
	Revisions    map[string]string    `json:"target_revisions"`
	Units        []*unitAcrossLocales `json:"units"`
}

type unitAcrossLocales struct {
	ID      string            `json:"unit_id"`
	GroupID string            `json:"group_id"`
	Context *string           `json:"context,omitempty"`
	Source  string            `json:"source"`
	Plain   string            `json:"plain_source,omitempty"`
	Targets map[string]string `json:"targets"`
}

func newUnitAcrossLocales(unit translation.XLIFFUnitMarkup) *unitAcrossLocales {
	output := &unitAcrossLocales{
		ID: unit.ID, GroupID: unit.GroupID, Context: unit.Context,
		Source: translation.XLIFFSimplifiedMarkup(unit.Source), Targets: map[string]string{},
	}
	if plain := translation.XLIFFMarkupPlainText(unit.Source); plain != output.Source {
		output.Plain = plain
	}
	return output
}

// DocumentTranslationUnits writes a document's translation units, naming it as the
// translation service does.
type DocumentTranslationUnits struct {
	application DocumentTextTranslations
}

// DocumentTextTranslations is the translation use case behind text edits.
type DocumentTextTranslations interface {
	EditEntityTranslationUnits(context.Context, translationapp.EditTranslationUnitsCommand) (map[string]translationapp.XLIFFImport, error)
}

func NewDocumentTranslationUnits(application DocumentTextTranslations) (*DocumentTranslationUnits, error) {
	if interfaceValueIsNil(application) {
		return nil, errors.New("MCP document translation units need the translation application")
	}
	return &DocumentTranslationUnits{application: application}, nil
}

func (units *DocumentTranslationUnits) EditDocumentUnits(ctx context.Context, document core.DocumentIdentity, edits []translationapp.TranslationUnitEdit) (map[string]translationapp.XLIFFImport, error) {
	target, err := translationTarget(translationTargetArguments{DocumentType: document.Domain, DocumentID: document.Reference})
	if err != nil {
		return nil, &core.InputError{Message: err.Error()}
	}
	return units.application.EditEntityTranslationUnits(ctx, translationapp.EditTranslationUnitsCommand{
		Target: translationapp.TargetFromMessage(target), Edits: edits,
	})
}
