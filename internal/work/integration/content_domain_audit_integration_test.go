//go:build integration

package integration

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestWorkDomainAuditClosesCreateVersionDeleteBoundariesIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminIdentityID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminIdentityID, "Work audit admin")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, adminIdentityID, policyv1.Role.Admin())
	memberID := integrationMemberID(adminIdentityID)
	contributorID := seedWorkAuditContributor(t, db, spiceDB)
	memberCtx := withWorkAuditedRequestContext(t, workIntegrationAdminCtx(adminIdentityID))
	writer := apitelemetry.NewDurableWriter(db)
	files := &filemedia.FileService{}
	contentBlocks, err := contentblock.NewGeneratedStore(filemedia.NewContentBlockFileReuseAuthorizer(spiceDB))
	require.NoError(t, err)
	workService := workdomain.NewAuditedWorkService(
		db,
		newWorkRuntimeForTest(db, ""),
		spiceDB,
		&fakeIdentityManager{identity: postIntegrationIdentity(adminIdentityID, "en")},
		noopAsyncPublisher{},
		writer,
		workdomain.WithWorkContentBlockStore(contentBlocks),
		workdomain.WithWorkContentBlockMediaHydrator(files),
		workdomain.WithWorkMemberSummaryLoader(workadapter.NewMemberSummaries(db, "")),
	)
	internalService := workdomain.NewInternalWorkService(
		db,
		noopAsyncPublisher{},
		newWorkRuntimeForTest(db, ""),
		spiceDB,
		workdomain.WithInternalWorkDomainAuditWriter(writer),
		workdomain.WithInternalWorkContentBlockStore(contentBlocks),
		workdomain.WithInternalWorkCheckpoints(testcollaboration.NewCheckpoints(db, spiceDB)),
	)

	isPresent := true
	created, err := workService.CreateWork(memberCtx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title:     "Audited work",
		Type:      managev1.WorkType_WORK_TYPE_MUSIC_PROJECT,
		Year:      2026,
		Month:     8,
		IsPresent: &isPresent,
		Document:  emptyWorkIntegrationDocument("en"),
	}))
	require.NoError(t, err)
	checkpoint, err := internalService.CreateWorkVersionCheckpoint(
		withWorkAuditedCollabRequestContext(t, memberCtx),
		connect.NewRequest(&intrav1.CreateWorkVersionCheckpointRequest{
			WorkId:               created.Msg.Id,
			Locale:               "en",
			ExpectedRevision:     created.Msg.Revision,
			ContributorMemberIds: []string{contributorID},
		}),
	)
	require.NoError(t, err)
	require.True(t, checkpoint.Msg.Created)
	require.NotNil(t, checkpoint.Msg.VersionId)
	_, err = workService.DeleteWork(memberCtx, connect.NewRequest(&managev1.DeleteWorkRequest{Id: created.Msg.Id}))
	require.NoError(t, err)

	requireWorkContentAuditRecords(t, db, created.Msg.Id, memberID, contributorID, []sharedtelemetry.AuditAction{
		sharedtelemetry.AuditWorkCreated,
		sharedtelemetry.AuditWorkUpdated,
		sharedtelemetry.AuditWorkDeleted,
	})
}

func withWorkAuditedRequestContext(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	user := auth.GetUser(ctx)
	require.NotNil(t, user)
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.99")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(ctx, requestContext), user)
}

func withWorkAuditedCollabRequestContext(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(
		uuid.NewString(), sharedtelemetry.SystemActor{ServiceName: "geul-collab"},
	)
	require.NoError(t, err)
	return sharedtelemetry.WithRequestContext(ctx, requestContext)
}

func requireWorkContentAuditRecords(
	t *testing.T,
	db *gorm.DB,
	targetID string,
	memberID string,
	contributorID string,
	wantActions []sharedtelemetry.AuditAction,
) {
	t.Helper()
	var records []struct {
		Action               string
		ActorKind            string
		ActorMemberID        *string
		ActorService         *string
		ChangedFields        pq.StringArray `gorm:"type:text[]"`
		VersionID            *string
		ContributorMemberIDs pq.StringArray `gorm:"type:uuid[]"`
	}
	require.NoError(t, db.Raw(`
		SELECT action, actor_kind, actor_member_id::text AS actor_member_id,
		       actor_service,
		       ARRAY(SELECT jsonb_array_elements_text(attributes->'changed_fields')) AS changed_fields,
		       attributes->>'version_id' AS version_id,
		       ARRAY(SELECT jsonb_array_elements_text(attributes->'contributor_member_ids')) AS contributor_member_ids
		FROM public.domain_audit
		WHERE target_type = 'work' AND target_id = ?
		ORDER BY occurred_at, audit_id
	`, targetID).Scan(&records).Error)
	require.Len(t, records, len(wantActions))
	for index, action := range wantActions {
		require.Equal(t, string(action), records[index].Action)
		if index == 1 {
			require.Equal(t, pq.StringArray{"version"}, records[index].ChangedFields)
			require.Equal(t, string(sharedtelemetry.ActorKindSystem), records[index].ActorKind)
			require.Equal(t, "geul-collab", workAuditStringPointer(t, records[index].ActorService))
			require.Nil(t, records[index].ActorMemberID)
			require.NotEmpty(t, workAuditStringPointer(t, records[index].VersionID))
			require.Equal(t, pq.StringArray{contributorID}, records[index].ContributorMemberIDs)
			continue
		}
		require.Equal(t, string(sharedtelemetry.ActorKindMember), records[index].ActorKind)
		require.Equal(t, memberID, workAuditStringPointer(t, records[index].ActorMemberID))
		require.Nil(t, records[index].ActorService)
		require.Empty(t, records[index].ChangedFields)
		require.Nil(t, records[index].VersionID)
		require.Empty(t, records[index].ContributorMemberIDs)
	}
}

func seedWorkAuditContributor(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient) string {
	t.Helper()
	memberID := uuid.NewString()
	testutil.InsertAuthorizedDocumentContributor(t, db, spiceDB, memberID)
	return memberID
}

func workAuditStringPointer(t *testing.T, value *string) string {
	t.Helper()
	require.NotNil(t, value)
	return *value
}
