package application_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	"github.com/echovisionlab/geul-api/internal/translation/translationtest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func targetOf(entity *translationtest.Entity) *application.Target {
	return &application.Target{EntityType: entity.EntityType, EntityID: entity.EntityID}
}

// notAdmin makes the platform administrator check deny.
func (e *env) notAdmin() {
	e.checker.Deny(policyv1.Platform.IsAdmin())
}

func TestSetEntitySourceLocaleSwitchesAndAuditsTheOwningDomainRecord(t *testing.T) {
	e := newEnv(t)
	entity := e.seedSeries(t)
	entity = e.store.SeedEntity(translationtest.Entity{
		EntityType: entity.EntityType, EntityID: entity.EntityID, SourceLocale: "en",
		DocumentID: entity.DocumentID, DocumentRevision: entity.DocumentRevision, BlockLocales: []string{"ja"},
	})
	p := newPrincipal(t)

	change, err := e.service.SetEntitySourceLocale(p.ctx, application.SetEntitySourceLocaleCommand{
		Target: targetOf(entity), SourceLocale: "ko", ExpectedDocumentRevision: entity.DocumentRevision.String(),
	})

	require.NoError(t, err)
	require.True(t, change.Changed)
	require.Equal(t, []string{"en", "ja", "ko"}, change.ChangedLocales)
	stored, _ := e.store.Entity("series", entity.EntityID)
	require.Equal(t, "ko", stored.SourceLocale)
	require.Equal(t, stored.DocumentRevision.String(), change.DocumentRevision)
	records := e.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditPostUpdated, records[0].Action)
	require.Equal(t, "en", records[0].PreviousLocale)
	require.Equal(t, "ko", records[0].NewLocale)
	require.Equal(t, []translationtest.Signal{{
		EntityType: "series", EntityID: entity.EntityID, DocumentRevision: change.DocumentRevision,
	}}, e.store.Signals())
	require.Equal(t, []string{"series/" + entity.EntityID + "/ko series_source_locale_changed"}, e.store.FakeDomains().OGRequests())
	require.Equal(t, []string{
		"RequireSourceMutable series/" + entity.EntityID, "RequireEditable series/" + entity.EntityID,
		"RequireSourceLocaleEdit series/" + entity.EntityID,
	}, e.store.FakeDomains().Checks())
}

func TestSetEntitySourceLocaleToTheCurrentLocaleChangesNothing(t *testing.T) {
	e := newEnv(t)
	entity := e.seedSeries(t)

	change, err := e.service.SetEntitySourceLocale(newPrincipal(t).ctx, application.SetEntitySourceLocaleCommand{
		Target: targetOf(entity), SourceLocale: "en", ExpectedDocumentRevision: entity.DocumentRevision.String(),
	})

	require.NoError(t, err)
	require.False(t, change.Changed)
	require.Equal(t, entity.DocumentRevision.String(), change.DocumentRevision)
	require.Empty(t, e.audit.Records())
	require.Empty(t, e.store.Signals())
	require.Empty(t, e.store.FakeDomains().OGRequests())
}

func TestSetEntitySourceLocaleRejectsAndKeepsTheSource(t *testing.T) {
	for name, test := range map[string]struct {
		command func(entity *translationtest.Entity) application.SetEntitySourceLocaleCommand
		code    connect.Code
	}{
		"a stale document revision": {
			command: func(entity *translationtest.Entity) application.SetEntitySourceLocaleCommand {
				return application.SetEntitySourceLocaleCommand{Target: targetOf(entity), SourceLocale: "ko", ExpectedDocumentRevision: uuid.NewString()}
			},
			code: connect.CodeFailedPrecondition,
		},
		"an unknown locale": {
			command: func(entity *translationtest.Entity) application.SetEntitySourceLocaleCommand {
				return application.SetEntitySourceLocaleCommand{Target: targetOf(entity), SourceLocale: "xx", ExpectedDocumentRevision: entity.DocumentRevision.String()}
			},
			code: connect.CodeInvalidArgument,
		},
		"a non canonical revision": {
			command: func(entity *translationtest.Entity) application.SetEntitySourceLocaleCommand {
				return application.SetEntitySourceLocaleCommand{Target: targetOf(entity), SourceLocale: "ko", ExpectedDocumentRevision: strings.ToUpper(entity.DocumentRevision.String())}
			},
			code: connect.CodeInvalidArgument,
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			entity := e.seedSeries(t)

			_, err := e.service.SetEntitySourceLocale(newPrincipal(t).ctx, test.command(entity))

			require.Equal(t, test.code, codeOf(err), "error %v", err)
			stored, _ := e.store.Entity("series", entity.EntityID)
			require.Equal(t, "en", stored.SourceLocale)
			require.Empty(t, e.audit.Records())
			require.Empty(t, e.store.Signals())
		})
	}
}

func jobFilters(entityType, entityID string) []*commonv1.FilterSpec {
	return []*commonv1.FilterSpec{
		{Field: "entity_type", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: entityType},
		{Field: "entity_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: entityID},
	}
}

func TestEntityTranslationsReadTheStoredLocales(t *testing.T) {
	e := newEnv(t)
	entity := e.seedSeries(t)
	title := "밤 산책"
	stored, _ := e.store.Entity("series", entity.EntityID)
	stored.Entries["ko"] = application.Entry{EntityType: "series", EntityID: entity.EntityID, Locale: "ko", Title: &title}
	e.store.SeedEntity(stored)

	translations, err := e.service.ListEntityTranslations(newPrincipal(t).ctx, application.ListEntityTranslationsQuery{Target: targetOf(entity)})
	require.NoError(t, err)
	require.Equal(t, "en", translations.SourceLocale)
	require.Equal(t, []string{"en", "ko"}, []string{translations.Entries[0].Locale, translations.Entries[1].Locale})

	entry, err := e.service.GetEntityTranslation(newPrincipal(t).ctx, application.GetEntityTranslationQuery{Target: targetOf(entity), Locale: " ko "})
	require.NoError(t, err)
	require.Equal(t, title, *entry.Title)

	_, err = e.service.GetEntityTranslation(newPrincipal(t).ctx, application.GetEntityTranslationQuery{Target: targetOf(entity), Locale: "ja"})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
}

func TestEntityTranslationsOfANonAdministrator(t *testing.T) {
	e := newEnv(t)
	e.notAdmin()
	series := e.seedSeries(t)
	post := e.store.SeedEntity(translationtest.Entity{EntityType: "post", EntityID: uuid.NewString(), SourceLocale: "en"})

	_, err := e.service.ListEntityTranslations(newPrincipal(t).ctx, application.ListEntityTranslationsQuery{Target: targetOf(post)})
	require.Equal(t, connect.CodePermissionDenied, codeOf(err), "only Post Series entries are open to their managers")

	_, err = e.service.ListEntityTranslations(newPrincipal(t).ctx, application.ListEntityTranslationsQuery{Target: targetOf(series)})
	require.NoError(t, err)
	require.Equal(t, []string{"RequireTranslationRead series/" + series.EntityID}, e.store.FakeDomains().Checks())
}

func settingsCommand(paths []string, settings translation.RuntimeSettings) application.UpdateTranslationSettingsCommand {
	return application.UpdateTranslationSettingsCommand{
		Settings: &settings, UpdateMask: &fieldmaskpb.FieldMask{Paths: paths},
	}
}

func TestUpdateTranslationSettingsAuditsTheChangedFields(t *testing.T) {
	e := newEnv(t)
	e.store.SeedSettings(translation.RuntimeSettings{DefaultLocale: "en"})

	updated, err := e.service.UpdateTranslationSettings(newPrincipal(t).ctx, settingsCommand(
		[]string{"default_locale"}, translation.RuntimeSettings{DefaultLocale: " ko "},
	))

	require.NoError(t, err)
	require.Equal(t, "ko", updated.DefaultLocale)
	stored, _ := e.store.Settings()
	require.True(t, stored.UpdatedAt.Equal(*updated.UpdatedAt))
	records := e.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditTranslationSettingsUpdated, records[0].Action)
	require.Equal(t, []string{"default_locale"}, records[0].ChangedFields)
}

func TestUpdateTranslationSettingsWithTheSameValuesChangesNothing(t *testing.T) {
	e := newEnv(t)
	updatedAt := time.Unix(1_700_000_000, 0).UTC()
	e.store.SeedSettings(translation.RuntimeSettings{DefaultLocale: "en", UpdatedAt: &updatedAt})

	updated, err := e.service.UpdateTranslationSettings(newPrincipal(t).ctx, settingsCommand(
		[]string{"default_locale"}, translation.RuntimeSettings{DefaultLocale: " en "},
	))

	require.NoError(t, err)
	require.True(t, updatedAt.Equal(*updated.UpdatedAt))
	require.Empty(t, e.audit.Records())
}

func TestUpdateTranslationSettingsRequiresAFreshAdministrator(t *testing.T) {
	e := newEnv(t)
	e.store.SeedSettings(translation.RuntimeSettings{DefaultLocale: "en"})
	e.checker.Deny(policyv1.TranslationSettings.Update())

	_, err := e.service.UpdateTranslationSettings(newPrincipal(t).ctx, settingsCommand(
		[]string{"default_locale"}, translation.RuntimeSettings{DefaultLocale: "ko"},
	))

	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
	stored, _ := e.store.Settings()
	require.Equal(t, "en", stored.DefaultLocale)
}

func TestTranslationAdministrationRequiresAnAdministrator(t *testing.T) {
	e := newEnv(t)
	e.notAdmin()
	ctx := newPrincipal(t).ctx

	_, err := e.service.GetTranslationOverview(ctx)
	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
	e.checker.Deny(policyv1.TranslationSettings.View())
	_, err = e.service.GetTranslationSettings(ctx)
	require.Equal(t, connect.CodePermissionDenied, codeOf(err))

	locales, err := e.service.ListTranslationLocales(ctx)
	require.NoError(t, err, "every authenticated principal reads the enabled locales")
	require.Len(t, locales, 4)
	_, err = e.service.ListTranslationLocales(auth.WithUser(t.Context(), &auth.UserInfo{}))
	require.Equal(t, connect.CodeUnauthenticated, codeOf(err))
}

func xliffEnv(t *testing.T) (*env, *translationtest.Entity) {
	t.Helper()
	e := newEnv(t)
	return e, e.seedSeries(t)
}

func TestExportXLIFFAsksTheViewAuthorityAndStoresAShortLivedFile(t *testing.T) {
	e, entity := xliffEnv(t)

	exported, err := e.service.ExportEntityTranslationXLIFF(newPrincipal(t).ctx, application.ExportTranslationXLIFFCommand{
		Target: targetOf(entity), TargetLocale: "ko", Mode: application.InterchangeModeReplace,
	})

	require.NoError(t, err)
	require.Equal(t, "en", exported.SourceLocale)
	require.Nil(t, exported.TargetRevision, "an absent target has no revision")
	require.Equal(t, []string{"RequireInterchangeView series/" + entity.EntityID}, e.store.FakeDomains().Checks())
	require.Len(t, e.files.created, 1)
	require.Contains(t, string(e.files.created[0].Body), "Night walks")
	require.Equal(t, "series-"+entity.EntityID+"-ko.xlf", e.files.created[0].FileName)
}

func TestExportXLIFFRejectsBeforeReading(t *testing.T) {
	for name, command := range map[string]application.ExportTranslationXLIFFCommand{
		"PATCH without units":  {TargetLocale: "ko", Mode: application.InterchangeModePatch},
		"REPLACE with units":   {TargetLocale: "ko", Mode: application.InterchangeModeReplace, UnitHandles: []string{"entity:title"}},
		"an unspecified mode":  {TargetLocale: "ko"},
		"an unknown locale":    {TargetLocale: "xx", Mode: application.InterchangeModeReplace},
		"the source as target": {TargetLocale: "en", Mode: application.InterchangeModeReplace},
	} {
		t.Run(name, func(t *testing.T) {
			e, entity := xliffEnv(t)
			command.Target = targetOf(entity)

			_, err := e.service.ExportEntityTranslationXLIFF(newPrincipal(t).ctx, command)

			require.Equal(t, connect.CodeInvalidArgument, codeOf(err), "error %v", err)
			require.Empty(t, e.files.created)
		})
	}
}

// exportForImport exports entity's units for ko with an existing target of
// each, and offers the exported file as the upload whose ID it returns, as
// an author returns it unchanged.
func exportForImport(t *testing.T, e *env, entity *translationtest.Entity) string {
	t.Helper()
	e.store.FakeDomains().LoadTarget = func(_, _, _ string, plan *translation.ExtractionPlan) (application.TranslationInterchangeTargetState, error) {
		targets := map[string]translation.UnitResult{}
		for _, unit := range plan.Units {
			targets[unit.UnitID] = translation.UnitResult{UnitID: unit.UnitID, TranslatedText: unit.SourceText}
		}
		return application.TranslationInterchangeTargetState{Exists: true, Revision: "target-before-import", Targets: targets}, nil
	}
	exported, err := e.service.ExportEntityTranslationXLIFF(newPrincipal(t).ctx, application.ExportTranslationXLIFFCommand{
		Target: targetOf(entity), TargetLocale: "ko", Mode: application.InterchangeModeReplace,
	})
	require.NoError(t, err)
	require.Equal(t, "target-before-import", *exported.TargetRevision)
	fileID := uuid.NewString()
	e.files.upload = application.VerifiedTranslationXLIFF{
		FileID: fileID, Extension: "xlf", MimeType: "application/xliff+xml", Body: e.files.created[0].Body,
	}
	return fileID
}

func TestImportXLIFFAppliesUnderTheEditAuthority(t *testing.T) {
	e, entity := xliffEnv(t)
	fileID := exportForImport(t, e, entity)
	checksBefore := len(e.store.FakeDomains().Checks())

	imported, err := e.service.ImportEntityTranslationXLIFF(newPrincipal(t).ctx, application.ImportTranslationXLIFFCommand{
		Target: targetOf(entity), TargetLocale: "ko", Mode: application.InterchangeModeReplace, FileID: fileID,
	})

	require.NoError(t, err)
	require.True(t, imported.Changed)
	require.Equal(t, "target-after-import", imported.TargetRevision)
	require.Equal(t, []string{"RequireInterchangeEdit series/" + entity.EntityID}, e.store.FakeDomains().Checks()[checksBefore:])
	applied := e.store.FakeDomains().Imported()
	require.Len(t, applied, 1)
	require.Equal(t, "en", applied[0].SourceLocale)
	require.ElementsMatch(t, []string{"entity:title", "entity:summary"}, applied[0].UnitHandles)
	require.Equal(t, []string{"series/" + entity.EntityID + "/ko translation_xliff_import"}, e.store.FakeDomains().OGRequests())
}

func TestImportXLIFFRejectsWithoutApplying(t *testing.T) {
	for name, test := range map[string]struct {
		arrange func(e *env, entity *translationtest.Entity, command *application.ImportTranslationXLIFFCommand)
		code    connect.Code
	}{
		"a denied edit": {
			arrange: func(e *env, _ *translationtest.Entity, _ *application.ImportTranslationXLIFFCommand) {
				e.store.FakeDomains().Deny = func(check, entityType, entityID string) error {
					if check == "RequireInterchangeEdit" {
						return domainerr.ResourceNotFound(entityType, entityID)
					}
					return nil
				}
			},
			code: connect.CodeNotFound,
		},
		"another target locale": {
			arrange: func(_ *env, _ *translationtest.Entity, command *application.ImportTranslationXLIFFCommand) {
				command.TargetLocale = "ja"
			},
			code: connect.CodeInvalidArgument,
		},
		"a non canonical file": {
			arrange: func(_ *env, _ *translationtest.Entity, command *application.ImportTranslationXLIFFCommand) {
				command.FileID = strings.ToUpper(command.FileID)
			},
			code: connect.CodeInvalidArgument,
		},
		"a PATCH of every unit as REPLACE missing one": {
			arrange: func(e *env, _ *translationtest.Entity, command *application.ImportTranslationXLIFFCommand) {
				document, err := translation.UnmarshalXLIFFInterchange(e.files.upload.Body)
				require.NoError(t, err)
				document.File.Groups[0].TranslationUnit = document.File.Groups[0].TranslationUnit[:1]
				body, err := translation.MarshalXLIFF(document)
				require.NoError(t, err)
				e.files.upload.Body = body
			},
			code: connect.CodeInvalidArgument,
		},
		"a stale expected revision shape": {
			arrange: func(_ *env, _ *translationtest.Entity, command *application.ImportTranslationXLIFFCommand) {
				command.ExpectedTargetRevision = stringPointer(" padded ")
			},
			code: connect.CodeInvalidArgument,
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, entity := xliffEnv(t)
			fileID := exportForImport(t, e, entity)
			command := application.ImportTranslationXLIFFCommand{
				Target: targetOf(entity), TargetLocale: "ko", Mode: application.InterchangeModeReplace, FileID: fileID,
			}
			test.arrange(e, entity, &command)

			_, err := e.service.ImportEntityTranslationXLIFF(newPrincipal(t).ctx, command)

			require.Equal(t, test.code, codeOf(err), "error %v", err)
			require.Empty(t, e.store.FakeDomains().Imported())
			require.Empty(t, e.store.FakeDomains().OGRequests())
		})
	}
}

func TestImportXLIFFRollsBackWhenTheOGRequestFails(t *testing.T) {
	e, entity := xliffEnv(t)
	fileID := exportForImport(t, e, entity)
	failure := errors.New("OG planner unavailable")
	e.store.FakeDomains().OG = func(string, string, string, string) (bool, error) { return false, failure }

	_, err := e.service.ImportEntityTranslationXLIFF(newPrincipal(t).ctx, application.ImportTranslationXLIFFCommand{
		Target: targetOf(entity), TargetLocale: "ko", Mode: application.InterchangeModeReplace, FileID: fileID,
	})

	require.ErrorIs(t, err, failure)
	steps := e.units.Steps()
	require.Equal(t, "rollback", string(steps[len(steps)-1]))
}
