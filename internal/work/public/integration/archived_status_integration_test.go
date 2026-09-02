//go:build integration

package integration

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestArchivedWorkRemainsInDefaultPublicReadsIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	adminID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: adminID, Name: "Archived Public Work Admin"})
	adminMemberID := seedPublicAdminMemberIdentityLink(t, db, adminID, "Archived Public Work Admin")
	ctx := publicLegalAdminCtx(adminMemberID, adminID)
	manageWork := newPublicWorkManageService(t, db, adminID)
	isPresent := true
	title := "Archived Public Work " + uuid.NewString()

	created, err := manageWork.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title:     title,
		Type:      managev1.WorkType_WORK_TYPE_ARTICLE,
		Year:      2026,
		Month:     8,
		IsPresent: &isPresent,
		Document:  emptyPublicWorkDocument("en"),
	}))
	require.NoError(t, err)
	_, err = manageWork.PublishWork(ctx, connect.NewRequest(&managev1.PublishWorkRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.NoError(t, db.Table("work").Where("id = ?", created.Msg.Id).
		Update("status", managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()).Error)

	publicWork := newExtractedPublicWorkService(t, db, "")
	listed, err := publicWork.List(context.Background(), connect.NewRequest(&openv1.ListWorksRequest{
		Filters: []*commonv1.FilterSpec{{
			Field: "search",
			Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
			Value: title,
		}},
	}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.Works, 1)
	require.Equal(t, created.Msg.Id, listed.Msg.Works[0].Id)
	require.Equal(t, openv1.WorkStatus_WORK_STATUS_ARCHIVED, listed.Msg.Works[0].Status)

	fetched, err := publicWork.Get(context.Background(), connect.NewRequest(&openv1.GetWorkRequest{Slug: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, openv1.WorkStatus_WORK_STATUS_ARCHIVED, fetched.Msg.Work.Status)
}
