package maptheme_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	"github.com/echovisionlab/geul-api/internal/maptheme/mapthemetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// requireAudited asserts the records appended so far, each attributed to
// the request's member and correlated with its request.
func requireAudited(t *testing.T, f *fixture, actor principal, want ...[2]string) {
	t.Helper()
	var got [][2]string
	for _, record := range f.audit.Records() {
		got = append(got, [2]string{string(record.Action), record.TargetID})
		require.Equal(t, actor.memberID, record.MemberID)
		require.Equal(t, sharedtelemetry.RequestIDFromContext(actor.ctx), record.RequestID)
	}
	require.Equal(t, want, got)
}

func touch(t *testing.T, themeID string) policyv1.RelationshipMutation {
	t.Helper()
	mutation, err := policyv1.MapTheme.TouchPolicy(themeID)
	require.NoError(t, err)
	return mutation
}

func remove(t *testing.T, themeID string) policyv1.RelationshipMutation {
	t.Helper()
	mutation, err := policyv1.MapTheme.DeletePolicy(themeID)
	require.NoError(t, err)
	return mutation
}

func TestCreateMapThemeStoresTheThemeWithItsPolicyAndAudit(t *testing.T) {
	f := newFixture(t, nil, nil)
	admin := newPrincipal(t)
	request := createRequest(" Created ")
	request.Settings.CalloutScale = 1.1
	request.LightVariant.BackgroundColor = " #ABCDEF "

	created, err := f.themes.CreateMapTheme(admin.ctx, connect.NewRequest(request))

	require.NoError(t, err)
	require.Equal(t, "Created", created.Msg.Name)
	require.EqualValues(t, 1, created.Msg.Revision)
	require.Equal(t, float64(float32(1.1)), created.Msg.Settings.CalloutScale)
	require.Equal(t, "#ABCDEF", created.Msg.LightVariant.BackgroundColor)
	require.Equal(t, created.Msg.Id+":light", created.Msg.LightVariant.Id)
	stored, ok := f.store.Stored(created.Msg.Id)
	require.True(t, ok)
	require.Equal(t, "Created", stored.Snapshot.Name)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch(t, created.Msg.Id)}}, f.checker.Applied())
	require.Equal(t, []authztest.Check{checkOf(admin.identityID)(policyv1.MapTheme.Create())}, f.checker.Checks())
	requireAudited(t, f, admin, [2]string{string(sharedtelemetry.AuditMapThemeCreated), created.Msg.Id})
	require.Equal(t, []mutation.Step{
		mutation.StepAuthorizationLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepRelationships, mutation.StepCommit,
	}, f.units.Steps())
}

func TestCreateMapThemeValidatesTheSnapshotBeforeTheTransaction(t *testing.T) {
	f := newFixture(t, nil, nil)
	request := createRequest("Invalid pair")
	request.DarkVariant.BackgroundColor = "var(--arbitrary-css)"

	_, err := f.themes.CreateMapTheme(newPrincipal(t).ctx, connect.NewRequest(request))

	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.ErrorContains(t, err, "dark_variant.background_color")
	require.Empty(t, f.units.Steps())
	themes, err := f.store.Themes(t.Context())
	require.NoError(t, err)
	require.Len(t, themes, 1)
}

func TestCreateMapThemeRollsBackWhatAFailedStepLeaves(t *testing.T) {
	auditDown := errors.New("audit unavailable")
	writeDown := errors.New("SpiceDB write unavailable")
	for name, test := range map[string]struct {
		checker *authztest.Checker
		audit   *audittest.Recorder
		code    connect.Code
		cause   error
	}{
		"a caller without create authority": {checker: authztest.AllowAll().Deny(policyv1.MapTheme.Create()), code: connect.CodePermissionDenied},
		"an audit failure":                  {audit: &audittest.Recorder{Err: auditDown}, cause: auditDown},
		"a relationship write failure":      {checker: authztest.AllowAll().FailWrites(writeDown), cause: writeDown},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, test.checker, test.audit)

			_, err := f.themes.CreateMapTheme(newPrincipal(t).ctx, connect.NewRequest(createRequest("Rolled back")))

			if test.cause != nil {
				require.ErrorIs(t, err, test.cause)
			} else {
				require.Equal(t, test.code, connect.CodeOf(err))
			}
			themes, err := f.store.Themes(t.Context())
			require.NoError(t, err)
			require.Len(t, themes, 1, "only the default theme is stored")
			require.Empty(t, f.checker.Applied())
			require.Equal(t, mutation.StepRollback, f.units.Steps()[len(f.units.Steps())-1])
		})
	}
}

func TestCopyMapThemeLocksTheSourceAndStoresACopyUnderTheNewName(t *testing.T) {
	f := newFixture(t, nil, nil)
	snapshot := mapthemetest.Snapshot("Source")
	snapshot.Settings.CalloutScale = 1.1
	source := f.store.Seed(snapshot)
	admin := newPrincipal(t)

	copied, err := f.themes.CopyMapTheme(admin.ctx, connect.NewRequest(&managev1.CopyMapThemeRequest{Id: source.ID, Name: " Copy "}))

	require.NoError(t, err)
	require.NotEqual(t, source.ID, copied.Msg.Id)
	require.Equal(t, "Copy", copied.Msg.Name)
	require.EqualValues(t, 1, copied.Msg.Revision)
	stored, _ := f.store.Stored(copied.Msg.Id)
	want := source.Snapshot
	want.Name = "Copy"
	require.Equal(t, want, stored.Snapshot)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch(t, copied.Msg.Id)}}, f.checker.Applied())
	requireAudited(t, f, admin, [2]string{string(sharedtelemetry.AuditMapThemeCreated), copied.Msg.Id})
	require.Equal(t, []mutation.Step{
		mutation.StepAuthorizationLock, mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepRelationships, mutation.StepCommit,
	}, f.units.Steps())
}

func TestCopyMapThemeAnswersAMissingSourceBeforeAuthorization(t *testing.T) {
	f := newFixture(t, nil, nil)

	_, err := f.themes.CopyMapTheme(newPrincipal(t).ctx, connect.NewRequest(&managev1.CopyMapThemeRequest{Id: uuid.NewString(), Name: "Copy"}))

	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.Empty(t, f.checker.Checks())
	require.Equal(t, []mutation.Step{mutation.StepAuthorizationLock, mutation.StepLock, mutation.StepRollback}, f.units.Steps())
}

func TestCopyMapThemeRejectsAnEmptyName(t *testing.T) {
	f := newFixture(t, nil, nil)
	source := f.seed("Source")

	_, err := f.themes.CopyMapTheme(newPrincipal(t).ctx, connect.NewRequest(&managev1.CopyMapThemeRequest{Id: source.ID, Name: "  "}))

	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Empty(t, f.units.Steps())
}

func TestDeleteMapThemeRemovesTheThemeAndItsPolicy(t *testing.T) {
	f := newFixture(t, nil, nil)
	theme := f.seed("Deleted")
	admin := newPrincipal(t)

	response, err := f.themes.DeleteMapTheme(admin.ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: theme.ID}))

	require.NoError(t, err)
	require.True(t, response.Msg.Success)
	_, ok := f.store.Stored(theme.ID)
	require.False(t, ok)
	require.Equal(t, [][]policyv1.RelationshipMutation{{remove(t, theme.ID)}}, f.checker.Applied())
	require.Equal(t, []authztest.Check{checkOf(admin.identityID)(policyv1.MapTheme.Delete(theme.ID))}, f.checker.Checks())
	requireAudited(t, f, admin, [2]string{string(sharedtelemetry.AuditMapThemeDeleted), theme.ID})
}

func TestDeleteMapThemeRefusesTheSiteDefault(t *testing.T) {
	f := newFixture(t, nil, nil)
	defaultID := f.store.Default()

	_, err := f.themes.DeleteMapTheme(newPrincipal(t).ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: defaultID}))

	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.ErrorContains(t, err, "the current default or last map theme cannot be deleted; select another default first")
	require.ErrorIs(t, err, maptheme.ErrDefaultTheme)
	_, ok := f.store.Stored(defaultID)
	require.True(t, ok)
	require.Empty(t, f.checker.Applied())
	require.Empty(t, f.audit.Records())
}

func TestDeleteMapThemeAnswersAMissingThemeBeforeAuthorization(t *testing.T) {
	f := newFixture(t, &authztest.Checker{}, nil)

	_, err := f.themes.DeleteMapTheme(newPrincipal(t).ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: uuid.NewString()}))

	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.Empty(t, f.checker.Checks())
}

func TestDeleteMapThemeRechecksAuthorityAfterTheRootLock(t *testing.T) {
	f := newFixture(t, authztest.AllowAll(), nil)
	theme := f.seed("Kept")
	f.checker.Deny(policyv1.MapTheme.Delete(theme.ID))

	_, err := f.themes.DeleteMapTheme(newPrincipal(t).ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: theme.ID}))

	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, ok := f.store.Stored(theme.ID)
	require.True(t, ok)
	require.Equal(t, []mutation.Step{mutation.StepAuthorizationLock, mutation.StepLock, mutation.StepAccess, mutation.StepRollback}, f.units.Steps())
}

func TestDeleteMapThemeKeepsThePolicyWhenTheAuditFails(t *testing.T) {
	auditDown := errors.New("audit unavailable")
	f := newFixture(t, nil, &audittest.Recorder{Err: auditDown})
	theme := f.seed("Kept")

	_, err := f.themes.DeleteMapTheme(newPrincipal(t).ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: theme.ID}))

	require.ErrorIs(t, err, auditDown)
	_, ok := f.store.Stored(theme.ID)
	require.True(t, ok)
	require.Empty(t, f.checker.Applied())
}

func TestSetDefaultMapThemeLocksSettingsThenTheThemeAndAuditsTheSwitch(t *testing.T) {
	f := newFixture(t, nil, nil)
	theme := f.seed("Next default")
	admin := newPrincipal(t)

	response, err := f.themes.SetDefaultMapTheme(admin.ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: theme.ID}))

	require.NoError(t, err)
	require.Equal(t, theme.ID, response.Msg.DefaultMapThemeId)
	require.Equal(t, theme.ID, f.store.Default())
	require.Equal(t, []authztest.Check{checkOf(admin.identityID)(policyv1.MapTheme.Manage(theme.ID))}, f.checker.Checks())
	requireAudited(t, f, admin, [2]string{string(sharedtelemetry.AuditSiteSettingsUpdated), "1"})
	require.Equal(t, []string{"default_map_theme_id"}, f.audit.Records()[0].ChangedFields)
	require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepCommit}, f.units.Steps())
	require.Empty(t, f.checker.Applied())
}

func TestSetDefaultMapThemeToTheCurrentDefaultChangesNothing(t *testing.T) {
	f := newFixture(t, nil, nil)
	defaultID := f.store.Default()

	response, err := f.themes.SetDefaultMapTheme(newPrincipal(t).ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: defaultID}))

	require.NoError(t, err)
	require.Equal(t, defaultID, response.Msg.DefaultMapThemeId)
	require.Empty(t, f.audit.Records())
}

func TestSetDefaultMapThemeAnswersAMissingThemeAndADenialWithoutSwitching(t *testing.T) {
	f := newFixture(t, nil, nil)
	defaultID := f.store.Default()
	_, err := f.themes.SetDefaultMapTheme(newPrincipal(t).ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: uuid.NewString()}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.Empty(t, f.checker.Checks())

	theme := f.seed("Denied default")
	f.checker.Deny(policyv1.MapTheme.Manage(theme.ID))
	_, err = f.themes.SetDefaultMapTheme(newPrincipal(t).ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: theme.ID}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Equal(t, defaultID, f.store.Default())
	require.Empty(t, f.audit.Records())
}

func TestMapThemeMutationsRejectMalformedIDsBeforeTheTransaction(t *testing.T) {
	f := newFixture(t, nil, nil)
	ctx := newPrincipal(t).ctx
	for name, call := range map[string]func() error{
		"delete": func() error { return f.service.DeleteMapTheme(ctx, maptheme.DeleteMapThemeCommand{ID: "THEME"}) },
		"copy": func() error {
			_, err := f.service.CopyMapTheme(ctx, maptheme.CopyMapThemeCommand{ID: "", Name: "x"})
			return err
		},
		"set default": func() error {
			_, err := f.service.SetDefaultMapTheme(ctx, maptheme.SetDefaultMapThemeCommand{ThemeID: " x "})
			return err
		},
		"get": func() error { _, err := f.service.GetMapTheme(ctx, maptheme.GetMapThemeQuery{ID: "x"}); return err },
	} {
		require.Equal(t, connect.CodeInvalidArgument, codeOf(call()), name)
	}
	require.Empty(t, f.units.Steps())
	require.Empty(t, f.checker.Checks())
}
