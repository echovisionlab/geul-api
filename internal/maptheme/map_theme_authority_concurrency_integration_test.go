//go:build integration

package maptheme

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMapThemeMutationsRecheckAuthorityAfterRootLockIntegration(t *testing.T) {
	tests := []struct {
		name      string
		lock      func(*gorm.DB, string, string) error
		invoke    func(context.Context, *MapThemeService, *InternalMapService, string, string) error
		assertion func(*testing.T, *gorm.DB, string)
	}{
		{
			name: "create",
			lock: func(tx *gorm.DB, _, identityID string) error {
				return tx.Exec("SELECT id FROM kratos.identities WHERE id = ?::uuid FOR UPDATE", identityID).Error
			},
			invoke: func(ctx context.Context, service *MapThemeService, _ *InternalMapService, _, _ string) error {
				_, err := service.CreateMapTheme(ctx, connect.NewRequest(validCreateMapThemeRequest("Role-revoked create")))
				return err
			},
			assertion: func(t *testing.T, db *gorm.DB, themeID string) {
				var count int64
				require.NoError(t, db.Table("map_theme").Where("name = ?", "Role-revoked create").Count(&count).Error)
				require.Zero(t, count)
			},
		},
		{
			name: "copy",
			lock: lockMapThemeAuthorityRaceRoot,
			invoke: func(ctx context.Context, service *MapThemeService, _ *InternalMapService, themeID, _ string) error {
				_, err := service.CopyMapTheme(ctx, connect.NewRequest(&managev1.CopyMapThemeRequest{Id: themeID, Name: "Role-revoked copy"}))
				return err
			},
			assertion: func(t *testing.T, db *gorm.DB, _ string) {
				var count int64
				require.NoError(t, db.Table("map_theme").Where("name = ?", "Role-revoked copy").Count(&count).Error)
				require.Zero(t, count)
			},
		},
		{
			name: "update",
			lock: lockMapThemeAuthorityRaceRoot,
			invoke: func(_ context.Context, _ *MapThemeService, internal *InternalMapService, themeID, memberID string) error {
				_, err := internal.SaveMapThemeSnapshot(context.Background(), connect.NewRequest(&intrav1.SaveMapThemeSnapshotRequest{
					ThemeId: themeID, Locale: "und", ExpectedRevision: 1, Snapshot: validDocumentSnapshot("Role-revoked update"), ContributorMemberIds: []string{memberID},
				}))
				return err
			},
			assertion: func(t *testing.T, db *gorm.DB, themeID string) {
				var row struct {
					Name     string `gorm:"column:name"`
					Revision int64  `gorm:"column:edit_version"`
				}
				require.NoError(t, db.Table("map_theme").Select("name, edit_version").Where("id = ?", themeID).Take(&row).Error)
				require.NotEqual(t, "Role-revoked update", row.Name)
				require.EqualValues(t, 1, row.Revision)
			},
		},
		{
			name: "set default",
			lock: lockMapThemeAuthorityRaceRoot,
			invoke: func(ctx context.Context, service *MapThemeService, _ *InternalMapService, themeID, _ string) error {
				_, err := service.SetDefaultMapTheme(ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: themeID}))
				return err
			},
			assertion: func(t *testing.T, db *gorm.DB, themeID string) {
				var defaultID string
				require.NoError(t, db.Table("site_settings").Select("default_map_theme_id::text").Where("id = 1").Scan(&defaultID).Error)
				require.NotEqual(t, themeID, defaultID)
			},
		},
		{
			name: "delete",
			lock: lockMapThemeAuthorityRaceRoot,
			invoke: func(ctx context.Context, service *MapThemeService, _ *InternalMapService, themeID, _ string) error {
				_, err := service.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: themeID}))
				return err
			},
			assertion: func(t *testing.T, db *gorm.DB, themeID string) {
				var count int64
				require.NoError(t, db.Table("map_theme").Where("id = ?", themeID).Count(&count).Error)
				require.Equal(t, int64(1), count)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newConcurrentServiceIntegrationDB(t)
			identityID := integrationTestUUID()
			memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Map Theme role race")
			spiceDB := integrationSpiceDB(t)
			grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
			ctx := postIntegrationAdminCtx(identityID, nil)
			service := mapThemeServiceForTest(t, db, spiceDB)
			theme, err := service.CreateMapTheme(ctx, connect.NewRequest(validCreateMapThemeRequest("Role-revoked source "+integrationTestUUID())))
			require.NoError(t, err)
			internal := NewInternalMapService(db, spiceDB)

			lockTx := db.Begin()
			require.NoError(t, lockTx.Error)
			require.NoError(t, test.lock(lockTx, theme.Msg.Id, identityID))

			result := make(chan error, 1)
			go func() { result <- test.invoke(ctx, service, internal, theme.Msg.Id, memberID) }()
			requireMapThemeMutationStillWaiting(t, result)
			grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.User())
			require.NoError(t, lockTx.Commit().Error)

			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))
			test.assertion(t, db, theme.Msg.Id)
		})
	}
}

func lockMapThemeAuthorityRaceRoot(tx *gorm.DB, themeID, _ string) error {
	return tx.Exec("SELECT id FROM map_theme WHERE id = ?::uuid FOR UPDATE", themeID).Error
}

func requireMapThemeMutationStillWaiting(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		require.FailNow(t, "map theme mutation returned before its final root lock", "error: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
}
