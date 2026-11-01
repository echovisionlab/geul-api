package application_test

import (
	"encoding/json"
	"errors"
	"slices"
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

func TestRegenerateQueuesOneJobPerMachineTranslationTarget(t *testing.T) {
	e := newEnv(t)
	e.seedProvider(t)
	entity := e.seedSeries(t)
	p := newPrincipal(t)

	jobs, err := e.service.RegenerateEntityTranslations(p.ctx, application.RegenerateEntityTranslationsCommand{Target: targetOf(entity)})

	require.NoError(t, err)
	locales := make([]string, 0, len(jobs))
	for _, job := range jobs {
		locales = append(locales, job.TargetLocale)
		require.Equal(t, application.JobQueued, job.Status)
		require.Equal(t, "en", job.SourceLocale)
		require.Equal(t, p.memberID, job.RequestedByMemberID)
	}
	require.Equal(t, []string{"ko", "ja", "fr"}, locales, "every enabled target but the source")
	enqueued := e.store.Enqueued()
	slices.Sort(enqueued)
	ids := []string{jobs[0].ID, jobs[1].ID, jobs[2].ID}
	slices.Sort(ids)
	require.Equal(t, ids, enqueued, "each queued job's command is enqueued with it")
	require.Equal(t, []string{
		"RequireEditable series/" + entity.EntityID, "RequireSourceLocaleEdit series/" + entity.EntityID,
	}, e.store.FakeDomains().Checks())
	require.Empty(t, e.audit.Records(), "jobs are operational rows, not audited content")
}

func TestRegenerateReusesTheActiveJobOfTheSameRequest(t *testing.T) {
	e := newEnv(t)
	e.seedProvider(t)
	entity := e.seedSeries(t)
	p := newPrincipal(t)
	first := e.queue(t, p, entity, "ko")

	second := e.queue(t, newPrincipal(t), entity, "ko")

	require.Equal(t, first.ID, second.ID)
	require.Len(t, e.store.Jobs(), 1)
	require.Equal(t, []string{first.ID}, e.store.Enqueued(), "a reused job is not enqueued again")
}

func TestRegenerateAsksTheLegalAuthorityOfAPolicy(t *testing.T) {
	e := newEnv(t)
	e.seedProvider(t)
	entity := e.store.SeedEntity(translationtest.Entity{
		EntityType: "terms", EntityID: uuid.NewString(), SourceLocale: "en",
		Source: &translation.SourceDocument{Title: "Terms"},
	})
	denied := domainerr.AdminRequired()
	e.store.FakeDomains().Deny = func(check, _, _ string) error {
		if check == "RequireLegalEditable" {
			return denied
		}
		return nil
	}

	_, err := e.service.RegenerateEntityTranslations(newPrincipal(t).ctx, application.RegenerateEntityTranslationsCommand{Target: targetOf(entity)})

	require.ErrorIs(t, err, denied)
	require.Equal(t, []string{
		"RequireEditable terms/" + entity.EntityID, "RequireLegalEditable terms/" + entity.EntityID,
	}, e.store.FakeDomains().Checks())
	require.Empty(t, e.store.Jobs())
}

func TestRegenerateRejectsAndQueuesNothing(t *testing.T) {
	denied := domainerr.ResourceNotFound("series", "masked")
	for name, test := range map[string]struct {
		arrange func(e *env, entity *translationtest.Entity) (application.RegenerateEntityTranslationsCommand, principal)
		code    connect.Code
	}{
		"without an active provider": {
			arrange: func(e *env, entity *translationtest.Entity) (application.RegenerateEntityTranslationsCommand, principal) {
				return application.RegenerateEntityTranslationsCommand{Target: targetOf(entity)}, newPrincipal(t)
			},
			code: connect.CodeFailedPrecondition,
		},
		"an unknown locale": {
			arrange: func(e *env, entity *translationtest.Entity) (application.RegenerateEntityTranslationsCommand, principal) {
				e.seedProvider(t)
				return application.RegenerateEntityTranslationsCommand{Target: targetOf(entity), Locales: []string{"xx"}}, newPrincipal(t)
			},
			code: connect.CodeInvalidArgument,
		},
		"a missing entity": {
			arrange: func(e *env, _ *translationtest.Entity) (application.RegenerateEntityTranslationsCommand, principal) {
				e.seedProvider(t)
				return application.RegenerateEntityTranslationsCommand{Target: &application.Target{EntityType: "series", EntityID: uuid.NewString()}}, newPrincipal(t)
			},
			code: connect.CodeNotFound,
		},
		"an unsupported entity type": {
			arrange: func(e *env, entity *translationtest.Entity) (application.RegenerateEntityTranslationsCommand, principal) {
				e.seedProvider(t)
				return application.RegenerateEntityTranslationsCommand{Target: &application.Target{EntityID: entity.EntityID}}, newPrincipal(t)
			},
			code: connect.CodeInvalidArgument,
		},
		"a denied source edit": {
			arrange: func(e *env, entity *translationtest.Entity) (application.RegenerateEntityTranslationsCommand, principal) {
				e.seedProvider(t)
				e.store.FakeDomains().Deny = func(check, _, _ string) error {
					if check == "RequireSourceLocaleEdit" {
						return denied
					}
					return nil
				}
				return application.RegenerateEntityTranslationsCommand{Target: targetOf(entity)}, newPrincipal(t)
			},
			code: connect.CodeNotFound,
		},
		"an anonymous caller": {
			arrange: func(e *env, entity *translationtest.Entity) (application.RegenerateEntityTranslationsCommand, principal) {
				e.seedProvider(t)
				return application.RegenerateEntityTranslationsCommand{Target: targetOf(entity)}, principal{ctx: t.Context()}
			},
			code: connect.CodeUnauthenticated,
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			entity := e.seedSeries(t)
			command, p := test.arrange(e, entity)

			_, err := e.service.RegenerateEntityTranslations(p.ctx, command)

			require.Equal(t, test.code, codeOf(err), "error %v", err)
			require.Empty(t, e.store.Jobs())
			require.Empty(t, e.store.Enqueued())
		})
	}
}

func TestCancelDeletesAnInFlightJobUnderTheEntityAuthority(t *testing.T) {
	e := newEnv(t)
	e.seedProvider(t)
	entity := e.seedSeries(t)
	job := e.queue(t, newPrincipal(t), entity, "ko")
	domains := e.store.FakeDomains()
	checksBefore := len(domains.Checks())

	events := captureTerminalEvents(t, func() {
		require.NoError(t, e.service.CancelTranslationJob(newPrincipal(t).ctx, application.CancelTranslationJobCommand{JobID: job.ID}))
	})

	require.Empty(t, e.store.Jobs())
	require.Equal(t, []string{
		"RequireEditable series/" + entity.EntityID, "RequireSourceLocaleEdit series/" + entity.EntityID,
	}, domains.Checks()[checksBefore:])
	require.Len(t, events, 1)
	require.Equal(t, "cancelled", events[0]["outcome"])

	err := e.service.CancelTranslationJob(newPrincipal(t).ctx, application.CancelTranslationJobCommand{JobID: job.ID})
	require.Equal(t, connect.CodeNotFound, codeOf(err), "a cancelled job is gone")
}

func TestCancelRequiresAnAuthenticatedRequesterBeforeAuthority(t *testing.T) {
	e := newEnv(t)
	e.seedProvider(t)
	job := e.queue(t, newPrincipal(t), e.seedSeries(t), "ko")
	checksBefore := len(e.store.FakeDomains().Checks())

	err := e.service.CancelTranslationJob(t.Context(), application.CancelTranslationJobCommand{JobID: job.ID})

	require.Equal(t, connect.CodeUnauthenticated, codeOf(err))
	require.Len(t, e.store.FakeDomains().Checks(), checksBefore)
	require.Len(t, e.store.Jobs(), 1)
}

func TestCancelAnswersADeniedAuthorityWithoutDeleting(t *testing.T) {
	e := newEnv(t)
	e.seedProvider(t)
	job := e.queue(t, newPrincipal(t), e.seedSeries(t), "ko")
	e.store.FakeDomains().Deny = func(string, string, string) error { return domainerr.NoPermission("edit", "series") }

	err := e.service.CancelTranslationJob(newPrincipal(t).ctx, application.CancelTranslationJobCommand{JobID: job.ID})

	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
	require.Len(t, e.store.Jobs(), 1)
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
		EntityType: "series", EntityID: entity.EntityID, SourceLocaleChanged: true, DocumentRevision: change.DocumentRevision,
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

func TestListTranslationJobsGivesAnAdministratorEveryJob(t *testing.T) {
	e := newEnv(t)
	e.seedProvider(t)
	e.queue(t, newPrincipal(t), e.seedSeries(t), "ko")
	e.queue(t, newPrincipal(t), e.seedSeries(t), "ja")
	checksBefore := len(e.store.FakeDomains().Checks())

	page, err := e.service.ListTranslationJobs(newPrincipal(t).ctx, application.ListTranslationJobsQuery{})

	require.NoError(t, err)
	require.Len(t, page.Jobs, 2)
	require.Equal(t, application.Pagination{Total: 2, Limit: 20}, page.Pagination)
	require.Len(t, e.store.FakeDomains().Checks(), checksBefore, "an administrator needs no entity check")
}

func TestListTranslationJobsGivesAManagerTheJobsOfOneEntity(t *testing.T) {
	e := newEnv(t)
	e.seedProvider(t)
	entity := e.seedSeries(t)
	job := e.queue(t, newPrincipal(t), entity, "ko")
	e.queue(t, newPrincipal(t), e.seedSeries(t), "ko")
	e.notAdmin()
	checksBefore := len(e.store.FakeDomains().Checks())

	page, err := e.service.ListTranslationJobs(newPrincipal(t).ctx, application.ListTranslationJobsQuery{
		Filters: jobFilters("series", entity.EntityID),
	})

	require.NoError(t, err)
	require.Len(t, page.Jobs, 1)
	require.Equal(t, job.ID, page.Jobs[0].ID)
	require.Equal(t, []string{"RequireJobRead series/" + entity.EntityID}, e.store.FakeDomains().Checks()[checksBefore:])
}

func TestListTranslationJobsRejectsANonAdministratorWithoutOneExactEntity(t *testing.T) {
	entityID := uuid.NewString()
	for name, test := range map[string]struct {
		filters []*commonv1.FilterSpec
		code    connect.Code
	}{
		"no scope":            {code: connect.CodePermissionDenied},
		"only an entity type": {filters: jobFilters("series", entityID)[:1], code: connect.CodePermissionDenied},
		"a Work":              {filters: jobFilters("work", entityID), code: connect.CodePermissionDenied},
		"a non UUID entity":   {filters: jobFilters("series", "series-a"), code: connect.CodePermissionDenied},
		"a repeated type": {
			filters: append(jobFilters("series", entityID), &commonv1.FilterSpec{Field: "entity_type", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "post"}),
			code:    connect.CodePermissionDenied,
		},
		"a non equality scope": {
			filters: []*commonv1.FilterSpec{
				{Field: "entity_type", Op: commonv1.FilterOp_FILTER_OP_IN, Values: []string{"series"}},
				{Field: "entity_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: entityID},
			},
			code: connect.CodePermissionDenied,
		},
		"an unsupported type": {filters: jobFilters("site_setting", entityID), code: connect.CodeInvalidArgument},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.notAdmin()

			_, err := e.service.ListTranslationJobs(newPrincipal(t).ctx, application.ListTranslationJobsQuery{Filters: test.filters})

			require.Equal(t, test.code, codeOf(err), "error %v", err)
			require.Empty(t, e.store.FakeDomains().Checks())
		})
	}
	t.Run("an anonymous caller", func(t *testing.T) {
		e := newEnv(t)
		_, err := e.service.ListTranslationJobs(t.Context(), application.ListTranslationJobsQuery{})
		require.Equal(t, connect.CodeUnauthenticated, codeOf(err))
	})
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
	require.Equal(t, []string{"RequireJobRead series/" + series.EntityID}, e.store.FakeDomains().Checks())
}

func settingsCommand(paths []string, settings, base translation.RuntimeSettings) application.UpdateTranslationSettingsCommand {
	return application.UpdateTranslationSettingsCommand{
		Settings: &settings, BaseSettings: &base, UpdateMask: &fieldmaskpb.FieldMask{Paths: paths},
	}
}

func TestUpdateTranslationSettingsAuditsTheChangedFieldsWithoutTermValues(t *testing.T) {
	e := newEnv(t)
	e.store.SeedSettings(translation.RuntimeSettings{DefaultLocale: "en", ProtectedTerms: []string{"Geul"}})

	updated, err := e.service.UpdateTranslationSettings(newPrincipal(t).ctx, settingsCommand(
		[]string{"default_locale", "protected_terms"},
		translation.RuntimeSettings{DefaultLocale: " ko ", ProtectedTerms: []string{"Geul", "Secret term"}},
		translation.RuntimeSettings{ProtectedTerms: []string{"Geul"}},
	))

	require.NoError(t, err)
	require.Equal(t, "ko", updated.DefaultLocale)
	require.Equal(t, []string{"Geul", "Secret term"}, updated.ProtectedTerms)
	stored, _ := e.store.Settings()
	require.True(t, stored.UpdatedAt.Equal(*updated.UpdatedAt))
	records := e.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditTranslationSettingsUpdated, records[0].Action)
	require.Equal(t, []string{"default_locale", "protected_terms"}, records[0].ChangedFields)
	encoded, err := json.Marshal(records[0])
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "Secret term")
}

func TestUpdateTranslationSettingsWithTheSameValuesChangesNothing(t *testing.T) {
	e := newEnv(t)
	updatedAt := time.Unix(1_700_000_000, 0).UTC()
	e.store.SeedSettings(translation.RuntimeSettings{DefaultLocale: "en", ProtectedTerms: []string{"Geul"}, UpdatedAt: &updatedAt})

	updated, err := e.service.UpdateTranslationSettings(newPrincipal(t).ctx, settingsCommand(
		[]string{"protected_terms"},
		translation.RuntimeSettings{ProtectedTerms: []string{" Geul "}},
		translation.RuntimeSettings{ProtectedTerms: []string{"Geul"}},
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
		[]string{"default_locale"}, translation.RuntimeSettings{DefaultLocale: "ko"}, translation.RuntimeSettings{},
	))

	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
	stored, _ := e.store.Settings()
	require.Equal(t, "en", stored.DefaultLocale)
}

func TestGetTranslationSettingsReportsWhetherGenerationIsAvailable(t *testing.T) {
	e := newEnv(t)
	view, err := e.service.GetTranslationSettings(newPrincipal(t).ctx)
	require.NoError(t, err)
	require.Equal(t, translation.DefaultLocale, view.Settings.DefaultLocale, "the default singleton is materialized")
	require.False(t, view.GenerationEnabled)
	require.NotNil(t, view.GenerationDisabledReason)

	e.seedProvider(t)
	view, err = e.service.GetTranslationSettings(newPrincipal(t).ctx)
	require.NoError(t, err)
	require.True(t, view.GenerationEnabled)
	require.Nil(t, view.GenerationDisabledReason)
}

func TestTranslationProvidersAreAuditedByChangedField(t *testing.T) {
	e := newEnv(t)
	p := newPrincipal(t)

	created, err := e.service.CreateTranslationProvider(p.ctx, application.CreateTranslationProviderCommand{
		Name: " DeepL ", Type: application.ProviderTypeDeepL, IsActive: true, Priority: 3,
		DeepLConfig: &application.DeepLConfigInput{APIKey: "secret-key"},
	})
	require.NoError(t, err)
	require.Equal(t, "DeepL", created.Name)
	cfg, err := created.DeepLConfig()
	require.NoError(t, err)
	require.Equal(t, translation.DefaultDeepLAPIBaseURL, cfg.APIBaseURL)

	name, priority := "DeepL Pro", int32(3)
	updated, err := e.service.UpdateTranslationProvider(p.ctx, application.UpdateTranslationProviderCommand{
		ID: created.ID, Name: &name, Priority: &priority, DeepLConfig: &application.DeepLConfigInput{},
	})
	require.NoError(t, err)
	require.Equal(t, "DeepL Pro", updated.Name)
	stored, _ := e.store.StoredProvider(created.ID)
	cfg, err = stored.DeepLConfig()
	require.NoError(t, err)
	require.Equal(t, "secret-key", cfg.APIKey, "an empty key keeps the stored one")

	_, err = e.service.UpdateTranslationProvider(p.ctx, application.UpdateTranslationProviderCommand{ID: created.ID, Name: &name})
	require.NoError(t, err)

	require.NoError(t, e.service.DeleteTranslationProvider(p.ctx, application.DeleteTranslationProviderCommand{ID: created.ID}))
	records := e.audit.Records()
	require.Len(t, records, 3, "the unchanged update appends nothing")
	require.Equal(t, sharedtelemetry.AuditTranslationProviderCreated, records[0].Action)
	require.Equal(t, created.ID, records[0].TargetID)
	require.Equal(t, sharedtelemetry.AuditTranslationProviderUpdated, records[1].Action)
	require.Equal(t, []string{"name"}, records[1].ChangedFields, "a config equal to the stored one is no change")
	require.Equal(t, sharedtelemetry.AuditTranslationProviderDeleted, records[2].Action)

	_, err = e.service.UpdateTranslationProvider(p.ctx, application.UpdateTranslationProviderCommand{ID: created.ID, Name: &name})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
	err = e.service.DeleteTranslationProvider(p.ctx, application.DeleteTranslationProviderCommand{ID: created.ID})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
}

func TestCreateTranslationProviderRejectsAnInvalidConfigAndHidesItsKey(t *testing.T) {
	e := newEnv(t)

	_, err := e.service.CreateTranslationProvider(newPrincipal(t).ctx, application.CreateTranslationProviderCommand{
		Name: "Broken", Type: application.ProviderTypeDeepL,
		DeepLConfig: &application.DeepLConfigInput{APIKey: "secret-key", APIBaseURL: "not a url"},
	})
	require.Equal(t, connect.CodeInvalidArgument, codeOf(err))

	_, err = e.service.CreateTranslationProvider(newPrincipal(t).ctx, application.CreateTranslationProviderCommand{Name: "Unknown"})
	require.Equal(t, connect.CodeInvalidArgument, codeOf(err))
	require.Empty(t, e.audit.Records())
}

func TestTranslationAdministrationRequiresAnAdministrator(t *testing.T) {
	e := newEnv(t)
	e.notAdmin()
	ctx := newPrincipal(t).ctx

	_, err := e.service.GetTranslationOverview(ctx)
	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
	e.checker.Deny(policyv1.TranslationProvider.List())
	_, err = e.service.ListTranslationProviders(ctx, application.ListTranslationProvidersQuery{})
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
