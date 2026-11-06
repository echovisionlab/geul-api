package application_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	"github.com/echovisionlab/geul-api/internal/translation/translationtest"
)

func TestTranslationUnitsReadUnderViewAuthorityAndWriteOneUnit(t *testing.T) {
	e, entity := xliffEnv(t)

	units, err := e.service.GetEntityTranslationUnits(newPrincipal(t).ctx, application.GetTranslationUnitsQuery{
		Target: targetOf(entity), TargetLocale: "ko",
	})

	require.NoError(t, err)
	require.Equal(t, "en", units.SourceLocale)
	require.Nil(t, units.TargetRevision, "an absent target has no revision")
	require.Equal(t, []string{"RequireInterchangeView series/" + entity.EntityID}, e.store.FakeDomains().Checks())
	sources := map[string]string{}
	for _, unit := range units.Units {
		require.Nil(t, unit.Target)
		sources[unit.ID] = unit.Source
	}
	require.Equal(t, "Night walks", sources["entity:title"])
	checksBefore := len(e.store.FakeDomains().Checks())

	written, err := e.service.PutEntityTranslationUnits(newPrincipal(t).ctx, application.PutTranslationUnitsCommand{
		Target: targetOf(entity), TargetLocale: "ko", Targets: map[string]string{"entity:title": "밤 산책"},
	})

	require.NoError(t, err)
	require.True(t, written.Changed)
	require.Equal(t, []string{"RequireInterchangeEdit series/" + entity.EntityID}, e.store.FakeDomains().Checks()[checksBefore:])
	applied := e.store.FakeDomains().Imported()
	require.Len(t, applied, 1)
	require.Equal(t, application.InterchangeModePatch, applied[0].Mode)
	require.Equal(t, []string{"entity:title"}, applied[0].UnitHandles)
	require.Equal(t, "밤 산책", applied[0].Targets["entity:title"].TranslatedText)
	require.Equal(t, []string{"series/" + entity.EntityID + "/ko translation_units_put"}, e.store.FakeDomains().OGRequests())
}

func TestEditTranslationUnitsWritesEveryLocaleFromTheSourceTextAlone(t *testing.T) {
	e, entity := xliffEnv(t)

	written, err := e.service.EditEntityTranslationUnits(newPrincipal(t).ctx, application.EditTranslationUnitsCommand{
		Target: targetOf(entity),
		Edits: []application.TranslationUnitEdit{{
			Source:  "Night walks",
			Targets: map[string]string{"ko": "밤 산책", "ja": "夜の散歩"},
		}},
	})

	require.NoError(t, err)
	require.Len(t, written, 2)
	require.True(t, written["ko"].Changed)
	require.True(t, written["ja"].Changed)
	applied := e.store.FakeDomains().Imported()
	require.Len(t, applied, 2)
	require.Equal(t, "ja", applied[0].TargetLocale)
	require.Equal(t, "夜の散歩", applied[0].Targets["entity:title"].TranslatedText)
	require.Equal(t, "ko", applied[1].TargetLocale)
	require.Equal(t, "밤 산책", applied[1].Targets["entity:title"].TranslatedText)

	// A second edit needs no revision either: it follows the target's current one.
	_, err = e.service.EditEntityTranslationUnits(newPrincipal(t).ctx, application.EditTranslationUnitsCommand{
		Target: targetOf(entity),
		Edits:  []application.TranslationUnitEdit{{Source: "Night walks", Targets: map[string]string{"ko": "밤의 산책"}}},
	})
	require.NoError(t, err)
}

func TestEditTranslationUnitsPlacesASharedPhraseByItsField(t *testing.T) {
	e, entity := xliffEnv(t)
	const inTitleAndSummary = "i"
	edit := func(fieldUnit string) error {
		_, err := e.service.EditEntityTranslationUnits(newPrincipal(t).ctx, application.EditTranslationUnitsCommand{
			Target: targetOf(entity),
			Edits:  []application.TranslationUnitEdit{{SourceContains: inTitleAndSummary, FieldUnit: fieldUnit, Targets: map[string]string{"ko": "밤"}}},
		})
		return err
	}

	require.Equal(t, connect.CodeInvalidArgument, codeOf(edit("")), "two units hold the phrase, so its text alone cannot place it")
	require.Empty(t, e.store.FakeDomains().Imported())

	require.NoError(t, edit("entity:summary"))
	applied := e.store.FakeDomains().Imported()
	require.Len(t, applied, 1)
	require.Equal(t, "밤", applied[0].Targets["entity:summary"].TranslatedText)
	require.NotContains(t, applied[0].Targets, "entity:title")
}

func TestEditTranslationUnitsRefusesAChangedSourceWithoutApplying(t *testing.T) {
	for name, edits := range map[string][]application.TranslationUnitEdit{
		"a source that no longer matches": {{Source: "Old title", Targets: map[string]string{"ko": "x"}}},
		"no edits":                        nil,
		"no targets":                      {{Source: "Night walks"}},
		"the source locale as target":     {{Source: "Night walks", Targets: map[string]string{"en": "x"}}},
		"one good locale and one bad edit": {
			{Source: "Night walks", Targets: map[string]string{"ko": "밤 산책"}},
			{Source: "Old title", Targets: map[string]string{"ja": "x"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, entity := xliffEnv(t)

			_, err := e.service.EditEntityTranslationUnits(newPrincipal(t).ctx, application.EditTranslationUnitsCommand{Target: targetOf(entity), Edits: edits})

			require.Equal(t, connect.CodeInvalidArgument, codeOf(err), "error %v", err)
			require.Empty(t, e.store.FakeDomains().Imported())
		})
	}
}

func TestTranslationUnitsReadOnlyTheSelectedUnits(t *testing.T) {
	e, entity := xliffEnv(t)

	units, err := e.service.GetEntityTranslationUnits(newPrincipal(t).ctx, application.GetTranslationUnitsQuery{
		Target: targetOf(entity), TargetLocale: "ko", UnitHandles: []string{"entity:summary"},
	})

	require.NoError(t, err)
	require.Len(t, units.Units, 1)
	require.Equal(t, "entity:summary", units.Units[0].ID)
}

func TestPutTranslationUnitsRejectsWithoutApplying(t *testing.T) {
	for name, test := range map[string]struct {
		arrange func(e *env, command *application.PutTranslationUnitsCommand)
		code    connect.Code
	}{
		"no targets": {
			arrange: func(_ *env, command *application.PutTranslationUnitsCommand) { command.Targets = nil },
			code:    connect.CodeInvalidArgument,
		},
		"an unknown unit": {
			arrange: func(_ *env, command *application.PutTranslationUnitsCommand) {
				command.Targets = map[string]string{"entity:missing": "x"}
			},
			code: connect.CodeInvalidArgument,
		},
		"a code the source does not have": {
			arrange: func(_ *env, command *application.PutTranslationUnitsCommand) {
				command.Targets = map[string]string{"entity:title": `<pc id="r9">밤</pc> 산책`}
			},
			code: connect.CodeInvalidArgument,
		},
		"malformed markup": {
			arrange: func(_ *env, command *application.PutTranslationUnitsCommand) {
				command.Targets = map[string]string{"entity:title": "밤 <산책"}
			},
			code: connect.CodeInvalidArgument,
		},
		"the source as target": {
			arrange: func(_ *env, command *application.PutTranslationUnitsCommand) { command.TargetLocale = "en" },
			code:    connect.CodeInvalidArgument,
		},
		"a denied edit": {
			arrange: func(e *env, _ *application.PutTranslationUnitsCommand) {
				e.store.FakeDomains().Deny = func(check, entityType, entityID string) error {
					if check == "RequireInterchangeEdit" {
						return domainerr.ResourceNotFound(entityType, entityID)
					}
					return nil
				}
			},
			code: connect.CodeNotFound,
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, entity := xliffEnv(t)
			command := putTitleCommand(entity)
			test.arrange(e, &command)

			_, err := e.service.PutEntityTranslationUnits(newPrincipal(t).ctx, command)

			require.Equal(t, test.code, codeOf(err), "error %v", err)
			require.Empty(t, e.store.FakeDomains().Imported())
			require.Empty(t, e.store.FakeDomains().OGRequests())
		})
	}
}

func putTitleCommand(entity *translationtest.Entity) application.PutTranslationUnitsCommand {
	return application.PutTranslationUnitsCommand{
		Target: targetOf(entity), TargetLocale: "ko", Targets: map[string]string{"entity:title": "밤 산책"},
	}
}
