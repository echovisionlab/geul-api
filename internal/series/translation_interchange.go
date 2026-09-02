package series

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// TranslationInterchangeTarget is Post Series' sparse target-locale copy.
type TranslationInterchangeTarget struct {
	Exists   bool
	Revision string
	Targets  map[string]translation.UnitResult
}

type TranslationInterchangeMutation struct {
	SeriesID         string
	SourceLocale     string
	TargetLocale     string
	Mode             managev1.TranslationInterchangeMode
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
	target           model.SeriesTranslation
	exists           bool
	revision         string
}

func (s *SeriesService) LoadTranslationInterchangeTarget(
	ctx context.Context,
	tx *gorm.DB,
	seriesID string,
	locale string,
	plan *translation.ExtractionPlan,
) (TranslationInterchangeTarget, error) {
	if err := validateSeriesInterchangeIdentity(seriesID, locale, plan); err != nil {
		return TranslationInterchangeTarget{}, err
	}
	state, err := loadSeriesInterchangeState(ctx, tx, seriesID, locale, false)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	if state.sourceLocale != plan.SourceLocale || locale == state.sourceLocale {
		return TranslationInterchangeTarget{}, errs.InvalidArgument(
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
func (s *SeriesService) ApplyTranslationInterchange(
	ctx context.Context,
	tx *gorm.DB,
	mutation TranslationInterchangeMutation,
) (TranslationInterchangeResult, error) {
	if s == nil || tx == nil || s.auditWriter == nil {
		return TranslationInterchangeResult{}, errs.DependencyUnavailable("Post Series translation interchange")
	}
	if err := validateSeriesInterchangeMutation(mutation); err != nil {
		return TranslationInterchangeResult{}, err
	}
	current, err := loadSeriesInterchangeState(
		ctx, tx, mutation.SeriesID, mutation.TargetLocale, true,
	)
	if err != nil {
		return TranslationInterchangeResult{}, err
	}
	if current.sourceLocale != mutation.SourceLocale || mutation.TargetLocale == current.sourceLocale {
		return TranslationInterchangeResult{}, errs.InvalidArgument(
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
	title := seriesInterchangeTarget(desired, mutation.Plan, "title")
	summary := seriesInterchangeTarget(desired, mutation.Plan, "summary")
	contentText := seriesInterchangeTarget(desired, mutation.Plan, "content_text")
	if current.exists && nullableStringEqual(current.target.Title, title) &&
		nullableStringEqual(current.target.Summary, summary) &&
		nullableStringEqual(current.target.ContentText, contentText) {
		return TranslationInterchangeResult{Revision: current.revision}, nil
	}

	now := mutation.Now.UTC()
	if now.IsZero() {
		return TranslationInterchangeResult{}, errs.InvalidArgument("now", "Post Series XLIFF mutation time is required")
	}
	affected := changedSeriesInterchangeHandles(currentTargets, mutation.Targets, mutation.UnitHandles)
	operation := sharedtelemetry.AuditItemOperationCreated
	if current.exists {
		operation = sharedtelemetry.AuditItemOperationUpdated
		now = translation.NextTargetUpdatedAt(now, current.target.UpdatedAt)
		result := tx.WithContext(ctx).Table("series_translation").
			Where("entity_id = ? AND locale = ?", mutation.SeriesID, mutation.TargetLocale).
			Updates(structured.Fields{
				"title": title, "summary": summary, "content_text": contentText, "updated_at": now,
			})
		if result.Error != nil {
			return TranslationInterchangeResult{}, errs.Internal(result.Error)
		}
		if result.RowsAffected != 1 {
			return TranslationInterchangeResult{}, errs.FailedPrecondition("Post Series translation changed; retry")
		}
	} else if err := tx.WithContext(ctx).Table("series_translation").Create(&model.SeriesTranslation{
		EntityID: mutation.SeriesID, Locale: mutation.TargetLocale,
		Title: title, Summary: summary, ContentText: contentText,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		return TranslationInterchangeResult{}, errs.Internal(err)
	}
	if err := domainaudit.AppendRequest(
		ctx, tx, s.auditWriter, sharedtelemetry.AuditPostSeriesUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostSeriesLocaleContentAuditRecord(
				metadata, mutation.SeriesID, mutation.TargetLocale, operation,
			)
		},
	); err != nil {
		return TranslationInterchangeResult{}, err
	}
	reloaded, err := loadSeriesInterchangeState(
		ctx, tx, mutation.SeriesID, mutation.TargetLocale, true,
	)
	if err != nil {
		return TranslationInterchangeResult{}, err
	}
	if !reloaded.exists || reloaded.revision == "" || reloaded.revision == current.revision {
		return TranslationInterchangeResult{}, errs.InternalMsg("Post Series XLIFF mutation did not advance target revision")
	}
	return TranslationInterchangeResult{
		Revision: reloaded.revision, Changed: true,
		AffectedUnitHandles: affected,
	}, nil
}

func loadSeriesInterchangeState(
	ctx context.Context,
	tx *gorm.DB,
	seriesID string,
	locale string,
	update bool,
) (seriesInterchangeState, error) {
	if tx == nil {
		return seriesInterchangeState{}, errs.DependencyUnavailable("Post Series translation interchange database")
	}
	contentDocument, err := loadSeriesContentDocumentState(ctx, tx, seriesID, update)
	if err != nil {
		return seriesInterchangeState{}, err
	}
	query := tx.WithContext(ctx).Table("series_translation").
		Where("entity_id = ? AND locale = ?", seriesID, locale)
	if update {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var target model.SeriesTranslation
	result := query.Take(&target)
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return seriesInterchangeState{}, errs.Internal(result.Error)
	}
	state := seriesInterchangeState{
		sourceLocale:     contentDocument.SourceLocale,
		documentRevision: contentDocument.Revision.String(),
		target:           target,
		exists:           result.Error == nil,
	}
	if state.exists {
		revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
			LocaleExists: true, DocumentRevision: state.documentRevision, LocaleUpdatedAt: &target.UpdatedAt,
		})
		if err != nil {
			return seriesInterchangeState{}, errs.Internal(err)
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
		return errs.InvalidArgument("target", "Post Series XLIFF identity does not match the current plan")
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
		return errs.InvalidArgument("target", "Post Series XLIFF identity does not match the current plan")
	}
	if mutation.Mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH &&
		mutation.Mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
		return errs.InvalidArgument("mode", "PATCH or REPLACE is required")
	}
	known := make(map[string]struct{}, len(mutation.Plan.Units))
	for _, unit := range mutation.Plan.Units {
		if unit.ContainerType != translation.ContainerTypeEntity || unit.ContainerID != mutation.SeriesID {
			return errs.InvalidArgument("file_id", "Post Series XLIFF contains a non-locale structural unit")
		}
		switch unit.UnitID {
		case "entity:title", "entity:summary", "entity:content_text":
		default:
			return errs.InvalidArgument("file_id", "Post Series XLIFF contains a non-locale structural unit")
		}
		known[unit.UnitID] = struct{}{}
	}
	if len(mutation.Targets) != len(mutation.UnitHandles) {
		return errs.InvalidArgument("file_id", "Post Series XLIFF target set does not match its stable unit manifest")
	}
	seen := make(map[string]struct{}, len(mutation.UnitHandles))
	for _, handle := range mutation.UnitHandles {
		if _, duplicate := seen[handle]; duplicate {
			return errs.InvalidArgument("file_id", "Post Series XLIFF stable units must be unique")
		}
		seen[handle] = struct{}{}
		if _, ok := known[handle]; !ok {
			return errs.InvalidArgument("file_id", "Post Series XLIFF may update locale copy only")
		}
		if result, ok := mutation.Targets[handle]; !ok || result.UnitID != handle {
			return errs.InvalidArgument("file_id", "Post Series XLIFF may update locale copy only")
		}
	}
	for handle := range mutation.Targets {
		if _, ok := seen[handle]; !ok {
			return errs.InvalidArgument("file_id", "Post Series XLIFF target set does not match its stable unit manifest")
		}
	}
	return nil
}

func projectSeriesInterchangeTargets(
	plan *translation.ExtractionPlan,
	row model.SeriesTranslation,
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
	mode managev1.TranslationInterchangeMode,
	current map[string]translation.UnitResult,
	imported map[string]translation.UnitResult,
) map[string]translation.UnitResult {
	result := make(map[string]translation.UnitResult, len(current)+len(imported))
	if mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH {
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
