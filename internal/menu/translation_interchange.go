package menu

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// TranslationInterchangeTarget is Menu's raw sparse target projection.
// Missing map entries remain absent; a present empty label remains explicit.
type TranslationInterchangeTarget struct {
	Exists   bool
	Revision string
	Targets  map[string]translation.UnitResult
}

// TranslationInterchangeMutation is one application-validated XLIFF target
// import. Menu revalidates identity, locale role and target CAS under its own
// root and locale-row locks before persisting values-only labels and Audit.
type TranslationInterchangeMutation struct {
	MenuID           string
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

// LoadTranslationInterchangeTarget loads only target-owned values. It never
// materializes source fallback into an absent Menu locale row.
func (s *MenuService) LoadTranslationInterchangeTarget(
	ctx context.Context,
	tx *gorm.DB,
	menuID string,
	locale string,
	plan *translation.ExtractionPlan,
) (TranslationInterchangeTarget, error) {
	if err := validateMenuInterchangeIdentity(menuID, locale, plan); err != nil {
		return TranslationInterchangeTarget{}, err
	}
	snapshot, err := loadMenuAIDocumentSnapshot(ctx, tx, menuID, locale, false)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	if snapshot.SourceLocale != plan.SourceLocale || locale == snapshot.SourceLocale {
		return TranslationInterchangeTarget{}, errs.InvalidArgument(
			"target_locale", "Menu XLIFF locale role no longer matches the owning document",
		)
	}
	revision, err := menuInterchangeRevision(snapshot)
	if err != nil {
		return TranslationInterchangeTarget{}, errs.Internal(err)
	}
	return TranslationInterchangeTarget{
		Exists: snapshot.LocaleExists, Revision: revision,
		Targets: projectMenuInterchangeTargets(plan, snapshot.Labels, snapshot.LocaleExists),
	}, nil
}

// ApplyTranslationInterchange owns Menu target CAS, sparse persistence and
// Audit in the caller's already-authorized transaction. It never changes the
// source-owned item graph.
func (s *MenuService) ApplyTranslationInterchange(
	ctx context.Context,
	tx *gorm.DB,
	mutation TranslationInterchangeMutation,
) (TranslationInterchangeResult, error) {
	if s == nil || tx == nil || s.auditWriter == nil {
		return TranslationInterchangeResult{}, errs.DependencyUnavailable("Menu translation interchange")
	}
	if err := validateMenuInterchangeMutation(mutation); err != nil {
		return TranslationInterchangeResult{}, err
	}
	root, err := lockMenuForUpdate(ctx, tx, mutation.MenuID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return TranslationInterchangeResult{}, errs.NotFound("menu", mutation.MenuID)
	}
	if err != nil {
		return TranslationInterchangeResult{}, errs.Internal(err)
	}
	current, err := loadMenuAIDocumentSnapshotFromRoot(
		ctx, tx, *root, mutation.TargetLocale, true,
	)
	if err != nil {
		return TranslationInterchangeResult{}, err
	}
	if current.SourceLocale != mutation.SourceLocale || mutation.TargetLocale == current.SourceLocale {
		return TranslationInterchangeResult{}, errs.InvalidArgument(
			"target_locale", "Menu XLIFF locale role no longer matches the owning document",
		)
	}
	currentRevision, err := menuInterchangeRevision(current)
	if err != nil {
		return TranslationInterchangeResult{}, errs.Internal(err)
	}
	if err := translation.ValidateExpectedTargetRevision(
		mutation.ExpectedRevision, currentRevision, current.LocaleExists,
	); err != nil {
		return TranslationInterchangeResult{}, err
	}

	currentLabels := current.Labels
	currentExists := current.LocaleExists
	if !currentExists {
		currentLabels = seedMenuTargetLabels(current)
		currentExists = true
	}
	currentTargets := projectMenuInterchangeTargets(mutation.Plan, currentLabels, currentExists)
	desired := mergeMenuInterchangeTargets(mutation.Mode, currentTargets, mutation.Targets)
	nextLabels, err := menuInterchangeLabels(mutation.Plan, desired)
	if err != nil {
		return TranslationInterchangeResult{}, err
	}
	if current.LocaleExists && maps.Equal(current.Labels, nextLabels) {
		return TranslationInterchangeResult{Revision: currentRevision}, nil
	}

	now := mutation.Now.UTC()
	if now.IsZero() {
		return TranslationInterchangeResult{}, errs.InvalidArgument("now", "Menu XLIFF mutation time is required")
	}
	affected := changedMenuInterchangeHandles(
		mutation.Mode, currentTargets, mutation.Targets, mutation.UnitHandles,
	)
	next := cloneMenuAIDocumentSnapshot(current)
	next.LocaleExists = true
	next.Labels = nextLabels
	if err := persistMenuAIDocumentTarget(ctx, tx, current, next, false, now); err != nil {
		return TranslationInterchangeResult{}, err
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if !current.LocaleExists {
		operation = sharedtelemetry.AuditItemOperationCreated
	}
	if err := domainaudit.AppendRequest(
		ctx, tx, s.auditWriter, sharedtelemetry.AuditMenuUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMenuLocaleContentAuditRecord(
				metadata, mutation.MenuID, mutation.TargetLocale, operation,
			)
		},
	); err != nil {
		return TranslationInterchangeResult{}, err
	}
	reloaded, err := loadMenuAIDocumentSnapshotFromRoot(
		ctx, tx, *root, mutation.TargetLocale, true,
	)
	if err != nil {
		return TranslationInterchangeResult{}, err
	}
	revision, err := menuInterchangeRevision(reloaded)
	if err != nil {
		return TranslationInterchangeResult{}, errs.Internal(err)
	}
	if revision == currentRevision {
		return TranslationInterchangeResult{}, errs.InternalMsg("Menu XLIFF mutation did not advance target revision")
	}
	return TranslationInterchangeResult{
		Revision: revision, Changed: true,
		AffectedUnitHandles: affected,
	}, nil
}

func validateMenuInterchangeIdentity(menuID, locale string, plan *translation.ExtractionPlan) error {
	if strings.TrimSpace(menuID) == "" || strings.TrimSpace(locale) == "" || plan == nil ||
		plan.EntityType != "menu" || plan.EntityID != menuID || plan.TargetLocale != locale ||
		strings.TrimSpace(plan.SourceLocale) == "" || plan.SourceLocale == locale {
		return errs.InvalidArgument("target", "Menu XLIFF identity does not match the current plan")
	}
	return nil
}

func validateMenuInterchangeMutation(mutation TranslationInterchangeMutation) error {
	if err := validateMenuInterchangeIdentity(
		mutation.MenuID, mutation.TargetLocale, mutation.Plan,
	); err != nil {
		return err
	}
	if mutation.SourceLocale != mutation.Plan.SourceLocale || mutation.Targets == nil {
		return errs.InvalidArgument("target", "Menu XLIFF identity does not match the current plan")
	}
	if mutation.Mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH &&
		mutation.Mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
		return errs.InvalidArgument("mode", "PATCH or REPLACE is required")
	}
	known := make(map[string]struct{}, len(mutation.Plan.Units))
	for _, unit := range mutation.Plan.Units {
		if unit.ContainerType != translation.ContainerTypeBlock || unit.FieldName != "label" ||
			unit.UnitID != "item:"+unit.ContainerID+":label" || strings.TrimSpace(unit.ContainerID) == "" {
			return errs.InvalidArgument("file_id", "Menu XLIFF contains an invalid stable label unit")
		}
		known[unit.UnitID] = struct{}{}
	}
	if len(mutation.Targets) != len(mutation.UnitHandles) {
		return errs.InvalidArgument("file_id", "Menu XLIFF target set does not match its stable unit manifest")
	}
	seen := make(map[string]struct{}, len(mutation.UnitHandles))
	for _, handle := range mutation.UnitHandles {
		if _, duplicate := seen[handle]; duplicate {
			return errs.InvalidArgument("file_id", "Menu XLIFF stable units must be unique")
		}
		seen[handle] = struct{}{}
		if _, ok := known[handle]; !ok {
			return errs.InvalidArgument("file_id", "Menu XLIFF may update current source labels only")
		}
		if result, ok := mutation.Targets[handle]; !ok || result.UnitID != handle {
			return errs.InvalidArgument("file_id", "Menu XLIFF may update current source labels only")
		}
	}
	for handle := range mutation.Targets {
		if _, ok := seen[handle]; !ok {
			return errs.InvalidArgument("file_id", "Menu XLIFF target set does not match its stable unit manifest")
		}
	}
	return nil
}

func menuInterchangeRevision(snapshot AIDocumentSnapshot) (string, error) {
	if !snapshot.LocaleExists {
		return translation.DeriveTargetRevision(translation.TargetRevisionFacts{})
	}
	if snapshot.TargetRevision == nil || strings.TrimSpace(*snapshot.TargetRevision) == "" {
		return "", errors.New("menu target revision is missing")
	}
	return *snapshot.TargetRevision, nil
}

func projectMenuInterchangeTargets(
	plan *translation.ExtractionPlan,
	labels map[string]string,
	exists bool,
) map[string]translation.UnitResult {
	targets := make(map[string]translation.UnitResult)
	if !exists || plan == nil {
		return targets
	}
	for _, unit := range plan.Units {
		value, present := labels[unit.ContainerID]
		if !present || unit.UnitID != "item:"+unit.ContainerID+":label" {
			continue
		}
		targets[unit.UnitID] = translation.UnitResult{
			UnitID: unit.UnitID, TranslatedText: value,
		}
	}
	return targets
}

func mergeMenuInterchangeTargets(
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

func menuInterchangeLabels(
	plan *translation.ExtractionPlan,
	targets map[string]translation.UnitResult,
) (map[string]string, error) {
	labels := make(map[string]string, len(targets))
	for _, unit := range plan.Units {
		target, present := targets[unit.UnitID]
		if !present {
			continue
		}
		if unit.ContainerType != translation.ContainerTypeBlock || unit.FieldName != "label" ||
			unit.UnitID != "item:"+unit.ContainerID+":label" || strings.TrimSpace(unit.ContainerID) == "" {
			return nil, errs.InvalidArgument("file_id", "Menu XLIFF may update current source labels only")
		}
		labels[unit.ContainerID] = target.TranslatedText
	}
	return labels, nil
}

func changedMenuInterchangeHandles(
	mode managev1.TranslationInterchangeMode,
	current map[string]translation.UnitResult,
	incoming map[string]translation.UnitResult,
	handles []string,
) []string {
	candidates := append([]string(nil), handles...)
	if mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
		for handle := range current {
			if _, present := incoming[handle]; !present {
				candidates = append(candidates, handle)
			}
		}
	}
	affected := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, handle := range candidates {
		if _, duplicate := seen[handle]; duplicate {
			continue
		}
		seen[handle] = struct{}{}
		if !reflect.DeepEqual(current[handle], incoming[handle]) {
			affected = append(affected, handle)
		}
	}
	sort.Strings(affected)
	return affected
}
