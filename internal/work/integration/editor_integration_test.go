//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestWorkServiceAdminOnlyAccessIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	adminID := testutil.IntegrationUUID()
	authorID := testutil.IntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, adminID, "Work Admin")
	testutil.SeedPostIntegrationIdentity(t, db, authorID, "Work Author")
	authorMemberID := testutil.PostIntegrationMemberID(authorID)

	stack := testutil.SetupOryStack(t)
	adminSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(adminID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), adminSubject, policyv1.Role.Admin())
	require.NoError(t, err)
	authorSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(authorID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), authorSubject, policyv1.Role.Author())
	require.NoError(t, err)
	workSvc := newWorkEditorIntegrationService(db, stack.SpiceDBClient, map[string]*auth.Identity{
		adminID:  workEditorIdentity(adminID, "Work Admin"),
		authorID: workEditorIdentity(authorID, "Work Author"),
	}, t)

	adminCtx := workIntegrationAdminCtx(adminID)
	authorCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(authorID),
		MemberID:      auth.MemberID(authorMemberID),
		SessionID:     auth.SessionID(testutil.IntegrationUUID()),
		Authenticated: true,
	})
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	workSlug := "work-admin-only-" + suffix
	isPresent := true

	created, err := workSvc.CreateWork(adminCtx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title:     "Work Admin Only " + suffix,
		Slug:      &workSlug,
		Type:      managev1.WorkType_WORK_TYPE_MUSIC_PROJECT,
		Year:      2026,
		Month:     4,
		IsPresent: &isPresent,
		Document:  emptyWorkIntegrationDocument("en"),
	}))
	require.NoError(t, err)
	_, err = workSvc.GetWork(authorCtx, connect.NewRequest(&managev1.GetWorkRequest{Id: created.Msg.Id}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.NoError(t, db.Model(&model.Work{}).
		Where("id = ?", created.Msg.Id).
		Update("status", managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()).Error)
	archived, err := workSvc.GetWork(authorCtx, connect.NewRequest(&managev1.GetWorkRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.WorkStatus_WORK_STATUS_ARCHIVED, archived.Msg.Status)

	unauthorizedSlug := "unauthorized-update-" + suffix
	_, err = workSvc.UpdateWork(authorCtx, connect.NewRequest(&managev1.UpdateWorkRequest{
		Id:   created.Msg.Id,
		Slug: &unauthorizedSlug,
	}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, err = workSvc.CheckWorkSlugAvailable(authorCtx, connect.NewRequest(&managev1.CheckWorkSlugAvailableRequest{
		Slug: "work-author-attempt-" + suffix,
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	available, err := workSvc.CheckWorkSlugAvailable(adminCtx, connect.NewRequest(&managev1.CheckWorkSlugAvailableRequest{
		Slug: "work-admin-available-" + suffix,
	}))
	require.NoError(t, err)
	require.True(t, available.Msg.Available)

	occupied, err := workSvc.CheckWorkSlugAvailable(adminCtx, connect.NewRequest(&managev1.CheckWorkSlugAvailableRequest{
		Slug: workSlug,
	}))
	require.NoError(t, err)
	require.False(t, occupied.Msg.Available)

	selfSlug, err := workSvc.CheckWorkSlugAvailable(adminCtx, connect.NewRequest(&managev1.CheckWorkSlugAvailableRequest{
		Slug: workSlug, ExcludeWorkId: &created.Msg.Id,
	}))
	require.NoError(t, err)
	require.True(t, selfSlug.Msg.Available)

	got, err := workSvc.GetWork(adminCtx, connect.NewRequest(&managev1.GetWorkRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, created.Msg.Id, got.Msg.Id)
}

func newWorkEditorIntegrationService(
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	identities map[string]*auth.Identity,
	t *testing.T,
) *workdomain.ConnectHandler {
	return workdomain.NewConnectHandler(newWorkServiceForTest(t, workdomain.Deps{DB: db, SpiceDB: spiceDB}))
}

func workEditorIdentity(id string, name string) *auth.Identity {
	return &auth.Identity{
		ID:         id,
		ExternalID: testutil.PostIntegrationMemberID(id),
		Traits:     map[string]interface{}{"name": name},
		State:      auth.KratosStateActive,
	}
}
