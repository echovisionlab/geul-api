//go:build integration

package page

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type pagePublishStatementRecorder struct {
	logger.Interface
	mu         sync.Mutex
	statements []string
}

func (recorder *pagePublishStatementRecorder) Trace(
	ctx context.Context,
	begin time.Time,
	fc func() (string, int64),
	err error,
) {
	sql, rows := fc()
	recorder.mu.Lock()
	recorder.statements = append(recorder.statements, sql)
	recorder.mu.Unlock()
	recorder.Interface.Trace(ctx, begin, func() (string, int64) { return sql, rows }, err)
}

func (recorder *pagePublishStatementRecorder) snapshot() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.statements...)
}

func TestPageServiceManagementWorkflowIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := pageIntegrationAdmin(t, db)
	contentBlocks := newPageIntegrationContentBlockStore(t, spiceDB)
	fileDeleter := &pageManagementFileService{recordingArtistFileDeleter: &recordingArtistFileDeleter{}}
	pageSvc := NewPageService(
		db,
		newPageRuntimeForTest(db, "https://cdn.example.com"),
		fileDeleter,
		noopAsyncPublisher{},
		nil,
		spiceDB,
		WithPageContentBlockStore(contentBlocks),
		WithPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
		WithPageMenuTargets(noopPageMenuTargets{}),
	)
	suffix := strings.ReplaceAll(integrationTestUUID(), "-", "")
	slug := "page-manager-" + suffix
	otherSlug := "page-manager-other-" + suffix
	summary := "Page manager workflow summary"

	created, err := pageSvc.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{
		Title:   "Page Manager Workflow " + suffix,
		Slug:    &slug,
		Summary: &summary,
	}))
	require.NoError(t, err)
	require.NotNil(t, created.Msg.Document)
	require.Empty(t, created.Msg.Document.GetBase().GetNodes())
	require.NotEmpty(t, created.Msg.Revision)
	requireSynchronousAuthorizedResource(t, spiceDB, policyv1.Page.LookupManage(), created.Msg.Id, auth.GetUser(ctx).IdentityID.String(), true)

	other, err := pageSvc.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{
		Title: "Page Manager Workflow Other " + suffix,
		Slug:  &otherSlug,
	}))
	require.NoError(t, err)

	bySlug, err := pageSvc.GetPageBySlug(ctx, connect.NewRequest(&managev1.GetPageBySlugRequest{
		Slug: slug,
	}))
	require.NoError(t, err)
	require.Equal(t, created.Msg.Id, bySlug.Msg.Id)

	adminList, err := pageSvc.ListPagesAdmin(ctx, connect.NewRequest(&managev1.ListPagesAdminRequest{
		Pagination: &commonv1.PaginationRequest{Limit: 10},
		Filters: []*commonv1.FilterSpec{
			{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: managev1.PageStatus_PAGE_STATUS_DRAFT.String()},
			{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "Manager Workflow " + suffix},
		},
	}))
	require.NoError(t, err)
	requirePageSummaryIDs(t, adminList.Msg.Pages, created.Msg.Id)
	require.Equal(t, int32(1), adminList.Msg.Pagination.Total)

	available, err := pageSvc.CheckPageSlugAvailable(ctx, connect.NewRequest(&managev1.CheckPageSlugAvailableRequest{
		Slug: "page-manager-available-" + suffix,
	}))
	require.NoError(t, err)
	require.True(t, available.Msg.Available)

	occupied, err := pageSvc.CheckPageSlugAvailable(ctx, connect.NewRequest(&managev1.CheckPageSlugAvailableRequest{
		Slug: slug,
	}))
	require.NoError(t, err)
	require.False(t, occupied.Msg.Available)

	selfSlug, err := pageSvc.CheckPageSlugAvailable(ctx, connect.NewRequest(&managev1.CheckPageSlugAvailableRequest{
		Slug:      slug,
		ExcludeId: &created.Msg.Id,
	}))
	require.NoError(t, err)
	require.True(t, selfSlug.Msg.Available)

	otherPageSlug, err := pageSvc.CheckPageSlugAvailable(ctx, connect.NewRequest(&managev1.CheckPageSlugAvailableRequest{
		Slug:      otherSlug,
		ExcludeId: &created.Msg.Id,
	}))
	require.NoError(t, err)
	require.False(t, otherPageSlug.Msg.Available)

	firstFileID := seedImageBindingUploadedFileFixture(t, db, "page/"+created.Msg.Id+"/featured-one.webp")
	secondFileID := seedImageBindingUploadedFileFixture(t, db, "page/"+created.Msg.Id+"/featured-two.webp")
	firstImage, err := pageSvc.SetPageFeaturedImage(ctx, connect.NewRequest(&managev1.SetPageFeaturedImageRequest{
		PageId: created.Msg.Id,
		FileId: firstFileID,
	}))
	require.NoError(t, err)
	require.Equal(t, firstFileID, firstImage.Msg.GetImageDelivery().GetFileId())
	require.Equal(t, "https://cdn.example.com/media/test/"+firstFileID, firstImage.Msg.GetImageDelivery().GetInline().GetUrl())
	require.Equal(t, firstFileID, requirePageFeaturedImageFileID(t, db, created.Msg.Id))
	require.Empty(t, fileDeleter.deletedIDs)

	secondImage, err := pageSvc.SetPageFeaturedImage(ctx, connect.NewRequest(&managev1.SetPageFeaturedImageRequest{
		PageId: created.Msg.Id,
		FileId: secondFileID,
	}))
	require.NoError(t, err)
	require.Equal(t, secondFileID, secondImage.Msg.GetImageDelivery().GetFileId())
	require.Equal(t, secondFileID, requirePageFeaturedImageFileID(t, db, created.Msg.Id))
	require.Empty(t, fileDeleter.deletedIDs)
	requireFileRowExists(t, db, firstFileID)

	deletedImage, err := pageSvc.DeletePageFeaturedImage(ctx, connect.NewRequest(&managev1.DeletePageFeaturedImageRequest{
		PageId: created.Msg.Id,
	}))
	require.NoError(t, err)
	require.True(t, deletedImage.Msg.Success)
	requireNoPageFeaturedImageBinding(t, db, created.Msg.Id)
	require.Empty(t, fileDeleter.deletedIDs)
	requireFileRowExists(t, db, secondFileID)

	firstPublish, err := pageSvc.PublishPage(ctx, connect.NewRequest(&managev1.PublishPageRequest{Id: other.Msg.Id}))
	require.NoError(t, err)
	require.NotNil(t, firstPublish.Msg.PublishedAt)
	firstPublishedAt := firstPublish.Msg.PublishedAt.AsTime()
	_, err = pageSvc.UnpublishPage(ctx, connect.NewRequest(&managev1.UnpublishPageRequest{Id: other.Msg.Id}))
	require.NoError(t, err)
	publishStatements := &pagePublishStatementRecorder{Interface: db.Config.Logger}
	countedDB := db.Session(&gorm.Session{Logger: publishStatements})
	countedPageSvc := NewPageService(
		countedDB,
		newPageRuntimeForTest(countedDB, "https://cdn.example.com"),
		fileDeleter,
		noopAsyncPublisher{},
		nil,
		spiceDB,
		WithPageContentBlockStore(contentBlocks),
		WithPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
	)
	republished, err := countedPageSvc.PublishPage(ctx, connect.NewRequest(&managev1.PublishPageRequest{Id: other.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, firstPublishedAt, republished.Msg.PublishedAt.AsTime())
	publishSQL := publishStatements.snapshot()
	t.Logf("Page publish SQL statements: %d", len(publishSQL))
	require.LessOrEqual(t, len(publishSQL), 8, "Page publish must stay on the indexed readiness path")
	publishSQLText := strings.ToLower(strings.Join(publishSQL, "\n"))
	require.Contains(t, publishSQLText, "content_block_attachment")
	require.NotContains(t, publishSQLText, "content_block_locale")

	require.NotEmpty(t, other.Msg.Id)
	ownerFeaturedFileID := seedImageBindingUploadedFileFixture(t, db, "page/"+created.Msg.Id+"/owner-delete.webp")
	var ownerAsset model.PublicAsset
	require.NoError(t, db.Where("source_file_id = ? AND status = ?", ownerFeaturedFileID, model.PublicAssetStatusReady).
		Take(&ownerAsset).Error)
	_, err = pageSvc.SetPageFeaturedImage(ctx, connect.NewRequest(&managev1.SetPageFeaturedImageRequest{
		PageId: created.Msg.Id,
		FileId: ownerFeaturedFileID,
	}))
	require.NoError(t, err)
	require.NoError(t, mediaasset.NewLifecycle(db, "https://cdn.example.com").BindPublicAsset(ctx, mediaasset.Binding{
		AssetID:      ownerAsset.ID,
		OwnerType:    "page",
		OwnerID:      created.Msg.Id,
		BindingKey:   "featured_image",
		SourceFileID: &ownerFeaturedFileID,
	}))
	_, err = pageSvc.PublishPage(ctx, connect.NewRequest(&managev1.PublishPageRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	homepageUpdate := db.Model(&model.SiteSettings{}).
		Where("id = ?", 1).
		Update("homepage_page_id", created.Msg.Id)
	require.NoError(t, homepageUpdate.Error)
	require.Equal(t, int64(1), homepageUpdate.RowsAffected)
	shareLinkID := integrationTestUUID()
	shareLinkExpiry := time.Now().UTC().Add(time.Hour)
	require.NoError(t, db.Create(&model.ShareLink{
		ID: shareLinkID, Token: "page-delete-" + shareLinkID,
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE.String(),
		EntityID:   created.Msg.Id, ExpiresAt: &shareLinkExpiry, CreatedAt: time.Now().UTC(),
	}).Error)
	var pageContent struct {
		DocumentID string `gorm:"column:content_document_id"`
	}
	require.NoError(t, db.Table("page").
		Select("content_document_id").
		Where("id = ?", created.Msg.Id).
		Take(&pageContent).Error)
	require.NotEmpty(t, pageContent.DocumentID)
	deletedPage, err := pageSvc.DeletePage(ctx, connect.NewRequest(&managev1.DeletePageRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.True(t, deletedPage.Msg.Success)
	requireSynchronousAuthorizedResource(t, spiceDB, policyv1.Page.LookupManage(), created.Msg.Id, auth.GetUser(ctx).IdentityID.String(), false)
	require.Empty(t, fileDeleter.deletedIDs)
	requireFileRowExists(t, db, ownerFeaturedFileID)
	var featuredBindingCount int64
	require.NoError(t, db.Model(&model.PublicAssetBinding{}).
		Where("owner_type = ? AND owner_id = ? AND binding_key = ?", "page", created.Msg.Id, "featured_image").
		Count(&featuredBindingCount).Error)
	require.Zero(t, featuredBindingCount)
	var contentDocumentCount int64
	require.NoError(t, db.Table("content_document").
		Where("id = ?", pageContent.DocumentID).
		Count(&contentDocumentCount).Error)
	require.Zero(t, contentDocumentCount)
	var deletedShareLinkCount int64
	require.NoError(t, db.Model(&model.ShareLink{}).
		Where("id = ?", shareLinkID).
		Count(&deletedShareLinkCount).Error)
	require.Zero(t, deletedShareLinkCount)
	var siteSettings model.SiteSettings
	require.NoError(t, db.First(&siteSettings, "id = ?", 1).Error)
	require.Nil(t, siteSettings.HomepagePageID)
	var releasedAsset model.PublicAsset
	require.NoError(t, db.First(&releasedAsset, "id = ?", ownerAsset.ID).Error)
	require.Equal(t, model.PublicAssetStatusReady, releasedAsset.Status)
	require.Nil(t, releasedAsset.DeleteRequestedAt)
}

func TestPageDraftReadRequiresAdminIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	admin, spiceDB := pageIntegrationAdmin(t, db)
	pageSvc := NewPageService(
		db,
		newPageRuntimeForTest(db, "https://cdn.example.com"),
		newPageIntegrationFiles(db, spiceDB),
		noopAsyncPublisher{},
		nil,
		spiceDB,
		WithPageContentBlockStore(newPageIntegrationContentBlockStore(t, spiceDB)),
		WithPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
	)
	slug := "page-admin-boundary-" + strings.ReplaceAll(integrationTestUUID(), "-", "")
	page, err := pageSvc.CreatePage(admin, connect.NewRequest(&managev1.CreatePageRequest{
		Title: "Page Admin Boundary",
		Slug:  &slug,
	}))
	require.NoError(t, err)

	memberIdentityID := integrationTestUUID()
	memberID := seedExternalKratosIdentityWithTraits(t, db, memberIdentityID, "Page authority member")
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM member WHERE id = ?::uuid", memberID).Error
		_ = db.Exec("DELETE FROM kratos.identities WHERE id = ?::uuid", memberIdentityID).Error
	})
	member := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(memberIdentityID),
		MemberID:      auth.MemberID(memberID),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
	})

	_, draftByIDErr := pageSvc.GetPage(member, connect.NewRequest(&managev1.GetPageRequest{Id: page.Msg.Id}))
	_, missingByIDErr := pageSvc.GetPage(member, connect.NewRequest(&managev1.GetPageRequest{Id: integrationTestUUID()}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(draftByIDErr))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(missingByIDErr))

	_, draftBySlugErr := pageSvc.GetPageBySlug(member, connect.NewRequest(&managev1.GetPageBySlugRequest{Slug: slug}))
	_, missingBySlugErr := pageSvc.GetPageBySlug(member, connect.NewRequest(&managev1.GetPageBySlugRequest{Slug: slug + "-missing"}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(draftBySlugErr))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(missingBySlugErr))

}

func TestPageSlugSupportsSafeNestedRoutesIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := pageIntegrationAdmin(t, db)
	pageSvc := NewPageService(
		db,
		newPageRuntimeForTest(db, "https://cdn.example.com"),
		newPageIntegrationFiles(db, spiceDB),
		noopAsyncPublisher{},
		nil,
		spiceDB,
		WithPageContentBlockStore(newPageIntegrationContentBlockStore(t, spiceDB)),
		WithPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
	)
	validSlug := "page-nested-" + strings.ReplaceAll(integrationTestUUID(), "-", "") + "/team"
	page, err := pageSvc.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{
		Title: "Page Nested Route",
		Slug:  &validSlug,
	}))
	require.NoError(t, err)
	documentID := integrationTestUUID()
	require.NoError(t, db.Exec(`
		INSERT INTO content_document (id, profile, revision)
		VALUES (?::uuid, 'post', ?::uuid)
	`, documentID, integrationTestUUID()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO post (id, slug, content_document_id)
		VALUES (?::uuid, 'article', ?::uuid)
	`, integrationTestUUID(), documentID).Error)

	for _, test := range []struct {
		slug string
		code connect.Code
	}{
		{slug: "/about", code: connect.CodeInvalidArgument},
		{slug: "about/", code: connect.CodeInvalidArgument},
		{slug: "about//team", code: connect.CodeInvalidArgument},
		{slug: "about/./team", code: connect.CodeInvalidArgument},
		{slug: "about/../team", code: connect.CodeInvalidArgument},
		{slug: "admin/team", code: connect.CodeInvalidArgument},
		{slug: "posts/article", code: connect.CodeAlreadyExists},
	} {
		_, err = pageSvc.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{
			Title: "Invalid Page Slug",
			Slug:  &test.slug,
		}))
		require.Equal(t, test.code, connect.CodeOf(err), test.slug)

		_, err = pageSvc.UpdatePage(ctx, connect.NewRequest(&managev1.UpdatePageRequest{
			Id:   page.Msg.Id,
			Slug: &test.slug,
		}))
		require.Equal(t, test.code, connect.CodeOf(err), test.slug)

		available, checkErr := pageSvc.CheckPageSlugAvailable(ctx, connect.NewRequest(&managev1.CheckPageSlugAvailableRequest{
			Slug: test.slug,
		}))
		require.NoError(t, checkErr)
		require.False(t, available.Msg.Available)
	}

	var persisted model.Page
	require.NoError(t, db.Select("slug").First(&persisted, "id = ?", page.Msg.Id).Error)
	require.NotNil(t, persisted.Slug)
	require.Equal(t, validSlug, *persisted.Slug)
}

func pageIntegrationAdmin(t *testing.T, db *gorm.DB) (context.Context, *auth.SpiceDBClient) {
	t.Helper()
	identityID := integrationTestUUID()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Page integration admin")
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM member WHERE id = ?::uuid", memberID).Error
		_ = db.Exec("DELETE FROM kratos.identities WHERE id = ?::uuid", identityID).Error
	})
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	}), spiceDB
}

func newPageIntegrationContentBlockStore(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
) *contentblock.Store {
	t.Helper()
	store, err := contentblock.NewGeneratedStore(NewContentBlockFileReuseAuthorizer(spiceDB))
	require.NoError(t, err)
	return store
}

type pageManagementFileService struct {
	*recordingArtistFileDeleter
}

func (s *pageManagementFileService) ResolveAuthorizedPageFeaturedImage(
	_ context.Context,
	_ string,
	expectedFileID string,
) (*commonv1.MediaDelivery, error) {
	if expectedFileID == "" {
		return nil, nil
	}
	return &commonv1.MediaDelivery{
		FileId: expectedFileID,
		Inline: &commonv1.ExpiringMediaRef{
			FileId:  expectedFileID,
			Url:     "https://cdn.example.com/media/test/" + expectedFileID,
			Purpose: commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_INLINE,
		},
	}, nil
}

func requirePageSummaryIDs(t *testing.T, pages []*managev1.PageSummary, want ...string) {
	t.Helper()
	got := make([]string, 0, len(pages))
	for _, page := range pages {
		got = append(got, page.GetId())
	}
	require.ElementsMatch(t, want, got)
}

func requirePageFeaturedImageFileID(t *testing.T, db *gorm.DB, pageID string) string {
	t.Helper()

	var row struct {
		FeaturedImageFileID *string `gorm:"column:featured_image_file_id"`
	}
	require.NoError(t, db.Table("page").
		Select("featured_image_file_id").
		Where("id = ?", pageID).
		First(&row).Error)
	require.NotNil(t, row.FeaturedImageFileID)
	return *row.FeaturedImageFileID
}

func requireNoPageFeaturedImageBinding(t *testing.T, db *gorm.DB, pageID string) {
	t.Helper()

	var row struct {
		FeaturedImageFileID *string `gorm:"column:featured_image_file_id"`
	}
	require.NoError(t, db.Table("page").
		Select("featured_image_file_id").
		Where("id = ?", pageID).
		First(&row).Error)
	require.Nil(t, row.FeaturedImageFileID)
}
