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
