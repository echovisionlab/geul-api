//go:build integration

package series

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

type seriesQueryCounter struct {
	logger.Interface
	count *atomic.Int64
}

func (l seriesQueryCounter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	l.count.Add(1)
	l.Interface.Trace(ctx, begin, fc, err)
}

func TestSeriesLifecycleSlugAndAdminListContractIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	outsiderID := integrationTestUUID()
	seedSeriesActor(t, db, adminID, "Series closure admin")
	seedSeriesActor(t, db, outsiderID, "Series closure outsider")
	service := newSeriesIntegrationService(t, db, adminID, &recordingSeriesFileDeleter{})
	ctx := seriesIntegrationAdminCtx(adminID)
	suffix := strings.ReplaceAll(integrationTestUUID(), "-", "")

	created, err := service.CreateSeries(ctx, connect.NewRequest(&managev1.CreateSeriesRequest{
		Title: "Series Lifecycle " + suffix,
	}))
	require.NoError(t, err)
	require.Equal(t, managev1.SeriesStatus_SERIES_STATUS_DRAFT.String(), created.Msg.Status)
	require.Equal(t, "series-lifecycle-"+suffix, created.Msg.GetSlug())

	invalidStatus := "SERIES_STATUS_ARCHIVED"
	_, err = service.UpdateSeries(ctx, connect.NewRequest(&managev1.UpdateSeriesRequest{
		Id: created.Msg.Id, Status: &invalidStatus,
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	requireSeriesStatus(t, db, created.Msg.Id, managev1.SeriesStatus_SERIES_STATUS_DRAFT.String())

	blankSlug := "   "
	_, err = service.UpdateSeries(ctx, connect.NewRequest(&managev1.UpdateSeriesRequest{
		Id: created.Msg.Id, Slug: &blankSlug,
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Equal(t, "series-lifecycle-"+suffix, requireSeriesSlug(t, db, created.Msg.Id))

	unsafeSlug := "series/unsafe"
	_, err = service.UpdateSeries(ctx, connect.NewRequest(&managev1.UpdateSeriesRequest{
		Id: created.Msg.Id, Slug: &unsafeSlug,
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	published := managev1.SeriesStatus_SERIES_STATUS_PUBLISHED.String()
	nextSlugInput := "  series-published-" + suffix + "  "
	updated, err := service.UpdateSeries(ctx, connect.NewRequest(&managev1.UpdateSeriesRequest{
		Id: created.Msg.Id, Slug: &nextSlugInput, Status: &published,
	}))
	require.NoError(t, err)
	require.Equal(t, strings.TrimSpace(nextSlugInput), updated.Msg.GetSlug())
	require.Equal(t, published, updated.Msg.Status)

	_, err = service.CheckSeriesSlugAvailable(ctx, connect.NewRequest(&managev1.CheckSeriesSlugAvailableRequest{Slug: " "}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	alphaID := createSeriesForClosure(t, service, ctx, "Alpha "+suffix, "alpha-"+suffix)
	_, err = service.UpdateSeries(ctx, connect.NewRequest(&managev1.UpdateSeriesRequest{
		Id: alphaID, Status: &published,
	}))
	require.NoError(t, err)
	_ = seedSeriesRow(t, db, "Zulu "+suffix, "zulu-"+suffix, managev1.SeriesStatus_SERIES_STATUS_DRAFT.String())
	list, err := service.ListSeriesAdmin(ctx, connect.NewRequest(&managev1.ListSeriesAdminRequest{
		Filters: []*commonv1.FilterSpec{
			{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "alpha " + suffix},
			{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: published},
		},
		Sorts:      []*commonv1.SortSpec{{Field: "title", Order: commonv1.SortOrder_SORT_ORDER_ASC}},
		Pagination: &commonv1.PaginationRequest{Limit: 20},
	}))
	require.NoError(t, err)
	require.Len(t, list.Msg.Series, 1)
	require.Equal(t, alphaID, list.Msg.Series[0].GetSeries().GetId())

	outsiderCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(outsiderID), MemberID: auth.MemberID(integrationMemberID(outsiderID)),
		SessionID: auth.SessionID(integrationTestUUID()), Authenticated: true,
	})
	_, err = service.GetSeriesWithManagers(outsiderCtx, connect.NewRequest(&managev1.GetSeriesWithManagersRequest{Id: created.Msg.Id}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = service.ListSeriesManagers(outsiderCtx, connect.NewRequest(&managev1.ListSeriesManagersRequest{SeriesId: created.Msg.Id}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = service.ListSeriesPosts(outsiderCtx, connect.NewRequest(&managev1.ListSeriesPostsRequest{SeriesId: created.Msg.Id}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = service.ListSeriesManagers(ctx, connect.NewRequest(&managev1.ListSeriesManagersRequest{SeriesId: integrationTestUUID()}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	adminMemberID := integrationMemberID(adminID)
	_, err = service.AddSeriesManager(ctx, connect.NewRequest(&managev1.AddSeriesManagerRequest{
		SeriesId: created.Msg.Id, MemberId: adminMemberID,
	}))
	require.NoError(t, err)
	mySeries, err := service.ListMySeries(ctx, connect.NewRequest(&managev1.ListMySeriesRequest{}))
	require.NoError(t, err)
	require.NotEmpty(t, mySeries.Msg.Series)
	posts, err := service.ListSeriesPosts(ctx, connect.NewRequest(&managev1.ListSeriesPostsRequest{SeriesId: created.Msg.Id}))
	require.NoError(t, err)
	require.Empty(t, posts.Msg.Posts)
	simple, err := service.ListSeriesSimple(ctx, connect.NewRequest(&managev1.ListSeriesSimpleRequest{}))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(simple.Msg.Series), 3)

	zeroManagers, err := service.GetSeriesWithManagers(ctx, connect.NewRequest(&managev1.GetSeriesWithManagersRequest{Id: alphaID}))
	require.NoError(t, err)
	require.Empty(t, zeroManagers.Msg.Managers)
	outsiderMemberID := integrationMemberID(outsiderID)
	_, err = service.AddSeriesManager(ctx, connect.NewRequest(&managev1.AddSeriesManagerRequest{
		SeriesId: alphaID, MemberId: outsiderMemberID,
	}))
	require.NoError(t, err)
	withManager, err := service.GetSeriesWithManagers(ctx, connect.NewRequest(&managev1.GetSeriesWithManagersRequest{Id: alphaID}))
	require.NoError(t, err)
	require.Len(t, withManager.Msg.Managers, 1)
	require.Equal(t, outsiderMemberID, withManager.Msg.Managers[0].MemberId)
	_, err = service.RemoveSeriesManager(ctx, connect.NewRequest(&managev1.RemoveSeriesManagerRequest{
		SeriesId: alphaID, MemberId: outsiderMemberID,
	}))
	require.NoError(t, err)
	afterRemove, err := service.ListSeriesManagers(ctx, connect.NewRequest(&managev1.ListSeriesManagersRequest{SeriesId: alphaID}))
	require.NoError(t, err)
	require.Empty(t, afterRemove.Msg.Managers)
}

func TestSeriesPostRelationAuthorityAndAtomicOrderingIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	authorID := integrationTestUUID()
	seedSeriesActor(t, db, adminID, "Series relation admin")
	seedSeriesActor(t, db, authorID, "Series relation author")
	service := newSeriesIntegrationService(t, db, adminID, &recordingSeriesFileDeleter{})
	grantIntegrationGlobalRole(t, service.spiceDB, authorID, policyv1.Role.Author())
	adminCtx := seriesIntegrationAdminCtx(adminID)
	authorCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(authorID), MemberID: auth.MemberID(integrationMemberID(authorID)),
		SessionID: auth.SessionID(integrationTestUUID()), Authenticated: true,
	})
	suffix := strings.ReplaceAll(integrationTestUUID(), "-", "")
	seriesOne := createSeriesForClosure(t, service, adminCtx, "Relation One "+suffix, "relation-one-"+suffix)
	seriesTwo := createSeriesForClosure(t, service, adminCtx, "Relation Two "+suffix, "relation-two-"+suffix)
	memberID := integrationMemberID(authorID)
	addSeriesManagerRelation(t, service, authorID, seriesOne)
	addSeriesManagerRelation(t, service, authorID, seriesTwo)

	postOne := seedSeriesPost(t, db, memberID)
	postTwo := seedSeriesPost(t, db, memberID)
	postThree := seedSeriesPost(t, db, memberID)
	for _, postID := range []string{postOne, postTwo, postThree} {
		grantSeriesPostAuthorRelation(t, service, postID, authorID)
	}

	for _, postID := range []string{postOne, postTwo} {
		_, err := service.AssignPostToSeries(authorCtx, connect.NewRequest(&managev1.AssignPostToSeriesRequest{
			SeriesId: seriesOne, PostId: postID,
		}))
		require.NoError(t, err)
	}
	_, err := service.AssignPostToSeries(authorCtx, connect.NewRequest(&managev1.AssignPostToSeriesRequest{
		SeriesId: seriesTwo, PostId: postThree,
	}))
	require.NoError(t, err)
	requireSeriesOrder(t, db, seriesOne, []string{postOne, postTwo})

	_, err = service.ReorderSeriesPosts(authorCtx, connect.NewRequest(&managev1.ReorderSeriesPostsRequest{
		SeriesId: seriesOne, PostIds: []string{postTwo, postOne},
	}))
	require.NoError(t, err)
	requireSeriesOrder(t, db, seriesOne, []string{postTwo, postOne})

	for name, invalidOrder := range map[string][]string{
		"duplicate": {postTwo, postTwo},
		"missing":   {postTwo},
		"foreign":   {postTwo, postOne, postThree},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.ReorderSeriesPosts(authorCtx, connect.NewRequest(&managev1.ReorderSeriesPostsRequest{
				SeriesId: seriesOne, PostIds: invalidOrder,
			}))
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			requireSeriesOrder(t, db, seriesOne, []string{postTwo, postOne})
		})
	}

	_, err = service.UnassignPostFromSeries(authorCtx, connect.NewRequest(&managev1.UnassignPostFromSeriesRequest{
		SeriesId: seriesOne, PostId: postThree,
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	requireSeriesOrder(t, db, seriesOne, []string{postTwo, postOne})

	_, err = service.UnassignPostFromSeries(authorCtx, connect.NewRequest(&managev1.UnassignPostFromSeriesRequest{
		SeriesId: seriesOne, PostId: postOne,
	}))
	require.NoError(t, err)
	requirePostSeriesRelation(t, db, postOne, nil, nil)

	_, err = service.AssignPostToSeries(authorCtx, connect.NewRequest(&managev1.AssignPostToSeriesRequest{
		SeriesId: seriesOne, PostId: postOne,
	}))
	require.NoError(t, err)
	postFour := seedSeriesPost(t, db, memberID)
	grantSeriesPostAuthorRelation(t, service, postFour, authorID)
	_, err = service.AssignPostToSeries(authorCtx, connect.NewRequest(&managev1.AssignPostToSeriesRequest{
		SeriesId: seriesOne, PostId: postFour,
	}))
	require.NoError(t, err)
	requireSeriesOrder(t, db, seriesOne, []string{postTwo, postOne, postFour})
	_, err = service.UnassignPostFromSeries(authorCtx, connect.NewRequest(&managev1.UnassignPostFromSeriesRequest{
		SeriesId: seriesOne, PostId: postOne,
	}))
	require.NoError(t, err)
	requireSeriesOrder(t, db, seriesOne, []string{postTwo, postFour})
	requirePostSeriesRelation(t, db, postFour, &seriesOne, intPtr(1))

	_, err = service.AssignPostToSeries(authorCtx, connect.NewRequest(&managev1.AssignPostToSeriesRequest{
		SeriesId: seriesTwo, PostId: postFour,
	}))
	require.NoError(t, err)
	requireSeriesOrder(t, db, seriesOne, []string{postTwo})
	requireSeriesOrder(t, db, seriesTwo, []string{postThree, postFour})

	removeSeriesManagerRelation(t, service, authorID, seriesTwo)
	_, err = service.AssignPostToSeries(authorCtx, connect.NewRequest(&managev1.AssignPostToSeriesRequest{
		SeriesId: seriesTwo, PostId: postTwo,
	}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	requirePostSeriesRelation(t, db, postTwo, &seriesOne, intPtr(0))

	addSeriesManagerRelation(t, service, authorID, seriesTwo)
	replacementAuthorID := integrationMemberID(adminID)
	require.NoError(t, db.Exec(`
		INSERT INTO post_author (post_id, member_id, created_at)
		VALUES (?::uuid, ?::uuid, NOW())
	`, postTwo, replacementAuthorID).Error)
	require.NoError(t, db.Exec("DELETE FROM post_author WHERE post_id = ? AND member_id = ?", postTwo, memberID).Error)
	replaceSeriesPostAuthorRelation(t, service, postTwo, authorID, adminID)
	_, err = service.AssignPostToSeries(authorCtx, connect.NewRequest(&managev1.AssignPostToSeriesRequest{
		SeriesId: seriesTwo, PostId: postTwo,
	}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	requirePostSeriesRelation(t, db, postTwo, &seriesOne, intPtr(0))

	_, err = service.AssignPostToSeries(authorCtx, connect.NewRequest(&managev1.AssignPostToSeriesRequest{
		SeriesId: seriesTwo, PostId: integrationTestUUID(),
	}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestDeleteSeriesClearsPostRelationAndPreservesFeaturedImageFileIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedSeriesActor(t, db, adminID, "Series delete admin")
	fileDeleter := &recordingSeriesFileDeleter{}
	service := newSeriesIntegrationService(t, db, adminID, fileDeleter)
	ctx := seriesIntegrationAdminCtx(adminID)
	suffix := strings.ReplaceAll(integrationTestUUID(), "-", "")
	seriesID := createSeriesForClosure(t, service, ctx, "Delete Series "+suffix, "delete-series-"+suffix)
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO series_translation (
			entity_id, locale, title, created_at, updated_at
		) VALUES (?::uuid, 'ko', '삭제 시리즈', ?, ?)
	`, seriesID, now, now).Error)
	_, err := service.AddSeriesManager(ctx, connect.NewRequest(&managev1.AddSeriesManagerRequest{
		SeriesId: seriesID, MemberId: integrationMemberID(adminID),
	}))
	require.NoError(t, err)
	requireSeriesManagerAttribution(t, db, seriesID, integrationMemberID(adminID), true)
	postID := seedSeriesPost(t, db, integrationMemberID(adminID))
	require.NoError(t, db.Table("post").Where("id = ?", postID).Updates(structured.Fields{
		"series_id": seriesID, "series_order": 0,
	}).Error)

	fileID := seedImageBindingUploadedFileFixture(t, db, "series/"+seriesID+"/featured.webp")
	replacementFileID := seedImageBindingUploadedFileFixture(t, db, "series/"+seriesID+"/replacement.webp")
	deleteFileID := seedImageBindingUploadedFileFixture(t, db, "series/"+seriesID+"/delete.webp")
	derivativeAssetID := seedSeriesFileDerivative(t, db, fileID)
	_, err = service.SetSeriesFeaturedImage(ctx, connect.NewRequest(&managev1.SetSeriesFeaturedImageRequest{
		SeriesId: seriesID, FileId: fileID,
	}))
	require.NoError(t, err)
	_, err = service.SetSeriesFeaturedImage(ctx, connect.NewRequest(&managev1.SetSeriesFeaturedImageRequest{
		SeriesId: seriesID, FileId: replacementFileID,
	}))
	require.NoError(t, err)
	requireFileRowExists(t, db, fileID)
	require.Empty(t, fileDeleter.deletedIDs)
	deleteFeatured, err := service.DeleteSeriesFeaturedImage(ctx, connect.NewRequest(&managev1.DeleteSeriesFeaturedImageRequest{SeriesId: seriesID}))
	require.NoError(t, err)
	require.NotEmpty(t, deleteFeatured.Msg.GetOgGenerationRunId())
	var titleOnlyGenerations []model.OgGeneration
	require.NoError(t, db.Where("run_id = ?", deleteFeatured.Msg.GetOgGenerationRunId()).
		Order("request_sequence ASC").Find(&titleOnlyGenerations).Error)
	require.Len(t, titleOnlyGenerations, 2)
	locales := make([]string, 0, len(titleOnlyGenerations))
	for _, generation := range titleOnlyGenerations {
		var snapshot ogEntitySnapshotForTest
		require.NoError(t, json.Unmarshal(generation.EntitySnapshot, &snapshot))
		require.Nil(t, snapshot.FeaturedImage)
		require.NotNil(t, snapshot.Locale)
		locales = append(locales, *snapshot.Locale)
	}
	require.ElementsMatch(t, []string{"en", "ko"}, locales)
	requireFileRowExists(t, db, replacementFileID)
	require.Empty(t, fileDeleter.deletedIDs)
	_, err = service.SetSeriesFeaturedImage(ctx, connect.NewRequest(&managev1.SetSeriesFeaturedImageRequest{
		SeriesId: seriesID, FileId: deleteFileID,
	}))
	require.NoError(t, err)

	_, err = service.DeleteSeries(ctx, connect.NewRequest(&managev1.DeleteSeriesRequest{Id: seriesID}))
	require.NoError(t, err)
	requirePostSeriesRelation(t, db, postID, nil, nil)
	requireFileRowExists(t, db, fileID)
	requireFileRowExists(t, db, replacementFileID)
	requireFileRowExists(t, db, deleteFileID)
	var derivativeCount int64
	require.NoError(t, db.Table("file_derivative").Where("file_id = ?", fileID).Count(&derivativeCount).Error)
	require.Equal(t, int64(1), derivativeCount)
	var derivativeStatus string
	require.NoError(t, db.Table("public_asset").Select("status").Where("id = ?", derivativeAssetID).Scan(&derivativeStatus).Error)
	require.Equal(t, model.PublicAssetStatusReady, derivativeStatus)
	require.Empty(t, fileDeleter.deletedIDs)
	var bindingCount int64
	require.NoError(t, db.Table("public_asset_binding").
		Where("owner_type = ? AND owner_id = ? AND binding_key = ?", "series", seriesID, "featured_image").
		Count(&bindingCount).Error)
	require.Zero(t, bindingCount)
	requireSeriesManagerAttribution(t, db, seriesID, integrationMemberID(adminID), false)
}

func TestDeleteSeriesSerializesWithFeaturedImageBindingIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedSeriesActor(t, db, adminID, "Concurrent Series delete admin")
	service := newSeriesIntegrationService(t, db, adminID, &recordingSeriesFileDeleter{})
	ctx := seriesIntegrationAdminCtx(adminID)
	seriesID := createSeriesForClosure(t, service, ctx, "Concurrent Series delete", "concurrent-series-delete-"+adminID)
	fileID := seedImageBindingUploadedFileFixture(t, db, "series/"+seriesID+"/concurrent.webp")
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM public_asset_binding WHERE owner_type = 'series' AND owner_id = ?`, seriesID).Error
		_ = db.Exec(`DELETE FROM series WHERE id = ?`, seriesID).Error
		_ = db.Exec(`DELETE FROM public_asset WHERE source_file_id = ?`, fileID).Error
		_ = db.Exec(`DELETE FROM file WHERE id = ?`, fileID).Error
		_ = db.Exec(`DELETE FROM member WHERE id = ?`, integrationMemberID(adminID)).Error
		_ = db.Exec(`DELETE FROM kratos.identities WHERE id = ?`, adminID).Error
	})

	blocker := db.Begin()
	require.NoError(t, blocker.Error)
	t.Cleanup(func() { _ = blocker.Rollback().Error })
	require.NoError(t, blocker.Exec(`SELECT id FROM series WHERE id = ? FOR UPDATE`, seriesID).Error)

	setDone := make(chan error, 1)
	go func() {
		_, err := service.SetSeriesFeaturedImage(ctx, connect.NewRequest(&managev1.SetSeriesFeaturedImageRequest{
			SeriesId: seriesID, FileId: fileID,
		}))
		setDone <- err
	}()
	select {
	case err := <-setDone:
		t.Fatalf("featured image mutation did not wait for the Series lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	deleteDone := make(chan error, 1)
	go func() {
		_, err := service.DeleteSeries(ctx, connect.NewRequest(&managev1.DeleteSeriesRequest{Id: seriesID}))
		deleteDone <- err
	}()
	select {
	case err := <-deleteDone:
		t.Fatalf("Series deletion did not wait for the Series lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	require.NoError(t, blocker.Commit().Error)
	require.NoError(t, <-setDone)
	require.NoError(t, <-deleteDone)
	var seriesCount int64
	require.NoError(t, db.Table("series").Where("id = ?", seriesID).Count(&seriesCount).Error)
	require.Zero(t, seriesCount)
	var bindingCount int64
	require.NoError(t, db.Table("public_asset_binding").
		Where("owner_type = ? AND owner_id = ?", "series", seriesID).
		Count(&bindingCount).Error)
	require.Zero(t, bindingCount, "delete must release a binding committed immediately before it")
	requireFileRowExists(t, db, fileID)
}

func TestUpdateSeriesUsesLockedCurrentSlugForMenuRewriteIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedSeriesActor(t, db, adminID, "Concurrent Series update admin")
	service := newSeriesIntegrationService(t, db, adminID, &recordingSeriesFileDeleter{})
	ctx := seriesIntegrationAdminCtx(adminID)
	seriesID := createSeriesForClosure(t, service, ctx, "Concurrent Series update", "series-slug-original-"+adminID)
	originalSlug := requireSeriesSlug(t, db, seriesID)
	menuID := integrationTestUUID()
	menuItems, err := json.Marshal([]model.MenuItem{{
		ID: "series-link", Label: "Series", LinkType: "series", TargetID: &seriesID, TargetSlug: &originalSlug,
	}})
	require.NoError(t, err)
	menuContentDocumentID := integrationTestUUID()
	require.NoError(t, db.Exec(`
		INSERT INTO content_document (id, profile, revision, created_at, updated_at)
		VALUES (?::uuid, 'compact', ?::uuid, NOW(), NOW())
	`, menuContentDocumentID, integrationTestUUID()).Error)
	require.NoError(t, db.Create(&model.Menu{
		ID: menuID, ContentDocumentID: menuContentDocumentID, SourceLocale: "en",
		Name: "Concurrent Series menu", Items: menuItems, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}).Error)
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM menu WHERE id = ?`, menuID).Error
		_ = db.Exec(`DELETE FROM content_document WHERE id = ?`, menuContentDocumentID).Error
		_ = db.Exec(`DELETE FROM series WHERE id = ?`, seriesID).Error
		_ = db.Exec(`DELETE FROM member WHERE id = ?`, integrationMemberID(adminID)).Error
		_ = db.Exec(`DELETE FROM kratos.identities WHERE id = ?`, adminID).Error
	})

	blocker := db.Begin()
	require.NoError(t, blocker.Error)
	t.Cleanup(func() { _ = blocker.Rollback().Error })
	require.NoError(t, blocker.Exec(`SELECT id FROM series WHERE id = ? FOR UPDATE`, seriesID).Error)

	finalSlug := "series-slug-final-" + adminID
	updateDone := make(chan error, 1)
	go func() {
		_, err := service.UpdateSeries(ctx, connect.NewRequest(&managev1.UpdateSeriesRequest{Id: seriesID, Slug: &finalSlug}))
		updateDone <- err
	}()
	select {
	case err := <-updateDone:
		t.Fatalf("Series update did not wait for the Series lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	intermediateSlug := "series-slug-intermediate-" + adminID
	intermediateItems, err := json.Marshal([]model.MenuItem{{
		ID: "series-link", Label: "Series", LinkType: "series", TargetID: &seriesID, TargetSlug: &intermediateSlug,
	}})
	require.NoError(t, err)
	require.NoError(t, blocker.Table("series").Where("id = ?", seriesID).Update("slug", intermediateSlug).Error)
	require.NoError(t, blocker.Table("menu").Where("id = ?", menuID).Update("items", intermediateItems).Error)
	require.NoError(t, blocker.Commit().Error)
	require.NoError(t, <-updateDone)
	require.Equal(t, finalSlug, requireSeriesSlug(t, db, seriesID))

	var menu model.Menu
	require.NoError(t, db.First(&menu, "id = ?", menuID).Error)
	var storedItems []model.MenuItem
	require.NoError(t, json.Unmarshal(menu.Items, &storedItems))
	require.Len(t, storedItems, 1)
	require.NotNil(t, storedItems[0].TargetSlug)
	require.Equal(t, finalSlug, *storedItems[0].TargetSlug)
}

func TestSeriesMutationRechecksAuthorityAfterOwningRowLockIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	managerID := integrationTestUUID()
	seedSeriesActor(t, db, adminID, "Series authority admin")
	seedSeriesActor(t, db, managerID, "Series authority manager")
	service := newSeriesIntegrationService(t, db, adminID, &recordingSeriesFileDeleter{})
	adminCtx := seriesIntegrationAdminCtx(adminID)
	seriesID := seedSeriesRow(t, db, "Authority Series", "authority-series-"+managerID, managev1.SeriesStatus_SERIES_STATUS_DRAFT.String())
	managerMemberID := integrationMemberID(managerID)
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM series WHERE id = ?`, seriesID).Error
		_ = db.Exec(`DELETE FROM member WHERE id IN (?, ?)`, integrationMemberID(adminID), managerMemberID).Error
		_ = db.Exec(`DELETE FROM kratos.identities WHERE id IN (?, ?)`, adminID, managerID).Error
	})
	addSeriesManagerRelation(t, service, managerID, seriesID)
	managerCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(managerID), MemberID: auth.MemberID(managerMemberID),
		SessionID: auth.SessionID(integrationTestUUID()), Authenticated: true,
	})

	blocker := db.Begin()
	require.NoError(t, blocker.Error)
	t.Cleanup(func() { _ = blocker.Rollback().Error })
	require.NoError(t, blocker.Exec(`SELECT id FROM series WHERE id = ? FOR UPDATE`, seriesID).Error)
	nextTitle := "Must not commit after revoke"
	updateDone := make(chan error, 1)
	go func() {
		_, err := service.UpdateSeries(managerCtx, connect.NewRequest(&managev1.UpdateSeriesRequest{
			Id: seriesID, Title: &nextTitle,
		}))
		updateDone <- err
	}()
	select {
	case err := <-updateDone:
		t.Fatalf("Series update did not wait for the owning Series lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	removeSeriesManagerRelation(t, service, managerID, seriesID)
	require.NoError(t, blocker.Commit().Error)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(<-updateDone))
	var storedTitle string
	require.NoError(t, db.Raw(`
		SELECT translation.title
		FROM series_translation AS translation
		JOIN series AS root
		  ON root.id = translation.entity_id
		 AND root.source_locale = translation.locale
		WHERE translation.entity_id = ?`, seriesID).Scan(&storedTitle).Error)
	require.Equal(t, "Authority Series", storedTitle)

	adminBlocker := db.Begin()
	require.NoError(t, adminBlocker.Error)
	t.Cleanup(func() { _ = adminBlocker.Rollback().Error })
	require.NoError(t, adminBlocker.Exec(`SELECT id FROM series WHERE id = ? FOR UPDATE`, seriesID).Error)
	deleteDone := make(chan error, 1)
	go func() {
		_, err := service.DeleteSeries(adminCtx, connect.NewRequest(&managev1.DeleteSeriesRequest{Id: seriesID}))
		deleteDone <- err
	}()
	select {
	case err := <-deleteDone:
		t.Fatalf("Series delete did not wait for the owning Series lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	adminSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(adminID))
	require.NoError(t, err)
	_, err = service.spiceDB.SyncAccountIdentityGlobalRole(t.Context(), adminSubject, policyv1.Role.User())
	require.NoError(t, err)
	require.NoError(t, adminBlocker.Commit().Error)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(<-deleteDone))
	var remaining int64
	require.NoError(t, db.Table("series").Where("id = ?", seriesID).Count(&remaining).Error)
	require.EqualValues(t, 1, remaining)
}

func TestSeriesAdminListQueryBudgetDoesNotGrowPerRowIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedSeriesActor(t, db, adminID, "Series query admin")
	suffix := strings.ReplaceAll(integrationTestUUID(), "-", "")
	for index := 0; index < 12; index++ {
		seedSeriesRow(t, db, fmt.Sprintf("Query Series %02d %s", index, suffix), fmt.Sprintf("query-series-%02d-%s", index, suffix), managev1.SeriesStatus_SERIES_STATUS_DRAFT.String())
	}

	var queryCount atomic.Int64
	countedDB := db.Session(&gorm.Session{Logger: seriesQueryCounter{
		Interface: db.Config.Logger,
		count:     &queryCount,
	}})
	service := newSeriesIntegrationService(t, countedDB, adminID, &recordingSeriesFileDeleter{})
	ctx := seriesIntegrationAdminCtx(adminID)
	listCount := func(limit int32) int64 {
		queryCount.Store(0)
		response, err := service.ListSeriesAdmin(ctx, connect.NewRequest(&managev1.ListSeriesAdminRequest{
			Pagination: &commonv1.PaginationRequest{Limit: limit},
		}))
		require.NoError(t, err)
		require.NotEmpty(t, response.Msg.Series)
		return queryCount.Load()
	}
	oneRowQueries := listCount(1)
	twelveRowQueries := listCount(12)
	require.Equal(t, oneRowQueries, twelveRowQueries)
	require.LessOrEqual(t, twelveRowQueries, int64(8))
}

func TestListSeriesSimpleReturnsAllSeriesIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedSeriesActor(t, db, adminID, "Series simple list admin")
	service := newSeriesIntegrationService(t, db, adminID, nil)
	ctx := seriesIntegrationAdminCtx(adminID)
	suffix := strings.ReplaceAll(integrationTestUUID(), "-", "")

	const seededCount = 105
	seededIDs := make(map[string]struct{}, seededCount)
	for index := 0; index < seededCount; index++ {
		seriesID := seedSeriesRow(
			t, db,
			fmt.Sprintf("Simple Series %03d %s", index, suffix),
			fmt.Sprintf("simple-series-%03d-%s", index, suffix),
			managev1.SeriesStatus_SERIES_STATUS_DRAFT.String(),
		)
		seededIDs[seriesID] = struct{}{}
	}

	response, err := service.ListSeriesSimple(ctx, connect.NewRequest(&managev1.ListSeriesSimpleRequest{}))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(response.Msg.Series), seededCount)
	for _, item := range response.Msg.Series {
		delete(seededIDs, item.Id)
	}
	require.Empty(t, seededIDs, "the unpaginated picker response must include every Series")
}

func requireSeriesManagerAttribution(t *testing.T, db *gorm.DB, seriesID, memberID string, present bool) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("series_manager").
		Where("series_id = ? AND member_id = ?", seriesID, memberID).
		Count(&count).Error)
	if present {
		require.Equal(t, int64(1), count)
		return
	}
	require.Zero(t, count)
}

func createSeriesForClosure(
	t *testing.T,
	service *SeriesService,
	ctx context.Context,
	title string,
	slug string,
) string {
	t.Helper()
	created, err := service.CreateSeries(ctx, connect.NewRequest(&managev1.CreateSeriesRequest{Title: title, Slug: &slug}))
	require.NoError(t, err)
	return created.Msg.Id
}

func replaceSeriesPostAuthorRelation(t *testing.T, service *SeriesService, postID, previousIdentityID, nextIdentityID string) {
	t.Helper()
	previousActor, err := policyv1.NewAccountIdentityActor(previousIdentityID)
	require.NoError(t, err)
	removePrevious, err := policyv1.Post.DeleteAuthor(postID, previousActor)
	require.NoError(t, err)
	nextActor, err := policyv1.NewAccountIdentityActor(nextIdentityID)
	require.NoError(t, err)
	addNext, err := policyv1.Post.TouchAuthor(postID, nextActor)
	require.NoError(t, err)
	_, err = service.spiceDB.ApplyRelationships(t.Context(), removePrevious, addNext)
	require.NoError(t, err)
}

func seedSeriesFileDerivative(t *testing.T, db *gorm.DB, fileID string) string {
	t.Helper()
	assetID := integrationTestUUID()
	objectKey, err := mediaauth.AssetObjectKey(assetID, "webp")
	require.NoError(t, err)
	now := time.Now().UTC()
	fileSize := int64(512)
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: "thumbnail", ObjectKey: objectKey,
		Extension: "webp", MimeType: "image/webp", FileSize: &fileSize, SHA256: make([]byte, 32),
		Disposition: "inline", Status: model.PublicAssetStatusReady, ReadyAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO file_derivative (file_id, type, asset_id) VALUES (?::uuid, ?, ?::uuid)`,
		fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL.String(), assetID,
	).Error)
	return assetID
}

func requireSeriesStatus(t *testing.T, db *gorm.DB, seriesID string, expected string) {
	t.Helper()
	var status string
	require.NoError(t, db.Table("series").Select("status").Where("id = ?", seriesID).Scan(&status).Error)
	require.Equal(t, expected, status)
}

func requireSeriesSlug(t *testing.T, db *gorm.DB, seriesID string) string {
	t.Helper()
	var slug string
	require.NoError(t, db.Table("series").Select("slug").Where("id = ?", seriesID).Scan(&slug).Error)
	return slug
}

func requireSeriesOrder(t *testing.T, db *gorm.DB, seriesID string, expected []string) {
	t.Helper()
	var actual []string
	require.NoError(t, db.Table("post").Where("series_id = ?", seriesID).
		Order("series_order ASC").Pluck("id", &actual).Error)
	require.Equal(t, expected, actual)
	for index, postID := range expected {
		requirePostSeriesRelation(t, db, postID, &seriesID, intPtr(index))
	}
}

func requirePostSeriesRelation(t *testing.T, db *gorm.DB, postID string, expectedSeriesID *string, expectedOrder *int) {
	t.Helper()
	var row struct {
		SeriesID    *string `gorm:"column:series_id"`
		SeriesOrder *int    `gorm:"column:series_order"`
	}
	require.NoError(t, db.Table("post").Select("series_id, series_order").Where("id = ?", postID).Take(&row).Error)
	require.Equal(t, expectedSeriesID, row.SeriesID)
	require.Equal(t, expectedOrder, row.SeriesOrder)
}

func intPtr(value int) *int { return &value }
