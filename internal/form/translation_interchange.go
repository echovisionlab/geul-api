package form

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
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
	Mode             managev1.TranslationInterchangeMode
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

func (s *InternalFormService) LoadTranslationInterchangeTarget(
	ctx context.Context,
	tx *gorm.DB,
	formID string,
	locale string,
	plan *translation.ExtractionPlan,
) (TranslationInterchangeTarget, error) {
	if err := validateFormInterchangeIdentity(formID, locale, plan); err != nil {
		return TranslationInterchangeTarget{}, err
	}
	root, err := loadFormAIDocumentRoot(ctx, tx, formID, "KEY SHARE")
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	if _, valid := formAIDocumentLifecycle(root.Status); !valid {
		return TranslationInterchangeTarget{}, errs.InternalMsg("Form has an unsupported lifecycle status")
	}
	loaded, err := s.loadAIDocumentStateAfterAuthorization(
		ctx, tx, formID, locale, root, "", false,
	)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	if loaded.State.SourceLocale != plan.SourceLocale || locale == loaded.State.SourceLocale {
		return TranslationInterchangeTarget{}, errs.InvalidArgument(
			"target_locale", "Form XLIFF locale role no longer matches the owning document",
		)
	}
	state := TranslationInterchangeTarget{Exists: loaded.CurrentExists}
	if loaded.CurrentExists {
		state.Revision, err = deriveFormTargetRevision(root.DocumentRevision, loaded.Current)
		if err != nil {
			return TranslationInterchangeTarget{}, errs.Internal(err)
		}
		state.Targets, err = projectFormInterchangeTargets(plan, loaded.Current)
		if err != nil {
			return TranslationInterchangeTarget{}, err
		}
	} else {
		state.Targets = map[string]translation.UnitResult{}
	}
	return state, nil
}

// ApplyTranslationInterchange rebuilds the target schema from the current
// source-owned topology, then stores only locale-owned values. Authorization
// has already succeeded once in the caller's transaction; this seam owns the
// locked CAS, persistence, Audit and derived OG request.
func (s *InternalFormService) ApplyTranslationInterchange(
	ctx context.Context,
	tx *gorm.DB,
	mutation TranslationInterchangeMutation,
) (TranslationInterchangeResult, error) {
	if s == nil || tx == nil || s.auditWriter == nil {
		return TranslationInterchangeResult{}, errs.DependencyUnavailable("Form translation interchange")
	}
	if err := validateFormInterchangeMutation(mutation); err != nil {
		return TranslationInterchangeResult{}, err
	}
	root, err := loadFormAIDocumentRoot(ctx, tx, mutation.FormID, "UPDATE")
	if err != nil {
		return TranslationInterchangeResult{}, err
	}
	if _, valid := formAIDocumentLifecycle(root.Status); !valid {
		return TranslationInterchangeResult{}, errs.InternalMsg("Form has an unsupported lifecycle status")
	}
	loaded, err := s.loadAIDocumentStateAfterAuthorization(
		ctx, tx, mutation.FormID, mutation.TargetLocale, root, "", true,
	)
	if err != nil {
		return TranslationInterchangeResult{}, err
	}
	if loaded.State.SourceLocale != mutation.SourceLocale || mutation.TargetLocale == loaded.State.SourceLocale {
		return TranslationInterchangeResult{}, errs.InvalidArgument(
			"target_locale", "Form XLIFF locale role no longer matches the owning document",
		)
	}
	currentRevision := ""
	if loaded.CurrentExists {
		currentRevision, err = deriveFormTargetRevision(root.DocumentRevision, loaded.Current)
		if err != nil {
			return TranslationInterchangeResult{}, errs.Internal(err)
		}
	}
	if err := translation.ValidateExpectedTargetRevision(
		mutation.ExpectedRevision, currentRevision, loaded.CurrentExists,
	); err != nil {
		return TranslationInterchangeResult{}, err
	}
	currentTargets := map[string]translation.UnitResult{}
	if loaded.CurrentExists {
		currentTargets, err = projectFormInterchangeTargets(mutation.Plan, loaded.Current)
		if err != nil {
			return TranslationInterchangeResult{}, err
		}
	}
	desired := mergeFormInterchangeTargets(mutation.Mode, currentTargets, mutation.Targets)
	candidate, err := ApplyTranslationCandidate(mutation.Source, desired)
	if err != nil {
		return TranslationInterchangeResult{}, errs.InvalidArgument("file_id", err.Error())
	}
	if !formInterchangeHasSchemaTarget(mutation.Plan, desired) {
		candidate.ContentJSON, err = formAIDocumentEmptyTargetSchema(loaded.Source.Schema)
		if err != nil {
			return TranslationInterchangeResult{}, errs.Internal(err)
		}
	}
	if len(candidate.ContentJSON) != 0 {
		if err := validateFormAIDocumentTargetSchema(loaded.Source.Schema, candidate.ContentJSON); err != nil {
			return TranslationInterchangeResult{}, errs.InvalidArgument("file_id", err.Error())
		}
	}
	candidate.ContentText = formCanonicalContentText(candidate.ContentJSON)
	if loaded.CurrentExists && formInterchangeCandidateEqual(loaded.Current, candidate) {
		return TranslationInterchangeResult{Revision: currentRevision}, nil
	}

	now := mutation.Now.UTC()
	if now.IsZero() {
		return TranslationInterchangeResult{}, errs.InvalidArgument("now", "Form XLIFF mutation time is required")
	}
	if loaded.CurrentExists {
		now = translation.NextTargetUpdatedAt(now, loaded.Current.UpdatedAt)
	}
	affected := changedFormInterchangeHandles(currentTargets, mutation.Targets, mutation.UnitHandles)
	fields := structured.Fields{
		"title": candidate.Title, "content_json": formJSONValueOrNil(candidate.ContentJSON),
		"content_text": candidate.ContentText, "updated_at": now,
	}
	if loaded.CurrentExists {
		result := tx.WithContext(ctx).Table("form_translation").
			Where("entity_id = ?::uuid AND locale = ?", mutation.FormID, mutation.TargetLocale).
			Updates(fields)
		if result.Error != nil {
			return TranslationInterchangeResult{}, errs.Internal(result.Error)
		}
		if result.RowsAffected != 1 {
			return TranslationInterchangeResult{}, errs.FailedPrecondition("Form translation changed; retry")
		}
	} else {
		if err := tx.WithContext(ctx).Exec(
			`INSERT INTO form_translation (
				entity_id, locale, title, content_json, content_text, created_at, updated_at
			) VALUES (?::uuid, ?, ?, ?, ?, ?, ?)`,
			mutation.FormID, mutation.TargetLocale, candidate.Title,
			formJSONValueOrNil(candidate.ContentJSON), candidate.ContentText, now, now,
		).Error; err != nil {
			return TranslationInterchangeResult{}, errs.Internal(err)
		}
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if !loaded.CurrentExists {
		operation = sharedtelemetry.AuditItemOperationCreated
	}
	if err := domainaudit.AppendRequest(
		ctx, tx, s.auditWriter, sharedtelemetry.AuditFormUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewFormLocaleContentAuditRecord(
				metadata, mutation.FormID, mutation.TargetLocale, operation,
			)
		},
	); err != nil {
		return TranslationInterchangeResult{}, err
	}
	reloaded, exists, err := loadFormAIDocumentLocale(
		ctx, tx, mutation.FormID, mutation.TargetLocale, false,
	)
	if err != nil {
		return TranslationInterchangeResult{}, err
	}
	nextRevision, err := deriveFormTargetRevision(root.DocumentRevision, reloaded)
	if err != nil {
		return TranslationInterchangeResult{}, errs.Internal(err)
	}
	if !exists || reloaded.Revision == "" || nextRevision == currentRevision {
		return TranslationInterchangeResult{}, errs.InternalMsg("Form XLIFF mutation did not advance target revision")
	}
	return TranslationInterchangeResult{
		Revision: nextRevision, Changed: true,
		AffectedUnitHandles: affected,
	}, nil
}

func validateFormInterchangeIdentity(formID, locale string, plan *translation.ExtractionPlan) error {
	if !IsValidUUID(formID) || strings.TrimSpace(locale) == "" || plan == nil ||
		plan.EntityType != "form" || plan.EntityID != formID || plan.TargetLocale != locale ||
		strings.TrimSpace(plan.SourceLocale) == "" || plan.SourceLocale == locale {
		return errs.InvalidArgument("target", "Form XLIFF identity does not match the current plan")
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
		return errs.InvalidArgument("target", "Form XLIFF identity does not match the current plan")
	}
	if mutation.Mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH &&
		mutation.Mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
		return errs.InvalidArgument("mode", "PATCH or REPLACE is required")
	}
	known := make(map[string]struct{}, len(mutation.Plan.Units))
	for _, unit := range mutation.Plan.Units {
		known[unit.UnitID] = struct{}{}
	}
	if len(mutation.Targets) != len(mutation.UnitHandles) {
		return errs.InvalidArgument("file_id", "Form XLIFF target set does not match its stable unit manifest")
	}
	seen := make(map[string]struct{}, len(mutation.UnitHandles))
	for _, handle := range mutation.UnitHandles {
		if _, duplicate := seen[handle]; duplicate {
			return errs.InvalidArgument("file_id", "Form XLIFF stable units must be unique")
		}
		seen[handle] = struct{}{}
		if _, ok := known[handle]; !ok {
			return errs.InvalidArgument("file_id", "Form XLIFF may update current source units only")
		}
		if result, ok := mutation.Targets[handle]; !ok || result.UnitID != handle {
			return errs.InvalidArgument("file_id", "Form XLIFF may update current source units only")
		}
	}
	for handle := range mutation.Targets {
		if _, ok := seen[handle]; !ok {
			return errs.InvalidArgument("file_id", "Form XLIFF target set does not match its stable unit manifest")
		}
	}
	return nil
}

func projectFormInterchangeTargets(
	plan *translation.ExtractionPlan,
	row formAIDocumentLocaleRow,
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
		return nil, errs.Internal(err)
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
		return nil, errs.InternalMsg("Form translation contains a non-text locale value")
	}
	return targets, nil
}

func mergeFormInterchangeTargets(
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

func formInterchangeCandidateEqual(row formAIDocumentLocaleRow, candidate *translation.Candidate) bool {
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
