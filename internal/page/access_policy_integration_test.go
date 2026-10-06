//go:build integration

package page

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestPageAccessPolicyManagementIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := pageIntegrationAdmin(t, db)
	ctx = withPageAuditedRequestContext(t, ctx)
	service := NewAuditedPageService(db, newPageRuntimeForTest(db, "https://cdn.example.com"),
		newPageIntegrationFiles(db, spiceDB), noopAsyncPublisher{}, nil, apitelemetry.NewDurableWriter(db), spiceDB,
		WithPageContentBlockStore(newPageIntegrationContentBlockStore(t, spiceDB)),
		WithPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}), WithPageMenuTargets(noopPageMenuTargets{}))
	created, err := service.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{Title: "Access policy settings"}))
	require.NoError(t, err)
	pageID := created.Msg.Id
	require.Equal(t, commonv1.PageAccessMode_PAGE_ACCESS_MODE_PUBLIC, created.Msg.AccessPolicy.Mode)
	tagID := uuid.NewString()
	require.NoError(t, db.Create(&model.UserTag{ID: tagID, Name: "Page policy tag " + tagID}).Error)
	conditions := &commonv1.PageAccessPolicy{
		Mode:         commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS,
		AllowedRoles: []policyv1.AuthorizationRole{policyv1.AuthorizationRole_ADMIN, policyv1.AuthorizationRole_AUTHOR, policyv1.AuthorizationRole_AUTHOR},
		UserTagIds:   []string{tagID, tagID}, NewsletterSubscriber: true, Match: commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ALL,
	}
	normalized, err := pageaccess.Normalize(conditions)
	require.NoError(t, err)
	updated, err := service.UpdatePage(ctx, connect.NewRequest(&managev1.UpdatePageRequest{Id: pageID, AccessPolicy: conditions}))
	require.NoError(t, err)
	require.True(t, updated.Msg.Changed)
	require.True(t, proto.Equal(normalized, updated.Msg.AccessPolicy))
	for _, request := range []*managev1.UpdatePageRequest{{Id: pageID}, {Id: pageID, AccessPolicy: conditions}} {
		unchanged, err := service.UpdatePage(ctx, connect.NewRequest(request))
		require.NoError(t, err)
		require.False(t, unchanged.Msg.Changed)
		require.True(t, proto.Equal(normalized, unchanged.Msg.AccessPolicy))
		require.True(t, proto.Equal(updated.Msg.UpdatedAt, unchanged.Msg.UpdatedAt))
	}
	fetched, err := service.GetPage(ctx, connect.NewRequest(&managev1.GetPageRequest{Id: pageID}))
	require.NoError(t, err)
	require.True(t, proto.Equal(normalized, fetched.Msg.AccessPolicy))
	listed, err := service.ListPagesAdmin(ctx, connect.NewRequest(&managev1.ListPagesAdminRequest{}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.Pages, 1)
	require.True(t, proto.Equal(normalized, listed.Msg.Pages[0].AccessPolicy))
	var auditFields []string
	require.NoError(t, db.Raw("SELECT jsonb_array_elements_text(attributes->'changed_fields') FROM domain_audit WHERE target_type = 'page' AND target_id = ? AND action = 'page.updated'", pageID).Scan(&auditFields).Error)
	require.Equal(t, []string{"access_policy"}, auditFields)
	restricted, err := service.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{Title: "Created with conditions", AccessPolicy: conditions}))
	require.NoError(t, err)
	require.True(t, proto.Equal(normalized, restricted.Msg.AccessPolicy))
	for _, invalid := range []*commonv1.PageAccessPolicy{
		{Mode: commonv1.PageAccessMode(999)},
		{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, AllowedRoles: []policyv1.AuthorizationRole{policyv1.AuthorizationRole_USER}},
		{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS},
		{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, UserTagIds: []string{"invalid"}},
		{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, UserTagIds: []string{uuid.NewString()}},
	} {
		_, err := service.UpdatePage(ctx, connect.NewRequest(&managev1.UpdatePageRequest{Id: pageID, AccessPolicy: invalid}))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		_, err = service.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{Title: "Invalid access", AccessPolicy: invalid}))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	}
	// A removed saved tag remains readable, but it cannot be selected on save.
	require.NoError(t, db.Delete(&model.UserTag{}, "id = ?", tagID).Error)
	unchanged, err := service.UpdatePage(ctx, connect.NewRequest(&managev1.UpdatePageRequest{Id: pageID}))
	require.NoError(t, err)
	require.True(t, proto.Equal(normalized, unchanged.Msg.AccessPolicy))
	_, err = service.UpdatePage(ctx, connect.NewRequest(&managev1.UpdatePageRequest{Id: pageID, AccessPolicy: normalized}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	reset, err := service.UpdatePage(ctx, connect.NewRequest(&managev1.UpdatePageRequest{Id: pageID, AccessPolicy: &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_PUBLIC}}))
	require.NoError(t, err)
	require.True(t, reset.Msg.Changed)
	require.Equal(t, commonv1.PageAccessMode_PAGE_ACCESS_MODE_PUBLIC, reset.Msg.AccessPolicy.Mode)
	require.Empty(t, reset.Msg.AccessPolicy.UserTagIds)

	memberIdentityID := uuid.NewString()
	memberID := seedExternalKratosIdentityWithTraits(t, db, memberIdentityID, "Page policy user")
	grantIntegrationGlobalRole(t, spiceDB, memberIdentityID, policyv1.Role.User())
	memberCtx := auth.WithUser(ctx, &auth.UserInfo{IdentityID: auth.IdentityID(memberIdentityID), MemberID: auth.MemberID(memberID), SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true})
	_, err = service.UpdatePage(memberCtx, connect.NewRequest(&managev1.UpdatePageRequest{Id: pageID, AccessPolicy: conditions}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = service.CreatePage(memberCtx, connect.NewRequest(&managev1.CreatePageRequest{Title: "User forbidden"}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestPageAccessPolicyAbsentUpdateReadsLockedStateIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := pageIntegrationAdmin(t, db)
	service := NewPageService(db, newPageRuntimeForTest(db, "https://cdn.example.com"), newPageIntegrationFiles(db, spiceDB), noopAsyncPublisher{}, nil, spiceDB,
		WithPageContentBlockStore(newPageIntegrationContentBlockStore(t, spiceDB)), WithPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}))
	created, err := service.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{Title: "Locked access policy"}))
	require.NoError(t, err)
	encoded, err := pageaccess.Encode(&commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_AUTHENTICATED})
	require.NoError(t, err)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { _ = tx.Rollback().Error })
	require.NoError(t, tx.Model(&model.Page{}).Where("id = ?", created.Msg.Id).Update("access_policy", encoded).Error)
	type result struct {
		response *connect.Response[managev1.UpdatePageResponse]
		err      error
	}
	finished := make(chan result, 1)
	go func() {
		response, err := service.UpdatePage(ctx, connect.NewRequest(&managev1.UpdatePageRequest{Id: created.Msg.Id}))
		finished <- result{response, err}
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		return db.Raw("SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%FOR UPDATE%' AND query LIKE '%page%')").Scan(&waiting).Error == nil && waiting
	}, 5*time.Second, 20*time.Millisecond)
	require.NoError(t, tx.Commit().Error)
	select {
	case got := <-finished:
		require.NoError(t, got.err)
		require.False(t, got.response.Msg.Changed)
		require.Equal(t, commonv1.PageAccessMode_PAGE_ACCESS_MODE_AUTHENTICATED, got.response.Msg.AccessPolicy.Mode)
	case <-time.After(5 * time.Second):
		t.Fatal("Page update did not finish after the root lock was released")
	}
}
