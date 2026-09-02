//go:build integration

package maptheme

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMapThemeCreateValidationLeavesDBUnchanged(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceDB := durableAudienceSpiceDB(t)
	ctx := mapThemeAdminContext(t)
	name := "Invalid pair " + integrationTestUUID()
	request := validCreateMapThemeRequest(name)
	request.DarkVariant.BackgroundColor = "var(--arbitrary-css)"

	_, err := mapThemeServiceForTest(t, db, spiceDB).CreateMapTheme(ctx, connect.NewRequest(request))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	var count int64
	require.NoError(t, db.Table("map_theme").Where("name = ?", name).Count(&count).Error)
	require.Zero(t, count)
}

func TestManageMapThemeAdminCanReadAndMutate(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceDB := durableAudienceSpiceDB(t)
	ctx := mapThemeAdminContext(t)
	service := mapThemeServiceForTest(t, db, spiceDB)
	list, err := service.ListMapThemes(ctx, connect.NewRequest(&managev1.ListMapThemesRequest{}))
	require.NoError(t, err)
	require.NotEmpty(t, list.Msg.DefaultMapThemeId)
	originalDefaultID := list.Msg.DefaultMapThemeId
	seedSchemaMapThemePolicyFixture(t, spiceDB, originalDefaultID)

	createdID := ""
	copiedID := ""
	t.Cleanup(func() {
		_, _ = service.SetDefaultMapTheme(ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: originalDefaultID}))
		if createdID != "" {
			_, _ = service.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: createdID}))
		}
		if copiedID != "" {
			_, _ = service.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: copiedID}))
		}
	})

	resolved, err := service.ResolveMapTheme(ctx, connect.NewRequest(&managev1.ResolveMapThemeRequest{Scheme: "light"}))
	require.NoError(t, err)
	require.Equal(t, originalDefaultID, resolved.Msg.ThemeId)

	created, err := service.CreateMapTheme(ctx, connect.NewRequest(validCreateMapThemeRequest("Admin source "+integrationTestUUID())))
	require.NoError(t, err)
	createdID = created.Msg.Id
	got, err := service.GetMapTheme(ctx, connect.NewRequest(&managev1.GetMapThemeRequest{Id: createdID}))
	require.NoError(t, err)
	require.Equal(t, createdID, got.Msg.Id)

	copied, err := service.CopyMapTheme(ctx, connect.NewRequest(&managev1.CopyMapThemeRequest{
		Id: createdID, Name: "Admin copy " + integrationTestUUID(),
	}))
	require.NoError(t, err)
	copiedID = copied.Msg.Id

	_, err = service.SetDefaultMapTheme(ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: copiedID}))
	require.NoError(t, err)
	_, err = service.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: createdID}))
	require.NoError(t, err)
	createdID = ""

	_, err = service.SetDefaultMapTheme(ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: originalDefaultID}))
	require.NoError(t, err)
	_, err = service.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: copiedID}))
	require.NoError(t, err)
	copiedID = ""
}

func TestManageMapThemeAuthenticatedMemberCanListAndResolve(t *testing.T) {
	db := newServiceIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	service := mapThemeServiceForTest(t, db, stack.SpiceDBClient)
	memberCtx := mapThemeMemberContext(stack.CreateUser(t, "user"))

	list, err := service.ListMapThemes(memberCtx, connect.NewRequest(&managev1.ListMapThemesRequest{}))
	require.NoError(t, err)
	require.NotEmpty(t, list.Msg.DefaultMapThemeId)

	resolved, err := service.ResolveMapTheme(memberCtx, connect.NewRequest(&managev1.ResolveMapThemeRequest{Scheme: "dark"}))
	require.NoError(t, err)
	require.Equal(t, list.Msg.DefaultMapThemeId, resolved.Msg.ThemeId)
	require.Equal(t, "dark", resolved.Msg.Scheme)
}

func TestMapThemeDefaultSwitchBlocksCurrentDeleteAndAllowsFormerDelete(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceDB := durableAudienceSpiceDB(t)
	ctx := mapThemeAdminContext(t)
	service := mapThemeServiceForTest(t, db, spiceDB)
	list, err := service.ListMapThemes(ctx, connect.NewRequest(&managev1.ListMapThemesRequest{}))
	require.NoError(t, err)
	originalDefaultID := list.Msg.DefaultMapThemeId
	require.NotEmpty(t, originalDefaultID)
	seedSchemaMapThemePolicyFixture(t, spiceDB, originalDefaultID)
	defaultIncluded := false
	for _, theme := range list.Msg.Themes {
		defaultIncluded = defaultIncluded || theme.Id == originalDefaultID
	}
	require.True(t, defaultIncluded)

	created, err := service.CreateMapTheme(ctx, connect.NewRequest(validCreateMapThemeRequest("Default "+integrationTestUUID())))
	require.NoError(t, err)
	set, err := service.SetDefaultMapTheme(ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, created.Msg.Id, set.Msg.DefaultMapThemeId)

	_, err = service.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: created.Msg.Id}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, err = service.SetDefaultMapTheme(ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: originalDefaultID}))
	require.NoError(t, err)
	_, err = service.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
}

func seedSchemaMapThemePolicyFixture(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	themeID string,
) {
	t.Helper()
	policy, err := policyv1.MapTheme.TouchPolicy(themeID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		deletePolicy, deleteErr := policyv1.MapTheme.DeletePolicy(themeID)
		require.NoError(t, deleteErr)
		_, deleteErr = spiceDB.ApplyRelationships(cleanupCtx, deletePolicy)
		require.NoError(t, deleteErr)
	})
}

func TestMapThemeResolveUsesOneRepeatableReadSnapshotAcrossDefaultSwitchAndDelete(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	spiceDB := durableAudienceSpiceDB(t)
	ctx := mapThemeAdminContext(t)
	service := mapThemeServiceForTest(t, db, spiceDB)
	list, err := service.ListMapThemes(ctx, connect.NewRequest(&managev1.ListMapThemesRequest{}))
	require.NoError(t, err)
	originalDefaultID := list.Msg.DefaultMapThemeId
	themeA, err := service.CreateMapTheme(ctx, connect.NewRequest(validCreateMapThemeRequest("Snapshot A "+integrationTestUUID())))
	require.NoError(t, err)
	themeB, err := service.CreateMapTheme(ctx, connect.NewRequest(validCreateMapThemeRequest("Snapshot B "+integrationTestUUID())))
	require.NoError(t, err)
	_, err = service.SetDefaultMapTheme(ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: themeA.Msg.Id}))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = service.SetDefaultMapTheme(ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: originalDefaultID}))
		_, _ = service.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: themeB.Msg.Id}))
	})

	reader := newConcurrentServiceIntegrationDB(t)
	callbackName := "map_theme_snapshot_" + integrationTestUUID()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	require.NoError(t, reader.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "site_settings" {
			once.Do(func() {
				close(started)
				<-release
			})
		}
	}))
	defer reader.Callback().Query().Remove(callbackName)

	type resolveResult struct {
		theme *model.MapTheme
		err   error
	}
	resolved := make(chan resolveResult, 1)
	go func() {
		theme, resolveErr := loadResolvedMapTheme(context.Background(), reader, "")
		resolved <- resolveResult{theme: theme, err: resolveErr}
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("default map theme read did not reach the snapshot barrier")
	}
	_, switchErr := service.SetDefaultMapTheme(ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: themeB.Msg.Id}))
	_, deleteErr := service.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: themeA.Msg.Id}))
	close(release)
	require.NoError(t, switchErr)
	require.NoError(t, deleteErr)
	result := <-resolved
	require.NoError(t, result.err)
	require.Equal(t, themeA.Msg.Id, result.theme.ID)
}

func TestMapThemeDetailCopyAndInternalLoadUseOneParentVariantSnapshot(t *testing.T) {
	for _, action := range []string{"detail", "copy", "internal_load"} {
		t.Run(action, func(t *testing.T) {
			db := newConcurrentServiceIntegrationDB(t)
			spiceDB := durableAudienceSpiceDB(t)
			admin := mapThemeAdminUser(t)
			ctx := mapThemeMemberContext(admin)
			service := mapThemeServiceForTest(t, db, spiceDB)
			created, err := service.CreateMapTheme(ctx, connect.NewRequest(validCreateMapThemeRequest("Snapshot source "+integrationTestUUID())))
			require.NoError(t, err)

			copiedID := ""
			t.Cleanup(func() {
				if copiedID != "" {
					_, _ = service.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: copiedID}))
				}
				_, _ = service.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: created.Msg.Id}))
			})

			reader := newConcurrentServiceIntegrationDB(t)
			callbackName := "map_theme_parent_variant_snapshot_" + integrationTestUUID()
			started := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			require.NoError(t, reader.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement.Table == "map_theme" {
					once.Do(func() {
						close(started)
						<-release
					})
				}
			}))
			defer reader.Callback().Query().Remove(callbackName)

			type readResult struct {
				name          string
				revision      int64
				calloutScale  float64
				lightColor    string
				darkColor     string
				copiedThemeID string
				err           error
			}
			resultCh := make(chan readResult, 1)
			go func() {
				result := readResult{}
				switch action {
				case "detail":
					response, readErr := mapThemeServiceForTest(t, reader, spiceDB).GetMapTheme(ctx, connect.NewRequest(&managev1.GetMapThemeRequest{Id: created.Msg.Id}))
					result.err = readErr
					if readErr == nil {
						result.name = response.Msg.Name
						result.revision = response.Msg.Revision
						result.calloutScale = response.Msg.Settings.CalloutScale
						result.lightColor = response.Msg.LightVariant.BackgroundColor
						result.darkColor = response.Msg.DarkVariant.BackgroundColor
					}
				case "copy":
					response, readErr := mapThemeServiceForTest(t, reader, spiceDB).CopyMapTheme(ctx, connect.NewRequest(&managev1.CopyMapThemeRequest{
						Id: created.Msg.Id, Name: "Snapshot copy " + integrationTestUUID(),
					}))
					result.err = readErr
					if readErr == nil {
						result.revision = response.Msg.Revision
						result.calloutScale = response.Msg.Settings.CalloutScale
						result.lightColor = response.Msg.LightVariant.BackgroundColor
						result.darkColor = response.Msg.DarkVariant.BackgroundColor
						result.copiedThemeID = response.Msg.Id
					}
				case "internal_load":
					response, readErr := NewInternalMapService(reader, spiceDB).LoadMapThemeSnapshot(context.Background(), connect.NewRequest(&intrav1.LoadMapThemeSnapshotRequest{ThemeId: created.Msg.Id, Locale: "und"}))
					result.err = readErr
					if readErr == nil {
						result.name = response.Msg.Snapshot.Name
						result.revision = response.Msg.Revision
						result.calloutScale = response.Msg.Snapshot.Settings.CalloutScale
						result.lightColor = response.Msg.Snapshot.LightVariant.BackgroundColor
						result.darkColor = response.Msg.Snapshot.DarkVariant.BackgroundColor
					}
				}
				resultCh <- result
			}()

			select {
			case <-started:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("map theme parent read did not reach the snapshot barrier")
			}

			updated := validDocumentSnapshot("Snapshot updated " + integrationTestUUID())
			updated.Settings.CalloutScale = 2
			updated.LightVariant.BackgroundColor = "#101010"
			updated.DarkVariant.BackgroundColor = "#202020"
			saveResult := make(chan error, 1)
			go func() {
				_, saveErr := NewInternalMapService(db, spiceDB).SaveMapThemeSnapshot(
					context.Background(),
					connect.NewRequest(&intrav1.SaveMapThemeSnapshotRequest{
						ThemeId:              created.Msg.Id,
						Locale:               "und",
						ExpectedRevision:     1,
						Snapshot:             updated,
						ContributorMemberIds: []string{admin.MemberID},
					}),
				)
				saveResult <- saveErr
			}()
			close(release)
			require.NoError(t, <-saveResult)

			var result readResult
			select {
			case result = <-resultCh:
			case <-time.After(5 * time.Second):
				t.Fatal("map theme snapshot read did not complete")
			}
			copiedID = result.copiedThemeID
			require.NoError(t, result.err)
			require.EqualValues(t, 1, result.revision)
			require.InDelta(t, created.Msg.Settings.CalloutScale, result.calloutScale, 0.0001)
			require.Equal(t, created.Msg.LightVariant.BackgroundColor, result.lightColor)
			require.Equal(t, created.Msg.DarkVariant.BackgroundColor, result.darkColor)
			if action != "copy" {
				require.Equal(t, created.Msg.Name, result.name)
			}

			persisted, loadErr := NewInternalMapService(db, spiceDB).LoadMapThemeSnapshot(context.Background(), connect.NewRequest(&intrav1.LoadMapThemeSnapshotRequest{ThemeId: created.Msg.Id, Locale: "und"}))
			require.NoError(t, loadErr)
			require.EqualValues(t, 2, persisted.Msg.Revision)
			require.Equal(t, updated.Name, persisted.Msg.Snapshot.Name)
			require.InDelta(t, updated.Settings.CalloutScale, persisted.Msg.Snapshot.Settings.CalloutScale, 0.0001)
			require.Equal(t, updated.LightVariant.BackgroundColor, persisted.Msg.Snapshot.LightVariant.BackgroundColor)
			require.Equal(t, updated.DarkVariant.BackgroundColor, persisted.Msg.Snapshot.DarkVariant.BackgroundColor)
		})
	}
}

func TestMapThemeUnknownResolveFallsBackToCurrentDefaultForBothSchemes(t *testing.T) {
	db := newServiceIntegrationDB(t)
	service := mapThemeServiceForTest(t, db, durableAudienceSpiceDB(t))
	list, err := service.ListMapThemes(adminCtx(), connect.NewRequest(&managev1.ListMapThemesRequest{}))
	require.NoError(t, err)
	for _, scheme := range []string{"light", "dark"} {
		unknown := integrationTestUUID()
		resolved, resolveErr := service.ResolveMapTheme(adminCtx(), connect.NewRequest(&managev1.ResolveMapThemeRequest{ThemeId: &unknown, Scheme: scheme}))
		require.NoError(t, resolveErr)
		require.Equal(t, list.Msg.DefaultMapThemeId, resolved.Msg.ThemeId)
		require.Equal(t, scheme, resolved.Msg.Scheme)
		require.NotNil(t, resolved.Msg.Variant)
	}
}
