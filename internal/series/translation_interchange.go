package series

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// TranslationInterchangeTarget is Post Series' sparse target-locale copy.
type TranslationInterchangeTarget struct {
	Exists   bool
	Revision string
	Targets  map[string]translation.UnitResult
}

// InterchangeMode is how an imported target copy meets the stored one.
type InterchangeMode int

const (
	// InterchangeModeUnknown is a mode the import did not name.
	InterchangeModeUnknown InterchangeMode = iota
	// InterchangeModePatch keeps stored units the import leaves out.
	InterchangeModePatch
	// InterchangeModeReplace drops stored units the import leaves out.
	InterchangeModeReplace
)

type TranslationInterchangeMutation struct {
	SeriesID         string
	SourceLocale     string
	TargetLocale     string
	Mode             InterchangeMode
	ExpectedRevision *string
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

type seriesInterchangeState struct {
	sourceLocale     string
	documentRevision string
	target           Translation
	exists           bool
	revision         string
}

func (s *Service) LoadTranslationInterchangeTarget(
	ctx context.Context,
	store TranslationStore,
	seriesID string,
	locale string,
	plan *translation.ExtractionPlan,
) (TranslationInterchangeTarget, error) {
	if err := validateSeriesInterchangeIdentity(seriesID, locale, plan); err != nil {
		return TranslationInterchangeTarget{}, err
	}
	state, err := loadSeriesInterchangeState(ctx, store, seriesID, locale, false)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	if state.sourceLocale != plan.SourceLocale || locale == state.sourceLocale {
		return TranslationInterchangeTarget{}, domainerr.InvalidField(
			"target_locale", "Post Series XLIFF locale role no longer matches the owning document",
		)
	}
	return TranslationInterchangeTarget{
		Exists: state.exists, Revision: state.revision,
		Targets: projectSeriesInterchangeTargets(plan, state.target, state.exists),
	}, nil
}

// ApplyTranslationInterchange persists only Post Series target-locale fields.
// Source metadata, lifecycle and ordered Post relations are structurally
// unavailable to this target-only XLIFF seam.
func (s *Service) ApplyTranslationInterchange(
	ctx context.Context,
	store TranslationStore,
	mutation TranslationInterchangeMutation,
) (TranslationInterchangeResult, error) {
	if store == nil {
		return TranslationInterchangeResult{}, domainerr.DependencyUnavailable("Post Series translation interchange")
	}
	if err := validateSeriesInterchangeMutation(mutation); err != nil {
		return TranslationInterchangeResult{}, err
	}
	current, err := loadSeriesInterchangeState(
		ctx, store, mutation.SeriesID, mutation.TargetLocale, true,
	)
	if err != nil {
		return TranslationInterchangeResult{}, err
	}
	if current.sourceLocale != mutation.SourceLocale || mutation.TargetLocale == current.sourceLocale {
		return TranslationInterchangeResult{}, domainerr.InvalidField(
			"target_locale", "Post Series XLIFF locale role no longer matches the owning document",
		)
	}
	if err := translation.ValidateExpectedTargetRevision(
		mutation.ExpectedRevision, current.revision, current.exists,
	); err != nil {
		return TranslationInterchangeResult{}, err
	}
	currentTargets := projectSeriesInterchangeTargets(mutation.Plan, current.target, current.exists)
	desired := mergeSeriesInterchangeTargets(mutation.Mode, currentTargets, mutation.Targets)
	next := Translation{
		Title:       seriesInterchangeTarget(desired, mutation.Plan, "title"),
		Summary:     seriesInterchangeTarget(desired, mutation.Plan, "summary"),
		ContentText: seriesInterchangeTarget(desired, mutation.Plan, "content_text"),
	}
	if current.exists && current.target.sameCopy(next) {
		return TranslationInterchangeResult{Revision: current.revision}, nil
	}

	now := mutation.Now.UTC()
	if now.IsZero() {
		return TranslationInterchangeResult{}, domainerr.InvalidField("now", "Post Series XLIFF mutation time is required")
	}
	affected := changedSeriesInterchangeHandles(currentTargets, mutation.Targets, mutation.UnitHandles)
	operation, err := writeInterchangeTarget(ctx, store, mutation, current, next, now)
	if err != nil {
		return TranslationInterchangeResult{}, err
	}
	if err := store.AppendRequestAudit(
		ctx, s.audit, sharedtelemetry.AuditPostSeriesUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostSeriesLocaleContentAuditRecord(
				metadata, mutation.SeriesID, mutation.TargetLocale, operation,
			)
		},
	); err != nil {
		return TranslationInterchangeResult{}, err
	}
	reloaded, err := loadSeriesInterchangeState(
		ctx, store, mutation.SeriesID, mutation.TargetLocale, true,
	)
	if err != nil {
		return TranslationInterchangeResult{}, err
	}
	if !reloaded.exists || reloaded.revision == "" || reloaded.revision == current.revision {
		return TranslationInterchangeResult{}, errors.New("Post Series XLIFF mutation did not advance target revision")
	}
	return TranslationInterchangeResult{
		Revision: reloaded.revision, Changed: true,
		AffectedUnitHandles: affected,
	}, nil
}

// writeInterchangeTarget updates or creates the target locale row and
// returns the audited item operation.
func writeInterchangeTarget(
	ctx context.Context,
	store TranslationStore,
	mutation TranslationInterchangeMutation,
	current seriesInterchangeState,
	next Translation,
	now time.Time,
) (sharedtelemetry.AuditItemOperation, error) {
	if !current.exists {
		return sharedtelemetry.AuditItemOperationCreated, store.InsertTranslation(ctx, Translation{
			SeriesID: mutation.SeriesID, Locale: mutation.TargetLocale,
			Title: next.Title, Summary: next.Summary, ContentText: next.ContentText,
			CreatedAt: now, UpdatedAt: now,
		})
	}
	now = translation.NextTargetUpdatedAt(now, current.target.UpdatedAt)
	err := store.UpdateTranslation(ctx, mutation.SeriesID, mutation.TargetLocale, next.Title, next.Summary, next.ContentText, now)
	if errors.Is(err, ErrTranslationNotFound) {
		return "", domainerr.Precondition("Post Series translation changed; retry")
	}
	return sharedtelemetry.AuditItemOperationUpdated, err
}

func loadSeriesInterchangeState(
	ctx context.Context,
	store TranslationStore,
	seriesID string,
	locale string,
	update bool,
) (seriesInterchangeState, error) {
	contentDocument, err := loadSeriesContentDocumentState(ctx, store, seriesID, update)
	if err != nil {
		return seriesInterchangeState{}, err
	}
	target, err := store.Translation(ctx, seriesID, locale, update)
	if err != nil && !errors.Is(err, ErrTranslationNotFound) {
		return seriesInterchangeState{}, err
	}
	state := seriesInterchangeState{
		sourceLocale:     contentDocument.SourceLocale,
		documentRevision: contentDocument.Revision.String(),
		target:           target,
		exists:           err == nil,
	}
	if state.exists {
		revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
			LocaleExists: true, DocumentRevision: state.documentRevision, LocaleUpdatedAt: &target.UpdatedAt,
		})
		if err != nil {
			return seriesInterchangeState{}, err
		}
		state.revision = revision
	}
	return state, nil
}

func validateSeriesInterchangeIdentity(seriesID, locale string, plan *translation.ExtractionPlan) error {
	if _, err := uuidutil.ParseCanonical(seriesID, "series_id"); err != nil {
		return err
	}
	if strings.TrimSpace(locale) == "" || plan == nil || plan.EntityType != "series" ||
		plan.EntityID != seriesID || plan.TargetLocale != locale ||
		strings.TrimSpace(plan.SourceLocale) == "" || plan.SourceLocale == locale {
		return domainerr.InvalidField("target", "Post Series XLIFF identity does not match the current plan")
	}
	return nil
}

func validateSeriesInterchangeMutation(mutation TranslationInterchangeMutation) error {
	if err := validateSeriesInterchangeIdentity(
		mutation.SeriesID, mutation.TargetLocale, mutation.Plan,
	); err != nil {
		return err
	}
	if mutation.SourceLocale != mutation.Plan.SourceLocale || mutation.Targets == nil {
		return domainerr.InvalidField("target", "Post Series XLIFF identity does not match the current plan")
	}
	if mutation.Mode != InterchangeModePatch && mutation.Mode != InterchangeModeReplace {
		return domainerr.InvalidField("mode", "PATCH or REPLACE is required")
	}
	known := make(map[string]struct{}, len(mutation.Plan.Units))
	for _, unit := range mutation.Plan.Units {
		if unit.ContainerType != translation.ContainerTypeEntity || unit.ContainerID != mutation.SeriesID {
			return domainerr.InvalidField("file_id", "Post Series XLIFF contains a non-locale structural unit")
		}
		switch unit.UnitID {
		case "entity:title", "entity:summary", "entity:content_text":
		default:
			return domainerr.InvalidField("file_id", "Post Series XLIFF contains a non-locale structural unit")
		}
		known[unit.UnitID] = struct{}{}
	}
	if len(mutation.Targets) != len(mutation.UnitHandles) {
		return domainerr.InvalidField("file_id", "Post Series XLIFF target set does not match its stable unit manifest")
	}
	seen := make(map[string]struct{}, len(mutation.UnitHandles))
	for _, handle := range mutation.UnitHandles {
		if _, duplicate := seen[handle]; duplicate {
			return domainerr.InvalidField("file_id", "Post Series XLIFF stable units must be unique")
		}
		seen[handle] = struct{}{}
		if _, ok := known[handle]; !ok {
			return domainerr.InvalidField("file_id", "Post Series XLIFF may update locale copy only")
		}
		if result, ok := mutation.Targets[handle]; !ok || result.UnitID != handle {
			return domainerr.InvalidField("file_id", "Post Series XLIFF may update locale copy only")
		}
	}
	for handle := range mutation.Targets {
		if _, ok := seen[handle]; !ok {
			return domainerr.InvalidField("file_id", "Post Series XLIFF target set does not match its stable unit manifest")
		}
	}
	return nil
}

func projectSeriesInterchangeTargets(
	plan *translation.ExtractionPlan,
	row Translation,
	exists bool,
) map[string]translation.UnitResult {
	targets := make(map[string]translation.UnitResult)
	if !exists {
		return targets
	}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{name: "title", value: row.Title},
		{name: "summary", value: row.Summary},
		{name: "content_text", value: row.ContentText},
	} {
		handle := "entity:" + field.name
		if field.value == nil || !seriesInterchangePlanHasUnit(plan, handle) {
			continue
		}
		targets[handle] = translation.UnitResult{UnitID: handle, TranslatedText: *field.value}
	}
	return targets
}

func mergeSeriesInterchangeTargets(
	mode InterchangeMode,
	current map[string]translation.UnitResult,
	imported map[string]translation.UnitResult,
) map[string]translation.UnitResult {
	result := make(map[string]translation.UnitResult, len(current)+len(imported))
	if mode == InterchangeModePatch {
		maps.Copy(result, current)
	}
	for handle, target := range imported {
		target.UnitID = handle
		result[handle] = target
	}
	return result
}

func seriesInterchangeTarget(
	targets map[string]translation.UnitResult,
	plan *translation.ExtractionPlan,
	field string,
) *string {
	handle := "entity:" + field
	if !seriesInterchangePlanHasUnit(plan, handle) {
		return nil
	}
	target, present := targets[handle]
	if !present {
		return nil
	}
	value := target.TranslatedText
	return &value
}

func seriesInterchangePlanHasUnit(plan *translation.ExtractionPlan, handle string) bool {
	if plan == nil {
		return false
	}
	for _, unit := range plan.Units {
		if unit.UnitID == handle {
			return true
		}
	}
	return false
}

func changedSeriesInterchangeHandles(
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
