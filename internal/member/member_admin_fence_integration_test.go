//go:build integration

package member

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// An administrator whose Identity was deactivated or banned keeps a session
// and the SpiceDB role until both catch up. Member administrator mutations
// check the identity fence inside their transaction, as campaign mutations
// do, so such a stale administrator is rejected although the early
// SpiceDB-only check still passes.
func TestMemberAdminMutationsRejectStaleAdministratorIdentityIntegration(t *testing.T) {
	t.Parallel()
	for name, stale := range map[string]func(*gorm.DB, memberAuditFixture) error{
		"deactivated identity": func(db *gorm.DB, admin memberAuditFixture) error {
			return db.Exec(`UPDATE kratos.identities SET state = 'inactive' WHERE id = ?::uuid`, admin.IdentityID).Error
		},
		"banned identity": func(db *gorm.DB, admin memberAuditFixture) error {
			return db.Exec(`UPDATE kratos.identities SET metadata_admin = '{"banned": true}'::jsonb WHERE id = ?::uuid`, admin.IdentityID).Error
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := testutil.NewIntegrationDB(t)
			admin, ctx := memberAdminAuditContext(t, db)
			target := createMemberAuditFixture(t, db, "stale-admin-target")
			tag := model.UserTag{ID: uuid.NewString(), Name: "existing"}
			require.NoError(t, db.Create(&tag).Error)
			handler := NewConnectHandler(memberAuditService(t, Deps{DB: db, Audit: apitelemetry.NewDurableWriter(db)}))
			require.NoError(t, stale(db, admin))

			_, err := handler.CreateMemberTag(ctx, connect.NewRequest(&managev1.CreateMemberTagRequest{Name: "must not exist"}))
			requireAdminRequired(t, err)
			_, err = handler.DeleteMemberTag(ctx, connect.NewRequest(&managev1.DeleteMemberTagRequest{Id: tag.ID}))
			requireAdminRequired(t, err)
			_, err = handler.SetMemberTags(ctx, connect.NewRequest(&managev1.SetMemberTagsRequest{MemberId: target.MemberID, TagIds: []string{tag.ID}}))
			requireAdminRequired(t, err)
			nickname := "renamed by a stale administrator"
			_, err = handler.UpdateMemberProfile(ctx, connect.NewRequest(&managev1.UpdateMemberProfileRequest{MemberId: target.MemberID, Nickname: &nickname}))
			requireAdminRequired(t, err)

			var tags, mappings, audits int64
			require.NoError(t, db.Model(&model.UserTag{}).Count(&tags).Error)
			require.EqualValues(t, 1, tags)
			require.NoError(t, db.Model(&model.UserTagMapping{}).Count(&mappings).Error)
			require.Zero(t, mappings)
			var stored model.Member
			require.NoError(t, db.Where("id = ?::uuid", target.MemberID).Take(&stored).Error)
			require.Equal(t, "stale-admin-target", stored.Nickname)
			require.NoError(t, db.Table("public.domain_audit").Count(&audits).Error)
			require.Zero(t, audits)
		})
	}
}

func requireAdminRequired(t *testing.T, err error) {
	t.Helper()
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	require.Equal(t, "admin access required", connectErr.Message())
}
