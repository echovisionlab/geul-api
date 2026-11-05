package application

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// translationUnitsMaximum bounds one units read selection or write.
const translationUnitsMaximum = 1000

// GetTranslationUnitsQuery reads the translation units of one target locale:
// every current unit, or only UnitHandles when given.
type GetTranslationUnitsQuery struct {
	Target       *Target
	TargetLocale string
	UnitHandles  []string
}

// TranslationUnits is the source and current target of each unit as XLIFF 2.2
// inline markup. TargetRevision is nil while the target locale has no values.
type TranslationUnits struct {
	SourceLocale   string
	TargetLocale   string
	TargetRevision *string
	Units          []translation.XLIFFUnitMarkup
}

// GetEntityTranslationUnits returns the units the XLIFF export would contain,
// under the same view authority, without creating a File.
func (s *Service) GetEntityTranslationUnits(ctx context.Context, query GetTranslationUnitsQuery) (TranslationUnits, error) {
	entityType, entityID, targetLocale, err := s.validateTranslationInterchangeTarget(ctx, query.Target, query.TargetLocale)
	if err != nil {
		return TranslationUnits{}, err
	}
	mode := InterchangeModeReplace
	if len(query.UnitHandles) != 0 {
		mode = InterchangeModePatch
	}
	if len(query.UnitHandles) > translationUnitsMaximum {
		return TranslationUnits{}, domainerr.InvalidField("unit_handles", "at most 1000 unit handles can be read at once")
	}
	unitHandles, err := validateXLIFFExportSelection(mode, query.UnitHandles)
	if err != nil {
		return TranslationUnits{}, err
	}

	result := TranslationUnits{TargetLocale: targetLocale}
	err = s.units.ReadCurrent(ctx, func(store Store) error {
		if err := store.Domains().RequireInterchangeView(ctx, entityType, entityID); err != nil {
			return err
		}
		plan, _, err := s.buildTranslationInterchangePlan(ctx, store, entityType, entityID, targetLocale)
		if err != nil {
			return err
		}
		state, err := store.Domains().LoadInterchangeTarget(ctx, entityType, entityID, targetLocale, plan)
		if err != nil {
			return err
		}
		if err := validateTranslationInterchangeTargetState(state, plan); err != nil {
			return err
		}
		document, err := translation.BuildXLIFFDocument(plan)
		if err != nil {
			return mapTranslationInterchangeBuildError(err)
		}
		selected, err := selectXLIFFUnits(translation.XLIFFWithTargets(*document, state.Targets), mode, unitHandles)
		if err != nil {
			return err
		}
		result.Units, err = translation.XLIFFUnitsMarkup(selected)
		if err != nil {
			return err
		}
		result.SourceLocale = plan.SourceLocale
		if state.Exists {
			revision := state.Revision
			result.TargetRevision = &revision
		}
		return nil
	})
	if err != nil {
		return TranslationUnits{}, err
	}
	return result, nil
}

// TranslationUnitEdit sets one unit's target in several locales. Source is the
// unit's current plain source text: it names the unit and refuses the write when
// the source has changed, so the caller needs no read first. Unit or Block
// narrows a Source that several units share.
type TranslationUnitEdit struct {
	Source  string
	Unit    string
	Block   string
	Targets map[string]string
}

// EditTranslationUnitsCommand applies edits across locales in one transaction.
type EditTranslationUnitsCommand struct {
	Target *Target
	Edits  []TranslationUnitEdit
}

// EditEntityTranslationUnits writes every locale or none. It follows each
// target's current revision because Source already guards what each edit means
// and other units keep their values.
func (s *Service) EditEntityTranslationUnits(ctx context.Context, command EditTranslationUnitsCommand) (map[string]XLIFFImport, error) {
	locales, err := editedTranslationLocales(command.Edits)
	if err != nil {
		return nil, err
	}
	var entityType, entityID string
	for _, locale := range locales {
		entityType, entityID, _, err = s.validateTranslationInterchangeTarget(ctx, command.Target, locale)
		if err != nil {
			return nil, err
		}
	}
	now := s.now().UTC()
	results, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[map[string]XLIFFImport, Store]{
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			return store.Domains().RequireInterchangeEdit(ctx, entityType, entityID)
		}),
		Change: func(ctx context.Context, store Store) (map[string]XLIFFImport, error) {
			results := make(map[string]XLIFFImport, len(locales))
			for _, locale := range locales {
				result, err := s.applyTranslationUnitEdits(ctx, store, entityType, entityID, locale, command.Edits, now)
				if err != nil {
					return nil, err
				}
				results[locale] = XLIFFImport{
					TargetRevision: result.Revision, Changed: result.Changed,
					AffectedUnitHandles: append([]string(nil), result.AffectedUnitHandles...),
				}
			}
			return results, nil
		},
		// The owning domain appends the locale-content record in its apply.
		Audit: func(map[string]XLIFFImport) mutation.Audit { return mutation.Unchanged() },
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func editedTranslationLocales(edits []TranslationUnitEdit) ([]string, error) {
	if len(edits) == 0 || len(edits) > translationUnitsMaximum {
		return nil, domainerr.InvalidField("edits", "between 1 and 1000 edits are required")
	}
	seen := map[string]struct{}{}
	for _, edit := range edits {
		if len(edit.Targets) == 0 {
			return nil, domainerr.InvalidField("edits", "each edit needs at least one target locale")
		}
		for locale := range edit.Targets {
			seen[locale] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(seen)), nil
}

func (s *Service) applyTranslationUnitEdits(
	ctx context.Context, store Store, entityType, entityID, locale string, edits []TranslationUnitEdit, now time.Time,
) (TranslationInterchangeApplyResult, error) {
	plan, _, err := s.buildTranslationInterchangePlan(ctx, store, entityType, entityID, locale)
	if err != nil {
		return TranslationInterchangeApplyResult{}, err
	}
	document, err := translation.BuildXLIFFDocument(plan)
	if err != nil {
		return TranslationInterchangeApplyResult{}, mapTranslationInterchangeBuildError(err)
	}
	units, err := translation.XLIFFUnitsMarkup(*document)
	if err != nil {
		return TranslationInterchangeApplyResult{}, err
	}
	targets := make(map[string]string, len(edits))
	for _, edit := range edits {
		text, requested := edit.Targets[locale]
		if !requested {
			continue
		}
		unit, err := resolveTranslationUnitEdit(units, edit)
		if err != nil {
			return TranslationInterchangeApplyResult{}, err
		}
		markup, err := translation.XLIFFTargetFromPlainText(unit.Source, text)
		if err != nil {
			return TranslationInterchangeApplyResult{}, domainerr.InvalidField("edits", "unit "+unit.ID+": "+err.Error())
		}
		targets[unit.ID] = markup
	}
	imported, err := translation.XLIFFDocumentFromMarkup(*document, targets)
	if err != nil {
		return TranslationInterchangeApplyResult{}, domainerr.InvalidField("edits", err.Error())
	}
	state, err := store.Domains().LoadInterchangeTarget(ctx, entityType, entityID, locale, plan)
	if err != nil {
		return TranslationInterchangeApplyResult{}, err
	}
	var expected *string
	if state.Exists {
		expected = &state.Revision
	}
	return s.applyImportedXLIFF(ctx, store, imported, TranslationInterchangeApply{
		EntityType: entityType, EntityID: entityID, TargetLocale: locale,
		Mode: InterchangeModePatch, ExpectedRevision: expected, Now: now,
	}, importedTargetsFromUnits)
}

func resolveTranslationUnitEdit(units []translation.XLIFFUnitMarkup, edit TranslationUnitEdit) (translation.XLIFFUnitMarkup, error) {
	source := strings.TrimSpace(edit.Source)
	var matches []translation.XLIFFUnitMarkup
	for _, unit := range units {
		if edit.Unit != "" && unit.ID != edit.Unit {
			continue
		}
		if edit.Block != "" && !UnitBelongsToBlock(unit, edit.Block) {
			continue
		}
		if strings.TrimSpace(translation.XLIFFMarkupPlainText(unit.Source)) == source {
			matches = append(matches, unit)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return translation.XLIFFUnitMarkup{}, domainerr.InvalidField("edits", fmt.Sprintf("no unit has the source text %q; the source may have changed, so read it with translation_units_get", edit.Source))
	default:
		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, match.ID)
		}
		return translation.XLIFFUnitMarkup{}, domainerr.InvalidField("edits", fmt.Sprintf("%d units have the source text %q; pass i or b to choose one of: %s", len(matches), edit.Source, strings.Join(ids, ", ")))
	}
}

// UnitBelongsToBlock reports whether a unit is the Block's own or sits in a
// section with that handle.
func UnitBelongsToBlock(unit translation.XLIFFUnitMarkup, block string) bool {
	return unit.GroupID == "block:"+block || strings.Contains(unit.ID, "block:"+block+":") ||
		strings.Contains(unit.ID, "section:"+block+":")
}

// PutTranslationUnitsCommand writes the targets of the named units of one
// target locale. Targets maps a unit handle to XLIFF 2.2 inline markup that
// keeps the unit's source codes. ExpectedTargetRevision, when set, must match
// the target's current revision.
type PutTranslationUnitsCommand struct {
	Target                 *Target
	TargetLocale           string
	ExpectedTargetRevision *string
	Targets                map[string]string
}

// PutEntityTranslationUnits applies the targets through the same validation,
// owning-domain apply and OG request as an XLIFF PATCH import.
func (s *Service) PutEntityTranslationUnits(ctx context.Context, command PutTranslationUnitsCommand) (XLIFFImport, error) {
	entityType, entityID, targetLocale, err := s.validateTranslationInterchangeTarget(ctx, command.Target, command.TargetLocale)
	if err != nil {
		return XLIFFImport{}, err
	}
	expectedRevision, err := validateExpectedTranslationTargetRevision(command.ExpectedTargetRevision)
	if err != nil {
		return XLIFFImport{}, err
	}
	if len(command.Targets) == 0 || len(command.Targets) > translationUnitsMaximum {
		return XLIFFImport{}, domainerr.InvalidField("units", "between 1 and 1000 unit targets are required")
	}
	for handle := range command.Targets {
		if strings.TrimSpace(handle) == "" || strings.TrimSpace(handle) != handle {
			return XLIFFImport{}, domainerr.InvalidField("units", "unit handles must be exact and non-empty")
		}
	}

	now := s.now().UTC()
	result, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[TranslationInterchangeApplyResult, Store]{
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			return store.Domains().RequireInterchangeEdit(ctx, entityType, entityID)
		}),
		Change: func(ctx context.Context, store Store) (TranslationInterchangeApplyResult, error) {
			plan, _, err := s.buildTranslationInterchangePlan(ctx, store, entityType, entityID, targetLocale)
			if err != nil {
				return TranslationInterchangeApplyResult{}, err
			}
			document, err := translation.BuildXLIFFDocument(plan)
			if err != nil {
				return TranslationInterchangeApplyResult{}, mapTranslationInterchangeBuildError(err)
			}
			imported, err := translation.XLIFFDocumentFromMarkup(*document, command.Targets)
			if err != nil {
				return TranslationInterchangeApplyResult{}, domainerr.InvalidField("units", err.Error())
			}
			return s.applyImportedXLIFF(ctx, store, imported, TranslationInterchangeApply{
				EntityType: entityType, EntityID: entityID, TargetLocale: targetLocale,
				Mode: InterchangeModePatch, ExpectedRevision: expectedRevision, Now: now,
			}, importedTargetsFromUnits)
		},
		// The owning domain appends the locale-content record in its apply.
		Audit: func(TranslationInterchangeApplyResult) mutation.Audit { return mutation.Unchanged() },
	})
	if err != nil {
		return XLIFFImport{}, err
	}
	return XLIFFImport{
		TargetRevision: result.Revision, Changed: result.Changed,
		AffectedUnitHandles: append([]string(nil), result.AffectedUnitHandles...),
	}, nil
}
