//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestWorkServiceAdminListIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Work Admin List Admin")
	ctx := workIntegrationAdminCtx(adminID)
	fileDeleter := &recordingWorkFileDeleter{}
	workSvc := newWorkIntegrationService(t, db, adminID, fileDeleter)
	workSpiceDB := integrationSpiceDB(t)

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	searchPrefix := "Work Admin List " + suffix
	isPresent := true
	alphaSlug := "work-admin-list-alpha-" + suffix
	gammaSlug := "work-admin-list-gamma-" + suffix

	alpha, err := workSvc.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title:     searchPrefix + " Alpha",
		Slug:      &alphaSlug,
		Type:      managev1.WorkType_WORK_TYPE_MUSIC_PROJECT,
		Year:      2026,
		Month:     1,
		IsPresent: &isPresent,
		Document:  emptyWorkIntegrationDocument("en"),
	}))
	require.NoError(t, err)
	_, err = workSvc.PublishWork(ctx, connect.NewRequest(&managev1.PublishWorkRequest{Id: alpha.Msg.Id}))
	require.NoError(t, err)

	beta, err := workSvc.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title:     searchPrefix + " Beta",
		Type:      managev1.WorkType_WORK_TYPE_PORTFOLIO,
		Year:      2026,
		Month:     2,
		IsPresent: &isPresent,
		Document:  emptyWorkIntegrationDocument("en"),
	}))
	require.NoError(t, err)

	gamma, err := workSvc.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title:     searchPrefix + " Gamma",
		Slug:      &gammaSlug,
		Type:      managev1.WorkType_WORK_TYPE_ARTICLE,
		Year:      2026,
		Month:     3,
		IsPresent: &isPresent,
		Document:  emptyWorkIntegrationDocument("en"),
	}))
	require.NoError(t, err)

	mapPlaceSvc := mapPlaceServiceForTest(t, db, "cdn.example.com", workSpiceDB)
	mapPlace, err := mapPlaceSvc.CreateMapPlace(ctx, connect.NewRequest(&managev1.CreateMapPlaceRequest{
		Name:    "Work Admin List Place " + suffix,
		Address: "1 Test Road",
		Lat:     37.5,
		Lng:     127.0,
	}))
	require.NoError(t, err)
	updatedGamma, err := workSvc.UpdateWork(ctx, connect.NewRequest(&managev1.UpdateWorkRequest{
		Id:         gamma.Msg.Id,
		MapPlaceId: &mapPlace.Msg.Id,
	}))
	require.NoError(t, err)
	require.Equal(t, mapPlace.Msg.Id, updatedGamma.Msg.GetMapPlaceId())

	_, err = workSvc.UpdateWork(ctx, connect.NewRequest(&managev1.UpdateWorkRequest{
		Id:         gamma.Msg.Id,
		MapPlaceId: ptrString("00000000-0000-0000-0000-000000000000"),
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	creditName := "Integration Credit"
	creditRole := "Director"
	_, err = workSvc.AddWorkCredit(ctx, connect.NewRequest(&managev1.AddWorkCreditRequest{
		WorkId:     beta.Msg.Id,
		Name:       &creditName,
		CreditRole: &creditRole,
	}))
	require.NoError(t, err)

	featuredFileID := integrationTestUUID()
	seedIntegrationFile(t, db, featuredFileID, "list", "image/webp", nil)
	_, err = workSvc.SetWorkFeaturedImage(ctx, connect.NewRequest(&managev1.SetWorkFeaturedImageRequest{
		WorkId: beta.Msg.Id,
		FileId: featuredFileID,
	}))
	require.NoError(t, err)

	paged, err := workSvc.ListWorksAdmin(ctx, connect.NewRequest(&managev1.ListWorksAdminRequest{
		Filters: []*commonv1.FilterSpec{{
			Field: "search",
			Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
			Value: searchPrefix,
		}},
		Sorts:      []*commonv1.SortSpec{{Field: "title", Order: commonv1.SortOrder_SORT_ORDER_ASC}},
		Pagination: &commonv1.PaginationRequest{Limit: 2},
	}))
	require.NoError(t, err)
	require.Equal(t, []string{alpha.Msg.Id, beta.Msg.Id}, workWithStatsIDs(paged.Msg.Works))
	require.Equal(t, int32(3), paged.Msg.Pagination.Total)
	require.True(t, paged.Msg.Pagination.HasMore)
	require.Equal(t, int32(0), paged.Msg.Works[0].CreditCount)
	require.Equal(t, int32(1), paged.Msg.Works[1].CreditCount)
	require.Equal(t, int32(0), paged.Msg.Works[1].ClientCount)

	published, err := workSvc.ListWorksAdmin(ctx, connect.NewRequest(&managev1.ListWorksAdminRequest{
		Filters: []*commonv1.FilterSpec{
			{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: searchPrefix},
			{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: managev1.WorkStatus_WORK_STATUS_PUBLISHED.String()},
		},
		Sorts: []*commonv1.SortSpec{{Field: "title", Order: commonv1.SortOrder_SORT_ORDER_ASC}},
	}))
	require.NoError(t, err)
	require.Equal(t, []string{alpha.Msg.Id}, workWithStatsIDs(published.Msg.Works))

	withoutSlug, err := workSvc.ListWorksAdmin(ctx, connect.NewRequest(&managev1.ListWorksAdminRequest{
		Filters: []*commonv1.FilterSpec{
			{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: searchPrefix},
			{Field: "has_slug", Value: "false"},
		},
	}))
	require.NoError(t, err)
	require.Equal(t, []string{beta.Msg.Id}, workWithStatsIDs(withoutSlug.Msg.Works))
	require.Empty(t, withoutSlug.Msg.Works[0].Work.GetSlug())

	withFeaturedImage, err := workSvc.ListWorksAdmin(ctx, connect.NewRequest(&managev1.ListWorksAdminRequest{
		Filters: []*commonv1.FilterSpec{
			{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: searchPrefix},
			{Field: "has_featured_image", Value: "true"},
		},
	}))
	require.NoError(t, err)
	require.Equal(t, []string{beta.Msg.Id}, workWithStatsIDs(withFeaturedImage.Msg.Works))
	require.Contains(t, withFeaturedImage.Msg.Works[0].Work.GetFeaturedImageAsset().GetUrl(), "/asset/")

	articleWorks, err := workSvc.ListWorksAdmin(ctx, connect.NewRequest(&managev1.ListWorksAdminRequest{
		Filters: []*commonv1.FilterSpec{
			{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: searchPrefix},
			{Field: "type", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: managev1.WorkType_WORK_TYPE_ARTICLE.String()},
		},
	}))
	require.NoError(t, err)
	require.Equal(t, []string{gamma.Msg.Id}, workWithStatsIDs(articleWorks.Msg.Works))

	filteredByMapPlace, err := workSvc.ListWorksAdmin(ctx, connect.NewRequest(&managev1.ListWorksAdminRequest{
		Filters: []*commonv1.FilterSpec{
			{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: searchPrefix},
			{Field: "map_place_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: mapPlace.Msg.Id},
		},
	}))
	require.NoError(t, err)
	require.Equal(t, []string{gamma.Msg.Id}, workWithStatsIDs(filteredByMapPlace.Msg.Works))
}

func TestWorkContentUnlinkPreservesLibraryFilesIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Work File Retention Admin")
	ctx := workIntegrationAdminCtx(adminID)
	fileDeleter := &recordingWorkFileDeleter{}
	workSvc := newWorkIntegrationService(t, db, adminID, fileDeleter)
	workSpiceDB := integrationSpiceDB(t)
	isPresent := true

	created, err := workSvc.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title:     "Work File Retention " + integrationTestUUID(),
		Type:      managev1.WorkType_WORK_TYPE_ARTICLE,
		Year:      2026,
		Month:     8,
		IsPresent: &isPresent,
		Document:  emptyWorkIntegrationDocument("en"),
	}))
	require.NoError(t, err)
	requireSynchronousAuthorizedResource(t, workSpiceDB, policyv1.Work.LookupManage(), created.Msg.Id, adminID, true)

	firstFeaturedID := integrationTestUUID()
	secondFeaturedID := integrationTestUUID()
	for _, fileID := range []string{firstFeaturedID, secondFeaturedID} {
		seedIntegrationFile(t, db, fileID, "work-featured", "image/webp", nil)
	}
	_, err = workSvc.SetWorkFeaturedImage(ctx, connect.NewRequest(&managev1.SetWorkFeaturedImageRequest{
		WorkId: created.Msg.Id,
		FileId: firstFeaturedID,
	}))
	require.NoError(t, err)
	_, err = workSvc.SetWorkFeaturedImage(ctx, connect.NewRequest(&managev1.SetWorkFeaturedImageRequest{
		WorkId: created.Msg.Id,
		FileId: secondFeaturedID,
	}))
	require.NoError(t, err)
	_, err = workSvc.DeleteWorkFeaturedImage(ctx, connect.NewRequest(&managev1.DeleteWorkFeaturedImageRequest{
		WorkId: created.Msg.Id,
	}))
	require.NoError(t, err)
	require.Empty(t, fileDeleter.deletedIDs)
	requireFileRowExists(t, db, firstFeaturedID)
	requireFileRowExists(t, db, secondFeaturedID)

	editorFileID := integrationTestUUID()
	ownerFeaturedID := integrationTestUUID()
	seedIntegrationFile(t, db, editorFileID, "work-editor", "image/webp", nil)
	seedIntegrationFile(t, db, ownerFeaturedID, "work-owner-featured", "image/webp", nil)
	blockID := integrationTestUUID()
	workBlocks, blockStoreErr := contentblock.NewGeneratedStore(newContentBlockFileReuseAuthorizer(workSpiceDB))
	require.NoError(t, blockStoreErr)
	internalWork := workdomain.NewInternalWorkService(
		db,
		noopAsyncPublisher{},
		newWorkRuntimeForTest(db, ""),
		workSpiceDB,
		workdomain.WithInternalWorkDomainAuditWriter(apitelemetry.NewDurableWriter(db)),
		workdomain.WithInternalWorkContentBlockStore(workBlocks),
		workdomain.WithInternalWorkCheckpoints(testcollaboration.NewCheckpoints(db, workSpiceDB)),
	)
	applied, err := internalWork.ApplyWorkBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyWorkBlockBatchRequest{
		WorkId: created.Msg.Id,
		Locale: "en",
		Batch: &contentv1.RichTextBlockMutationBatch{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
			ExpectedRevision:        created.Msg.Revision,
			ContributorMemberIds:    []string{integrationMemberID(adminID)},
			BaseMutations: []*contentv1.RichTextBlockMutation{{
				Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
					Node: &contentv1.RichTextBlockNode{
						Block: &contentv1.RichTextBlock{
							Id: blockID,
							Value: &contentv1.RichTextBlock_File{File: &contentv1.FileBlock{Props: &contentv1.FileProps{
								Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: editorFileID}},
							}}},
						},
						Placement: &contentv1.ContentBlockPlacement{Index: 0},
					},
				}},
			}},
			LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
				Locale: "en",
				Mutations: []*contentv1.RichTextBlockLocaleMutation{{
					Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
						Block: &contentv1.RichTextBlockLocale{
							BlockId: blockID,
							Value:   &contentv1.RichTextBlockLocale_File{File: &contentv1.FileBlockLocale{Props: &contentv1.FileLocaleProps{}}},
						},
					}},
				}},
			}},
		},
	}))
	require.NoError(t, err)
	require.True(t, applied.Msg.Changed)
	var contentFileReferenceCount int64
	require.NoError(t, db.Table("content_block_attachment").
		Where("block_id = ? AND reference_path = ? AND file_id = ?", blockID, "file", editorFileID).
		Count(&contentFileReferenceCount).Error)
	require.EqualValues(t, 1, contentFileReferenceCount)
	_, err = workSvc.SetWorkFeaturedImage(ctx, connect.NewRequest(&managev1.SetWorkFeaturedImageRequest{
		WorkId: created.Msg.Id,
		FileId: ownerFeaturedID,
	}))
	require.NoError(t, err)
	var binding model.PublicAssetBinding
	require.NoError(t, db.Where(
		"owner_type = ? AND owner_id = ? AND binding_key = ?",
		"work", created.Msg.Id, "featured_image",
	).Take(&binding).Error)
	deleted, err := workSvc.DeleteWork(ctx, connect.NewRequest(&managev1.DeleteWorkRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.True(t, deleted.Msg.Success)
	requireSynchronousAuthorizedResource(t, workSpiceDB, policyv1.Work.LookupManage(), created.Msg.Id, adminID, false)
	require.Empty(t, fileDeleter.deletedIDs)
	requireFileRowExists(t, db, editorFileID)
	requireFileRowExists(t, db, ownerFeaturedID)
	var bindingCount int64
	require.NoError(t, db.Model(&model.PublicAssetBinding{}).Where(
		"owner_type = ? AND owner_id = ? AND binding_key = ?",
		binding.OwnerType, binding.OwnerID, binding.BindingKey,
	).Count(&bindingCount).Error)
	require.Zero(t, bindingCount)
	var asset model.PublicAsset
	require.NoError(t, db.Select("id", "status").Take(&asset, "id = ?", binding.AssetID).Error)
	require.Equal(t, model.PublicAssetStatusReady, asset.Status)
	require.NoError(t, db.Table("content_block_attachment").
		Where("block_id = ?", blockID).
		Count(&contentFileReferenceCount).Error)
	require.Zero(t, contentFileReferenceCount)
}

type recordingWorkFileDeleter struct {
	deletedIDs []string
}

func (d *recordingWorkFileDeleter) DeleteFileByID(_ context.Context, fileID string) error {
	d.deletedIDs = append(d.deletedIDs, fileID)
	return nil
}

func (d *recordingWorkFileDeleter) ReconcilePublishedEntityAssets(
	context.Context,
	managev1.TranscodeEntityType,
	string,
) error {
	return nil
}

func workWithStatsIDs(works []*managev1.WorkWithStats) []string {
	ids := make([]string, 0, len(works))
	for _, work := range works {
		if work.GetWork() == nil {
			continue
		}
		ids = append(ids, work.GetWork().GetId())
	}
	return ids
}
