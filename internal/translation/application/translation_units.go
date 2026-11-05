package application

import (
	"context"
	"strings"

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
