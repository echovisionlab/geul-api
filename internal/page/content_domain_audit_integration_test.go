//go:build integration

package page

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPageDomainAuditClosesCreateVersionDeleteBoundariesIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminIdentityID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminIdentityID, "Page audit admin")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, adminIdentityID, policyv1.Role.Admin())
	memberID := integrationMemberID(adminIdentityID)
	contributorID := seedPageAuditContributor(t, db, spiceDB)
	memberCtx := withPageAuditedRequestContext(t, workIntegrationAdminCtx(adminIdentityID))
	writer := apitelemetry.NewDurableWriter(db)
	contentBlocks, err := contentblock.NewGeneratedStore(filemedia.NewContentBlockFileReuseAuthorizer(spiceDB))
	require.NoError(t, err)
	pageService := NewAuditedPageService(
		db,
		newPageRuntimeForTest(db, "https://cdn.example.test"),
		newPageIntegrationFiles(db, spiceDB),
		noopAsyncPublisher{},
		&fakeIdentityManager{identity: postIntegrationIdentity(adminIdentityID, "en")},
		writer,
		spiceDB,
		WithPageContentBlockStore(contentBlocks),
		WithPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
		WithPageMenuTargets(noopPageMenuTargets{}),
	)
	internalService := NewInternalPageService(
		db,
		noopAsyncPublisher{},
		spiceDB,
		newPageRuntimeForTest(db, "https://cdn.example.test"),
		WithInternalPageDomainAuditWriter(writer),
		WithInternalPageContentBlockStore(contentBlocks),
		WithInternalPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
	)

	created, err := pageService.CreatePage(memberCtx, connect.NewRequest(&managev1.CreatePageRequest{Title: "Audited page"}))
	require.NoError(t, err)
	require.NotEmpty(t, created.Msg.Revision)
	checkpoint, err := internalService.CreatePageVersionCheckpoint(
		withPageAuditedCollabRequestContext(t, memberCtx),
		connect.NewRequest(&intrav1.CreatePageVersionCheckpointRequest{
			PageId:               created.Msg.Id,
			ExpectedRevision:     created.Msg.Revision,
			ContributorMemberIds: []string{contributorID},
			Locale:               "en",
		}),
	)
	require.NoError(t, err)
	require.True(t, checkpoint.Msg.Created)
	require.NotNil(t, checkpoint.Msg.VersionId)
	_, err = pageService.DeletePage(memberCtx, connect.NewRequest(&managev1.DeletePageRequest{Id: created.Msg.Id}))
	require.NoError(t, err)

	requirePageContentAuditRecords(t, db, created.Msg.Id, memberID, contributorID, []sharedtelemetry.AuditAction{
		sharedtelemetry.AuditPageCreated,
		sharedtelemetry.AuditPageUpdated,
		sharedtelemetry.AuditPageDeleted,
	})
}

func withPageAuditedRequestContext(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	user := auth.GetUser(ctx)
	require.NotNil(t, user)
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.99")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(ctx, requestContext), user)
}

func withPageAuditedCollabRequestContext(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(
		uuid.NewString(), sharedtelemetry.SystemActor{ServiceName: "geul-collab"},
	)
	require.NoError(t, err)
	return sharedtelemetry.WithRequestContext(ctx, requestContext)
}

func requirePageContentAuditRecords(
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
		WHERE target_type = 'page' AND target_id = ?
		ORDER BY occurred_at, audit_id
	`, targetID).Scan(&records).Error)
	require.Len(t, records, len(wantActions))
	for index, action := range wantActions {
		require.Equal(t, string(action), records[index].Action)
		if index == 1 {
			require.Equal(t, pq.StringArray{"version"}, records[index].ChangedFields)
			require.Equal(t, string(sharedtelemetry.ActorKindSystem), records[index].ActorKind)
			require.Equal(t, "geul-collab", pageAuditStringPointer(t, records[index].ActorService))
			require.Nil(t, records[index].ActorMemberID)
			require.NotEmpty(t, pageAuditStringPointer(t, records[index].VersionID))
			require.Equal(t, pq.StringArray{contributorID}, records[index].ContributorMemberIDs)
			continue
		}
		require.Equal(t, string(sharedtelemetry.ActorKindMember), records[index].ActorKind)
		require.Equal(t, memberID, pageAuditStringPointer(t, records[index].ActorMemberID))
		require.Nil(t, records[index].ActorService)
		require.Empty(t, records[index].ChangedFields)
		require.Nil(t, records[index].VersionID)
		require.Empty(t, records[index].ContributorMemberIDs)
	}
}

func seedPageAuditContributor(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient) string {
	t.Helper()
	identityID := uuid.NewString()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Page audit contributor")
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	return memberID
}

func pageAuditStringPointer(t *testing.T, value *string) string {
	t.Helper()
	require.NotNil(t, value)
	return *value
}
