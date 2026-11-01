package form

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// InterchangeMode is how an XLIFF import treats the target values it leaves
// out.
type InterchangeMode uint8

const (
	// InterchangePatch keeps the current target values the import leaves
	// out.
	InterchangePatch InterchangeMode = iota + 1
	// InterchangeReplace deletes them.
	InterchangeReplace
)

// TranslationInterchangeTarget is Form's exact sparse locale projection.
type TranslationInterchangeTarget struct {
	Exists   bool
	Revision string
	Targets  map[string]translation.UnitResult
}

type TranslationInterchangeMutation struct {
	FormID           string
	SourceLocale     string
	TargetLocale     string
	Mode             InterchangeMode
	ExpectedRevision *string
	Source           *translation.SourceDocument
	Plan             *translation.ExtractionPlan
	Targets          map[string]translation.UnitResult
	UnitHandles      []string
	Now              time.Time
}

type TranslationInterchangeResult struct {
	Revision            string
	Changed             bool
	AffectedUnitHandles []string
}

// LoadTranslationInterchangeTarget key-share locks the Form and projects
// its target locale for an XLIFF export.
func LoadTranslationInterchangeTarget(
	ctx context.Context,
	store Store,
	formID string,
	locale string,
	plan *translation.ExtractionPlan,
) (TranslationInterchangeTarget, error) {
	if err := validateFormInterchangeIdentity(formID, locale, plan); err != nil {
		return TranslationInterchangeTarget{}, err
	}
	state, err := loadFormInterchangeState(ctx, store, formID, locale, plan, KeyShareLock)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	return TranslationInterchangeTarget{Exists: state.loaded.CurrentExists, Revision: state.revision, Targets: state.targets}, nil
}

// formInterchangeState is the target locale of an XLIFF exchange as the
// locked Form holds it: its rows, target revision and current unit values.
type formInterchangeState struct {
	root     formAIDocumentRoot
	loaded   formAIDocumentLoadedState
	revision string
	targets  map[string]translation.UnitResult
}

// loadFormInterchangeState locks the Form root with lock and reads the
// target locale of plan. A target that has become the source locale, or a
// plan of a previous source locale, no longer matches the document.
func loadFormInterchangeState(
	ctx context.Context,
	store Store,
	formID string,
	locale string,
	plan *translation.ExtractionPlan,
	lock RowLock,
) (formInterchangeState, error) {
	root, err := loadFormAIDocumentRoot(ctx, store, formID, lock)
	if err != nil {
		return formInterchangeState{}, err
	}
	if !root.Status.Known() {
		return formInterchangeState{}, errFormUnsupportedLifecycle
	}
	loaded, err := loadAIDocumentStateAfterAuthorization(ctx, store, formID, locale, root, "", lock == UpdateLock)
	if err != nil {
		return formInterchangeState{}, err
	}
	if loaded.State.SourceLocale != plan.SourceLocale || locale == loaded.State.SourceLocale {
		return formInterchangeState{}, domainerr.InvalidField(
			"target_locale", "Form XLIFF locale role no longer matches the owning document",
		)
	}
	state := formInterchangeState{root: root, loaded: loaded, targets: map[string]translation.UnitResult{}}
	if !loaded.CurrentExists {
		return state, nil
	}
	if state.revision, err = deriveFormTargetRevision(root.DocumentRevision, loaded.Current); err != nil {
		return formInterchangeState{}, err
	}
	if state.targets, err = projectFormInterchangeTargets(plan, loaded.Current); err != nil {
		return formInterchangeState{}, err
	}
	return state, nil
}

// ApplyTranslationInterchange rebuilds the target schema from the current
// source-owned topology, then stores only locale-owned values. Authorization
// has already succeeded once in the caller's transaction; this seam owns the
// locked CAS, persistence and Audit.
func ApplyTranslationInterchange(
	ctx context.Context,
	joined Joined,
	mutation TranslationInterchangeMutation,
) (TranslationInterchangeResult, error) {
	if err := validateFormInterchangeMutation(mutation); err != nil {
		return TranslationInterchangeResult{}, err
	}
	store := joined.Store
	state, err := loadFormInterchangeState(ctx, store, mutation.FormID, mutation.TargetLocale, mutation.Plan, UpdateLock)
	if err != nil {
		return TranslationInterchangeResult{}, err
	}
	exists := state.loaded.CurrentExists
	if err := translation.ValidateExpectedTargetRevision(mutation.ExpectedRevision, state.revision, exists); err != nil {
		return TranslationInterchangeResult{}, err
	}
	candidate, err := buildFormInterchangeCandidate(mutation, state)
	if err != nil {
		return TranslationInterchangeResult{}, err
	}
	if exists && formInterchangeCandidateEqual(state.loaded.Current, candidate) {
		return TranslationInterchangeResult{Revision: state.revision}, nil
	}
	if err := writeFormInterchangeTarget(ctx, store, mutation, state, candidate); err != nil {
		return TranslationInterchangeResult{}, err
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if !exists {
		operation = sharedtelemetry.AuditItemOperationCreated
	}
	if err := domainaudit.AppendRequest(
		ctx, nil, joined.Audit, sharedtelemetry.AuditFormUpdated,
		formLocaleContentRecord(mutation.FormID, mutation.TargetLocale, operation),
	); err != nil {
		return TranslationInterchangeResult{}, err
	}
	nextRevision, err := advancedFormInterchangeRevision(ctx, store, mutation, state)
	if err != nil {
		return TranslationInterchangeResult{}, err
	}
	return TranslationInterchangeResult{
		Revision: nextRevision, Changed: true,
		AffectedUnitHandles: changedFormInterchangeHandles(state.targets, mutation.Targets, mutation.UnitHandles),
	}, nil
}

// buildFormInterchangeCandidate merges the imported units over the current
// ones and rebuilds the target locale on the source topology. Without a
// schema unit the target keeps an empty overlay of the source schema.
func buildFormInterchangeCandidate(mutation TranslationInterchangeMutation, state formInterchangeState) (*translation.Candidate, error) {
	desired := mergeFormInterchangeTargets(mutation.Mode, state.targets, mutation.Targets)
	candidate, err := ApplyTranslationCandidate(mutation.Source, desired)
	if err != nil {
		return nil, domainerr.InvalidField("file_id", err.Error())
	}
	sourceSchema := state.loaded.Source.Schema
	if !formInterchangeHasSchemaTarget(mutation.Plan, desired) {
		if candidate.ContentJSON, err = formAIDocumentEmptyTargetSchema(sourceSchema); err != nil {
			return nil, err
		}
	}
	if len(candidate.ContentJSON) != 0 {
		if err := validateFormAIDocumentTargetSchema(sourceSchema, candidate.ContentJSON); err != nil {
			return nil, domainerr.InvalidField("file_id", err.Error())
		}
	}
	candidate.ContentText = formCanonicalContentText(candidate.ContentJSON)
	return candidate, nil
}

// writeFormInterchangeTarget stores candidate as the target locale,
// updating the locked locale or inserting a missing one.
func writeFormInterchangeTarget(
	ctx context.Context,
	store Store,
	mutation TranslationInterchangeMutation,
	state formInterchangeState,
	candidate *translation.Candidate,
) error {
	now := mutation.Now.UTC()
	if now.IsZero() {
		return domainerr.InvalidField("now", "Form XLIFF mutation time is required")
	}
	change := LocaleChange{
		SetTitle: true, Title: candidate.Title,
		SetSchema: true, Schema: candidate.ContentJSON, ContentText: candidate.ContentText,
		UpdatedAt: now,
	}
	if !state.loaded.CurrentExists {
		return store.InsertLocale(ctx, mutation.FormID, mutation.TargetLocale, change, now)
	}
	change.UpdatedAt = translation.NextTargetUpdatedAt(now, state.loaded.Current.UpdatedAt)
	exists, err := store.UpdateLocale(ctx, mutation.FormID, mutation.TargetLocale, change)
	if err != nil {
		return err
	}
	if !exists {
		return domainerr.Precondition("Form translation changed; retry")
	}
	return nil
}

// advancedFormInterchangeRevision reads the target revision the write
// produced; a write that left it unchanged is a server fault.
func advancedFormInterchangeRevision(ctx context.Context, store Store, mutation TranslationInterchangeMutation, state formInterchangeState) (string, error) {
	reloaded, exists, err := store.Locale(ctx, mutation.FormID, mutation.TargetLocale, false)
	if err != nil {
		return "", err
	}
	nextRevision, err := deriveFormTargetRevision(state.root.DocumentRevision, reloaded)
	if err != nil {
		return "", err
	}
	if !exists || reloaded.Revision == "" || nextRevision == state.revision {
		return "", errors.New("Form XLIFF mutation did not advance target revision")
	}
	return nextRevision, nil
}

func validateFormInterchangeIdentity(formID, locale string, plan *translation.ExtractionPlan) error {
	if !IsValidUUID(formID) || strings.TrimSpace(locale) == "" || plan == nil ||
		plan.EntityType != "form" || plan.EntityID != formID || plan.TargetLocale != locale ||
		strings.TrimSpace(plan.SourceLocale) == "" || plan.SourceLocale == locale {
		return domainerr.InvalidField("target", "Form XLIFF identity does not match the current plan")
	}
	return nil
}

func validateFormInterchangeMutation(mutation TranslationInterchangeMutation) error {
	if err := validateFormInterchangeIdentity(
		mutation.FormID, mutation.TargetLocale, mutation.Plan,
	); err != nil {
		return err
	}
	if mutation.Source == nil || mutation.SourceLocale != mutation.Plan.SourceLocale || mutation.Targets == nil {
		return domainerr.InvalidField("target", "Form XLIFF identity does not match the current plan")
	}
	if mutation.Mode != InterchangePatch && mutation.Mode != InterchangeReplace {
		return domainerr.InvalidField("mode", "PATCH or REPLACE is required")
	}
	known := make(map[string]struct{}, len(mutation.Plan.Units))
	for _, unit := range mutation.Plan.Units {
		known[unit.UnitID] = struct{}{}
	}
	if len(mutation.Targets) != len(mutation.UnitHandles) {
		return domainerr.InvalidField("file_id", "Form XLIFF target set does not match its stable unit manifest")
	}
	seen := make(map[string]struct{}, len(mutation.UnitHandles))
	for _, handle := range mutation.UnitHandles {
		if _, duplicate := seen[handle]; duplicate {
			return domainerr.InvalidField("file_id", "Form XLIFF stable units must be unique")
		}
		seen[handle] = struct{}{}
		if _, ok := known[handle]; !ok {
			return domainerr.InvalidField("file_id", "Form XLIFF may update current source units only")
		}
		if result, ok := mutation.Targets[handle]; !ok || result.UnitID != handle {
			return domainerr.InvalidField("file_id", "Form XLIFF may update current source units only")
		}
	}
	for handle := range mutation.Targets {
		if _, ok := seen[handle]; !ok {
			return domainerr.InvalidField("file_id", "Form XLIFF target set does not match its stable unit manifest")
		}
	}
	return nil
}

func projectFormInterchangeTargets(
	plan *translation.ExtractionPlan,
	row Locale,
) (map[string]translation.UnitResult, error) {
	targets := make(map[string]translation.UnitResult)
	known := make(map[string]struct{}, len(plan.Units))
	for _, unit := range plan.Units {
		known[unit.UnitID] = struct{}{}
	}
	add := func(handle string, value *string) {
		if value == nil {
			return
		}
		if _, ok := known[handle]; ok {
			targets[handle] = translation.UnitResult{UnitID: handle, TranslatedText: *value}
		}
	}
	add("entity:title", row.Title)
	if len(row.Schema) == 0 {
		return targets, nil
	}
	var schema formDocumentObject
	if err := json.Unmarshal(row.Schema, &schema); err != nil {
		return nil, err
	}
	invalidLocaleValue := false
	walkFormSchemaTranslationText(formValueSlice(schema["steps"]), func(text formSchemaTranslationText) {
		if _, ok := known[text.unitID]; !ok {
			return
		}
		raw, present := text.object[text.field]
		if !present {
			return
		}
		value, ok := raw.(string)
		if !ok {
			invalidLocaleValue = true
			return
		}
		targets[text.unitID] = translation.UnitResult{UnitID: text.unitID, TranslatedText: value}
	})
	if invalidLocaleValue {
		return nil, errors.New("Form translation contains a non-text locale value")
	}
	return targets, nil
}

func mergeFormInterchangeTargets(
	mode InterchangeMode,
	current map[string]translation.UnitResult,
	imported map[string]translation.UnitResult,
) map[string]translation.UnitResult {
	result := make(map[string]translation.UnitResult, len(current)+len(imported))
	if mode == InterchangePatch {
		maps.Copy(result, current)
	}
	for handle, target := range imported {
		target.UnitID = handle
		result[handle] = target
	}
	return result
}

func formInterchangeCandidateEqual(row Locale, candidate *translation.Candidate) bool {
	if !formAIDocumentEqualString(row.Title, candidate.Title) ||
		!formAIDocumentEqualString(row.ContentText, candidate.ContentText) {
		return false
	}
	if len(row.Schema) == 0 || len(candidate.ContentJSON) == 0 {
		return len(row.Schema) == 0 && len(candidate.ContentJSON) == 0
	}
	var current, next any
	if json.Unmarshal(row.Schema, &current) != nil || json.Unmarshal(candidate.ContentJSON, &next) != nil {
		return false
	}
	return reflect.DeepEqual(current, next)
}

func formInterchangeHasSchemaTarget(
	plan *translation.ExtractionPlan,
	targets map[string]translation.UnitResult,
) bool {
	for _, unit := range plan.Units {
		if unit.FieldName == "content_json" {
			if _, present := targets[unit.UnitID]; present {
				return true
			}
		}
	}
	return false
}

func changedFormInterchangeHandles(
	current map[string]translation.UnitResult,
	incoming map[string]translation.UnitResult,
	handles []string,
) []string {
	affected := make([]string, 0, len(handles))
	for _, handle := range handles {
		if !reflect.DeepEqual(current[handle], incoming[handle]) {
			affected = append(affected, handle)
		}
	}
	sort.Strings(affected)
	return affected
}
