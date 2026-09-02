//go:build integration

package public

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	eventdomain "github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestPublicProgramEventServiceIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	adminID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID:   adminID,
		Name: "Public Program Event Admin",
	})
	adminMemberID := seedPublicAdminMemberIdentityLink(t, db, adminID, "Public Program Event Admin")
	adminCtx := publicProgramEventAdminCtx(adminMemberID, adminID)
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{
		IdentityID: adminID,
		MemberID:   adminMemberID,
		SessionID:  uuid.NewString(),
	})
	require.NoError(t, err)
	adminCtx = sharedtelemetry.WithRequestContext(adminCtx, requestContext)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	contentBlocks, err := contentblock.NewGeneratedStore(publicProgramEventFileReuseAuthorizer{})
	require.NoError(t, err)
	publicFileSvc := publicProgramEventFileGateway{db: db}
	manageFileGateway := publicFileSvc

	typeSvc := eventdomain.NewProgramEventTypeService(db, publicIntegrationSpiceDB)
	typeResp, err := typeSvc.CreateProgramEventType(adminCtx, connect.NewRequest(&managev1.CreateProgramEventTypeRequest{
		Slug:              "public-program-event-type-" + suffix,
		Locale:            "en",
		Name:              "Public Live " + suffix,
		Description:       stringPtr("Public live type"),
		RequiresPlace:     boolPtr(true),
		RequiresStreamUrl: boolPtr(false),
	}))
	require.NoError(t, err)

	seriesPosterFileID, seriesPosterAssetID := seedCanonicalPublicFileFixture(t, db, "series-poster.webp", "image/webp", "poster")
	seriesSvc := eventdomain.NewProgramEventSeriesService(db, newManageProgramEventRuntime("https://cdn.example.com"), publicIntegrationSpiceDB)
	seriesResp, err := seriesSvc.CreateProgramEventSeries(adminCtx, connect.NewRequest(&managev1.CreateProgramEventSeriesRequest{
		Title:        "Public Program Event Series " + suffix,
		Slug:         "public-program-event-series-" + suffix,
		Summary:      stringPtr("Public program event series summary"),
		PosterFileId: stringPtr(seriesPosterFileID),
	}))
	require.NoError(t, err)
	publishedSeriesStatus := managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED
	_, err = seriesSvc.UpdateProgramEventSeries(adminCtx, connect.NewRequest(&managev1.UpdateProgramEventSeriesRequest{
		Id:     seriesResp.Msg.Id,
		Status: &publishedSeriesStatus,
	}))
	require.NoError(t, err)

	placeSvc := referencecatalog.NewMapPlaceService(
		db,
		referencecatalogadapter.NewAssets("https://cdn.example.com"),
		referencecatalogadapter.NewMemberSummaries("https://cdn.example.com"),
		publicIntegrationSpiceDB,
	)
	googlePlaceID := "google-place-" + suffix
	placeResp, err := placeSvc.CreateMapPlace(adminCtx, connect.NewRequest(&managev1.CreateMapPlaceRequest{
		Name:          "Public Event Venue " + suffix,
		Address:       "2 Event Way, Seoul",
		Lat:           37.57,
		Lng:           126.98,
		GooglePlaceId: &googlePlaceID,
	}))
	require.NoError(t, err)

	eventSvc := eventdomain.NewAuditedProgramEventService(
		db,
		newManageProgramEventRuntime("https://cdn.example.com"),
		manageFileGateway,
		apitelemetry.NewDurableWriter(db),
		publicIntegrationSpiceDB,
		programeventadapter.NewCreditMemberSummaries(db, "https://cdn.example.com"),
		eventdomain.WithProgramEventContentBlockStore(contentBlocks),
	)
	startsAt := time.Now().UTC().Add(6 * time.Hour)
	eventResp, err := eventSvc.CreateProgramEvent(adminCtx, connect.NewRequest(&managev1.CreateProgramEventRequest{
		Title:        "Public Program Event " + suffix,
		Slug:         "public-program-event-" + suffix,
		SourceLocale: "en",
		TypeId:       typeResp.Msg.Id,
		StartsAt:     timestamppb.New(startsAt),
		Timezone:     "Asia/Seoul",
		Summary:      stringPtr("Public program event summary"),
		LocationMode: managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_MAP_PLACE,
		MapPlaceId:   stringPtr(placeResp.Msg.Id),
		SeriesId:     stringPtr(seriesResp.Msg.Id),
		SeriesOrder:  publicInt32Ptr(1),
	}))
	require.NoError(t, err)
	posterFileID, posterAssetID := seedCanonicalPublicFileFixture(t, db, "public-poster.webp", "image/webp", "poster")
	blockFileID, blockAssetID := seedCanonicalPublicFileFixture(t, db, "program-event-block.webp", "image/webp", "image")
	posterMedia, err := eventSvc.AddProgramEventMedia(adminCtx, connect.NewRequest(&managev1.AddProgramEventMediaRequest{
		EventId: eventResp.Msg.Id,
		FileId:  posterFileID,
		Role:    "poster",
	}))
	require.NoError(t, err)
	creditRole := "curator"
	_, err = eventSvc.AddProgramEventCredit(adminCtx, connect.NewRequest(&managev1.AddProgramEventCreditRequest{
		EventId:    eventResp.Msg.Id,
		MemberId:   stringPtr(adminMemberID),
		CreditRole: &creditRole,
	}))
	require.NoError(t, err)
	displayCreditRole := "host"
	displayCreditName := "External Host " + suffix
	_, err = eventSvc.AddProgramEventCredit(adminCtx, connect.NewRequest(&managev1.AddProgramEventCreditRequest{
		EventId:     eventResp.Msg.Id,
		DisplayName: &displayCreditName,
		CreditRole:  &displayCreditRole,
	}))
	require.NoError(t, err)

	internalEventSvc := eventdomain.NewInternalProgramEventService(
		db,
		nil,
		eventdomain.WithInternalProgramEventSpiceDB(publicIntegrationSpiceDB),
		eventdomain.WithInternalProgramEventContentBlockStore(contentBlocks),
		eventdomain.WithInternalProgramEventMediaHydrator(manageFileGateway),
		eventdomain.WithInternalProgramEventCheckpoints(testcollaboration.NewCheckpoints(db, publicIntegrationSpiceDB)),
	)
	blockID := uuid.NewString()
	sourceApplied, err := internalEventSvc.ApplyProgramEventBlockBatch(adminCtx, connect.NewRequest(&intrav1.ApplyProgramEventBlockBatchRequest{
		EventId: eventResp.Msg.Id,
		Locale:  "en",
		Batch: publicProgramEventParagraphBatch(
			eventResp.Msg.Document,
			eventResp.Msg.DocumentRevision,
			blockID,
			"en",
			"Public program event body",
			adminMemberID,
			true,
		),
	}))
	require.NoError(t, err)
	sourceLoaded, err := eventSvc.GetProgramEvent(adminCtx, connect.NewRequest(&managev1.GetProgramEventRequest{Id: eventResp.Msg.Id}))
	require.NoError(t, err)
	fileBlockID := uuid.NewString()
	fileApplied, err := internalEventSvc.ApplyProgramEventBlockBatch(adminCtx, connect.NewRequest(&intrav1.ApplyProgramEventBlockBatchRequest{
		EventId: eventResp.Msg.Id,
		Locale:  "en",
		Batch: publicProgramEventFileBatch(
			sourceLoaded.Msg.Document,
			sourceApplied.Msg.DocumentRevision,
			fileBlockID,
			blockFileID,
			"program-event-block.webp",
			"en",
			"Public block alt",
			"Public block caption",
			adminMemberID,
		),
	}))
	require.NoError(t, err)
	setPublicDownloadAudience(t, db, fileBlockID, "public")
	fileLoaded, err := eventSvc.GetProgramEvent(adminCtx, connect.NewRequest(&managev1.GetProgramEventRequest{Id: eventResp.Msg.Id}))
	require.NoError(t, err)
	requirePublicProgramEventManageBlockMedia(t, fileLoaded.Msg.BlockMedia, blockFileID, fileBlockID)
	localizedSummary := "공개 프로그램 이벤트 요약"
	translatedRevision := seedPublicProgramEventMachineTranslation(
		t, db, contentBlocks, eventResp.Msg.Id, fileLoaded.Msg.Document,
		fileApplied.Msg.DocumentRevision, blockID, fileBlockID, blockAssetID, localizedSummary,
	)
	managed, err := eventSvc.GetProgramEvent(adminCtx, connect.NewRequest(&managev1.GetProgramEventRequest{Id: eventResp.Msg.Id}))
	require.NoError(t, err)
	requirePublicProgramEventManageBlockMedia(t, managed.Msg.BlockMedia, blockFileID, fileBlockID)
	_, err = eventSvc.ArchiveProgramEvent(adminCtx, connect.NewRequest(&managev1.ArchiveProgramEventRequest{Id: eventResp.Msg.Id}))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	_, err = eventSvc.PublishProgramEvent(adminCtx, connect.NewRequest(&managev1.PublishProgramEventRequest{Id: eventResp.Msg.Id}))
	require.NoError(t, err)

	publicEventSvc := NewProgramEventService(
		db,
		newPublicProgramEventAssets(db, "https://cdn.example.com"),
		programeventadapter.NewPublicCreditMemberSummaries(db, "https://cdn.example.com"),
		WithProgramEventContentBlockStore(contentBlocks),
		WithProgramEventFileService(publicFileSvc),
	)
	getReq := connect.NewRequest(&openv1.GetProgramEventRequest{Slug: "public-program-event-" + suffix})
	getReq.Header().Set("Accept-Language", "ko")
	fetched, err := publicEventSvc.Get(context.Background(), getReq)
	require.NoError(t, err)
	require.NotNil(t, fetched.Msg.Event)
	require.Equal(t, eventResp.Msg.Id, fetched.Msg.Event.Id)
	require.Equal(t, "Public Program Event "+suffix, fetched.Msg.Event.Title)
	require.Equal(t, "public-program-event-"+suffix, fetched.Msg.Event.GetSlug())
	require.Equal(t, openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED, fetched.Msg.Event.Status)
	require.Equal(t, localizedSummary, fetched.Msg.Event.GetSummary())
	require.Equal(t,
		"<p>공개 프로그램 이벤트 본문</p><figure><img src=\"https://cdn.example.com/asset/"+blockAssetID+"/image.webp\" alt=\"공개 블록 대체 텍스트\"><figcaption>공개 블록 캡션</figcaption></figure>",
		fetched.Msg.Event.GetContentHtml(),
	)
	require.Equal(t, "공개 프로그램 이벤트 본문\nprogram-event-block.webp\n공개 블록 캡션", fetched.Msg.Event.GetContentText())
	require.Equal(t, "ko", fetched.Msg.Event.GetDocument().GetLocale())
	require.Equal(t, contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT, fetched.Msg.Event.GetDocument().GetProfile())
	require.Len(t, fetched.Msg.Event.GetDocument().GetBase().GetNodes(), 2)
	require.Len(t, fetched.Msg.Event.GetDocument().GetLocaleOverlay().GetBlocks(), 2)
	require.Equal(t, translatedRevision, fetched.Msg.Event.GetDocumentRevision())
	requirePublicProgramEventHydratedBlockMedia(t, fetched.Msg.BlockMedia, blockFileID, blockAssetID, fileBlockID)
	require.Equal(t, placeResp.Msg.Id, fetched.Msg.Event.GetMapPlaceId())
	require.NotNil(t, fetched.Msg.Event.LocationPlace)
	require.Equal(t, googlePlaceID, fetched.Msg.Event.LocationPlace.GetGooglePlaceId())
	require.Equal(t, "Public Event Venue "+suffix, fetched.Msg.Event.LocationPlace.Name)
	require.Equal(t, "2 Event Way, Seoul", fetched.Msg.Event.LocationPlace.GetAddress())
	require.Equal(t, 37.57, fetched.Msg.Event.LocationPlace.Lat)
	require.Equal(t, 126.98, fetched.Msg.Event.LocationPlace.Lng)
	require.Equal(t, seriesResp.Msg.Id, fetched.Msg.Event.GetSeriesId())
	require.NotNil(t, fetched.Msg.Event.Series)
	require.NotNil(t, fetched.Msg.Event.Type)
	require.Equal(t, typeResp.Msg.Id, fetched.Msg.Event.Type.Id)
	require.Equal(t, "Public Live "+suffix, fetched.Msg.Event.Type.Name)
	require.True(t, fetched.Msg.Event.Type.RequiresPlace)
	require.Equal(t, "https://cdn.example.com/asset/"+posterAssetID+"/poster.webp", fetched.Msg.Event.GetPosterAsset().GetUrl())
	require.Len(t, fetched.Msg.Event.Credits, 2)
	require.NotNil(t, fetched.Msg.Event.Credits[0].Member)
	require.Equal(t, "Public Program Event Admin", fetched.Msg.Event.Credits[0].Member.GetNickname())
	require.Equal(t, creditRole, fetched.Msg.Event.Credits[0].GetCreditRole())
	require.Equal(t, displayCreditName, fetched.Msg.Event.Credits[1].GetDisplayName())
	require.Equal(t, displayCreditRole, fetched.Msg.Event.Credits[1].GetCreditRole())

	fetchedByID, err := publicEventSvc.Get(context.Background(), connect.NewRequest(&openv1.GetProgramEventRequest{Slug: eventResp.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, eventResp.Msg.Id, fetchedByID.Msg.Event.Id)

	listed, err := publicEventSvc.List(context.Background(), connect.NewRequest(&openv1.ListProgramEventsRequest{
		Filters: []*commonv1.FilterSpec{
			nil,
			{
				Field: "type_id",
				Op:    commonv1.FilterOp_FILTER_OP_EQ,
				Value: typeResp.Msg.Id,
			},
		},
	}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.Events, 1)
	require.Equal(t, eventResp.Msg.Id, listed.Msg.Events[0].Id)
	require.Equal(t, "https://cdn.example.com/asset/"+posterAssetID+"/poster.webp", listed.Msg.Events[0].GetPosterAsset().GetUrl())

	archived, err := eventSvc.ArchiveProgramEvent(adminCtx, connect.NewRequest(&managev1.ArchiveProgramEventRequest{Id: eventResp.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED, archived.Msg.Status)

	archivedFetched, err := publicEventSvc.Get(context.Background(), connect.NewRequest(&openv1.GetProgramEventRequest{
		Slug: "public-program-event-" + suffix,
	}))
	require.NoError(t, err)
	require.Equal(t, openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED, archivedFetched.Msg.Event.Status)

	archivedListed, err := publicEventSvc.List(context.Background(), connect.NewRequest(&openv1.ListProgramEventsRequest{
		Filters: []*commonv1.FilterSpec{
			{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "Public Program Event " + suffix},
			{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String()},
		},
	}))
	require.NoError(t, err)
	require.Len(t, archivedListed.Msg.Events, 1)
	require.Equal(t, eventResp.Msg.Id, archivedListed.Msg.Events[0].Id)

	archivedTimezone := "UTC"
	_, err = eventSvc.UpdateProgramEvent(adminCtx, connect.NewRequest(&managev1.UpdateProgramEventRequest{
		Id:       eventResp.Msg.Id,
		Timezone: &archivedTimezone,
	}))
	require.NoError(t, err)

	_, err = eventSvc.ReorderProgramEventMedia(adminCtx, connect.NewRequest(&managev1.ReorderProgramEventMediaRequest{
		EventId:  eventResp.Msg.Id,
		Role:     "poster",
		MediaIds: []string{posterMedia.Msg.Media.Id},
	}))
	require.NoError(t, err)

	archivedCreditName := "Archived Event Credit"
	_, err = eventSvc.AddProgramEventCredit(adminCtx, connect.NewRequest(&managev1.AddProgramEventCreditRequest{
		EventId:     eventResp.Msg.Id,
		DisplayName: &archivedCreditName,
	}))
	require.NoError(t, err)

	republished, err := eventSvc.PublishProgramEvent(adminCtx, connect.NewRequest(&managev1.PublishProgramEventRequest{Id: eventResp.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED, republished.Msg.Status)
	editableTimezone := "UTC"
	_, err = eventSvc.UpdateProgramEvent(adminCtx, connect.NewRequest(&managev1.UpdateProgramEventRequest{
		Id:       eventResp.Msg.Id,
		Timezone: &editableTimezone,
	}))
	require.NoError(t, err)

	publicSeriesSvc := NewProgramEventSeriesService(db, newPublicProgramEventAssets(db, "https://cdn.example.com"))
	seriesGet, err := publicSeriesSvc.Get(context.Background(), connect.NewRequest(&openv1.GetProgramEventSeriesRequest{
		Slug: "public-program-event-series-" + suffix,
	}))
	require.NoError(t, err)
	require.NotNil(t, seriesGet.Msg.Series)
	require.Equal(t, seriesResp.Msg.Id, seriesGet.Msg.Series.Id)
	require.Equal(
		t,
		openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED,
		seriesGet.Msg.Series.Status,
	)
	require.Equal(t, "Public Program Event Series "+suffix, seriesGet.Msg.Series.Title)
	require.Equal(t, "Public program event series summary", seriesGet.Msg.Series.GetSummary())
	require.Equal(t, "https://cdn.example.com/asset/"+seriesPosterAssetID+"/poster.webp", seriesGet.Msg.Series.GetPosterAsset().GetUrl())

	seriesGetByID, err := publicSeriesSvc.Get(context.Background(), connect.NewRequest(&openv1.GetProgramEventSeriesRequest{
		Slug: seriesResp.Msg.Id,
	}))
	require.NoError(t, err)
	require.Equal(t, seriesResp.Msg.Id, seriesGetByID.Msg.Series.Id)

	seriesList, err := publicSeriesSvc.List(context.Background(), connect.NewRequest(&openv1.ListProgramEventSeriesRequest{
		Filters: []*commonv1.FilterSpec{
			nil,
			{
				Field: "search",
				Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
				Value: "Public Program Event Series " + suffix,
			},
		},
	}))
	require.NoError(t, err)
	require.Len(t, seriesList.Msg.Series, 1)
	require.Equal(t, seriesResp.Msg.Id, seriesList.Msg.Series[0].Id)

	publicTypeSvc := NewProgramEventTypeService(db)
	typeList, err := publicTypeSvc.List(context.Background(), connect.NewRequest(&openv1.ListProgramEventTypesRequest{
		Filters: []*commonv1.FilterSpec{
			nil,
			{
				Field: "search",
				Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
				Value: "Public Live " + suffix,
			},
		},
	}))
	require.NoError(t, err)
	require.Len(t, typeList.Msg.Types, 1)
	require.Equal(t, typeResp.Msg.Id, typeList.Msg.Types[0].Id)
}

func publicProgramEventParagraphBatch(
	document *contentv1.RichTextDocument,
	expectedRevision string,
	blockID string,
	locale string,
	text string,
	contributorMemberID string,
	includeBase bool,
) *contentv1.RichTextBlockMutationBatch {
	batch := &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(),
		Profile:                 document.GetProfile(),
		ExpectedRevision:        expectedRevision,
		ContributorMemberIds:    []string{contributorMemberID},
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: locale,
			Mutations: []*contentv1.RichTextBlockLocaleMutation{{
				Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
					Block: &contentv1.RichTextBlockLocale{
						BlockId: blockID,
						Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
							Props: &contentv1.ParagraphLocaleProps{},
							Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
								Text: &contentv1.RichTextStyledText{Text: text},
							}}},
						}},
					},
				}},
			}},
		}},
	}
	if includeBase {
		batch.BaseMutations = []*contentv1.RichTextBlockMutation{{
			Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
				Node: &contentv1.RichTextBlockNode{
					Block: &contentv1.RichTextBlock{
						Id:    blockID,
						Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}},
					},
					Placement: &contentv1.ContentBlockPlacement{Index: 0},
				},
			}},
		}}
	}
	return batch
}

func publicProgramEventFileBatch(
	document *contentv1.RichTextDocument,
	expectedRevision string,
	blockID string,
	fileID string,
	name string,
	locale string,
	alt string,
	caption string,
	contributorMemberID string,
) *contentv1.RichTextBlockMutationBatch {
	return &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(),
		Profile:                 document.GetProfile(),
		ExpectedRevision:        expectedRevision,
		ContributorMemberIds:    []string{contributorMemberID},
		BaseMutations: []*contentv1.RichTextBlockMutation{{
			Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
				Node: &contentv1.RichTextBlockNode{
					Block: &contentv1.RichTextBlock{
						Id: blockID,
						Value: &contentv1.RichTextBlock_File{File: &contentv1.FileBlock{Props: &contentv1.FileProps{
							Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: fileID}},
							Name:       &name,
						}}},
					},
					Placement: &contentv1.ContentBlockPlacement{Index: 1},
				},
			}},
		}},
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{
			publicProgramEventFileLocaleMutationGroup(blockID, locale, alt, caption),
		},
	}
}

func seedPublicProgramEventMachineTranslation(
	t *testing.T,
	db *gorm.DB,
	store *contentblock.Store,
	eventID string,
	document *contentv1.RichTextDocument,
	expectedRevision string,
	paragraphBlockID string,
	fileBlockID string,
	fileAssetID string,
	summary string,
) string {
	t.Helper()
	var event model.ProgramEvent
	require.NoError(t, db.Select("content_document_id").Where("id = ?", eventID).Take(&event).Error)
	require.NotNil(t, event.ContentDocumentID)
	documentID, err := uuid.Parse(*event.ContentDocumentID)
	require.NoError(t, err)
	paragraph := &contentv1.RichTextBlockLocale{
		BlockId: paragraphBlockID,
		Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
			Props: &contentv1.ParagraphLocaleProps{},
			Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{
				Text: "공개 프로그램 이벤트 본문",
			}}}},
		}},
	}
	file := &contentv1.RichTextBlockLocale{
		BlockId: fileBlockID,
		Value: &contentv1.RichTextBlockLocale_File{File: &contentv1.FileBlockLocale{Props: &contentv1.FileLocaleProps{
			Alt: stringPtr("공개 블록 대체 텍스트"), Caption: stringPtr("공개 블록 캡션"),
		}}},
	}
	batch, err := contentblock.BatchFromRichTextSystemProto(documentID, &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(),
		Profile:                 document.GetProfile(),
		ExpectedRevision:        expectedRevision,
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: "ko",
			Mutations: []*contentv1.RichTextBlockLocaleMutation{
				{Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{Block: paragraph}}},
				{Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{Block: file}}},
			},
		}},
	})
	require.NoError(t, err)
	var translatedRevision string
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		result, applyErr := store.ApplyBatch(t.Context(), tx, batch, func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: "en"}, nil
		})
		if applyErr != nil {
			return applyErr
		}
		translatedRevision = result.DocumentRevision.String()
		now := time.Now().UTC()
		return tx.Exec(`
			INSERT INTO program_event_translation (
				entity_id, locale, summary, created_at, updated_at
			)
			VALUES (?, 'ko', ?, ?, ?)
			ON CONFLICT (entity_id, locale) DO UPDATE SET
				summary = EXCLUDED.summary,
				updated_at = EXCLUDED.updated_at`,
			eventID, summary, now, now,
		).Error
	}))
	require.NotEmpty(t, translatedRevision)
	return translatedRevision
}

func publicProgramEventFileLocaleMutationGroup(
	blockID string,
	locale string,
	alt string,
	caption string,
) *contentv1.RichTextLocaleMutationGroup {
	return &contentv1.RichTextLocaleMutationGroup{
		Locale: locale,
		Mutations: []*contentv1.RichTextBlockLocaleMutation{{
			Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
				Block: &contentv1.RichTextBlockLocale{
					BlockId: blockID,
					Value: &contentv1.RichTextBlockLocale_File{File: &contentv1.FileBlockLocale{Props: &contentv1.FileLocaleProps{
						Alt: &alt, Caption: &caption,
					}}},
				},
			}},
		}},
	}
}

func requirePublicProgramEventHydratedBlockMedia(
	t *testing.T,
	items []*contentv1.ContentBlockMediaItem,
	fileID string,
	assetID string,
	blockID string,
) {
	t.Helper()
	require.Len(t, items, 1)
	item := items[0]
	require.Equal(t, blockID, item.GetSelector().GetBlockId())
	require.Equal(t, "file", item.GetSelector().GetReferencePath())
	require.Equal(t, fileID, item.GetAttachment().GetActiveFileId())
	require.Equal(t, fileID, item.GetDelivery().GetFileId())
	require.Equal(t, "image/webp", item.GetDelivery().GetMimeType())
	require.Equal(t, assetID, item.GetDelivery().GetAsset().GetAssetId())
	require.Equal(t, "https://cdn.example.com/asset/"+assetID+"/image.webp", item.GetDelivery().GetAsset().GetUrl())
	require.Equal(t, assetID, item.GetDelivery().GetThumbnail().GetAssetId())
	require.Equal(t, fileID, item.GetDelivery().GetDownload().GetFileId())
	require.Equal(t, commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_DOWNLOAD, item.GetDelivery().GetDownload().GetPurpose())
	require.Equal(t, contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_AVAILABLE, item.GetDownloadAvailability())
	require.Equal(t, contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_DOWNLOAD, item.GetDownloadAction())
}

func requirePublicProgramEventManageBlockMedia(
	t *testing.T,
	items []*contentv1.ContentBlockMediaItem,
	fileID string,
	blockID string,
) {
	t.Helper()
	require.Len(t, items, 1)
	item := items[0]
	require.Equal(t, blockID, item.GetSelector().GetBlockId())
	require.Equal(t, "file", item.GetSelector().GetReferencePath())
	require.Equal(t, fileID, item.GetAttachment().GetActiveFileId())
	require.Equal(t, fileID, item.GetDelivery().GetFileId())
	require.True(t, item.GetDelivery().GetInline() != nil || item.GetDelivery().GetAsset() != nil)
	require.NotNil(t, item.GetDelivery().GetDownload())
	require.Equal(t, contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_AVAILABLE, item.GetDownloadAvailability())
	require.Equal(t, contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_DOWNLOAD, item.GetDownloadAction())
}

type publicProgramEventFileGateway struct {
	db *gorm.DB
}

type publicProgramEventFileReuseAuthorizer struct{}

func (publicProgramEventFileReuseAuthorizer) AuthorizeFileReuse(
	context.Context,
	*gorm.DB,
	contentblock.Document,
	contentblock.FullBlock,
	contentblock.FileReference,
	contentblock.File,
) error {
	return nil
}

func setPublicDownloadAudience(t *testing.T, db *gorm.DB, blockID, audience string) {
	t.Helper()
	require.NoError(t, db.Exec(
		`UPDATE content_block_attachment SET download_audience = ? WHERE block_id = ? AND reference_path = 'file'`, audience, blockID,
	).Error)
}

func (publicProgramEventFileGateway) DeleteFileByID(context.Context, string) error { return nil }

func (gateway publicProgramEventFileGateway) HydrateAuthorizedContentBlockMedia(
	ctx context.Context,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	result := make([]*contentv1.ContentBlockMediaItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		copy := proto.Clone(item).(*contentv1.ContentBlockMediaItem)
		fileID := copy.GetAttachment().GetActiveFileId()
		if fileID != "" {
			asset := newPublicProgramEventAssets(gateway.db, "https://cdn.example.com").
				ResolveReadyAssetForSourceFile(ctx, fileID, "image")
			copy.Delivery = &commonv1.MediaDelivery{
				FileId:    fileID,
				MimeType:  "image/webp",
				Asset:     asset,
				Thumbnail: asset,
				Download: &commonv1.ExpiringMediaRef{
					FileId:  fileID,
					Url:     "https://media.example.com/download/" + fileID,
					Purpose: commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_DOWNLOAD,
				},
			}
			copy.DownloadAvailability = contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_AVAILABLE
			copy.DownloadAction = contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_DOWNLOAD
		}
		result = append(result, copy)
	}
	return result, nil
}

func (gateway publicProgramEventFileGateway) HydrateAuthorizedProgramEventBlockMediaWithDB(
	ctx context.Context,
	_ *gorm.DB,
	_ string,
	_ uuid.UUID,
	_ *auth.UserInfo,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return gateway.HydrateAuthorizedContentBlockMedia(ctx, items)
}

func publicProgramEventAdminCtx(memberID string, identityID string) context.Context {
	return publicPrincipalContext(memberID, identityID)
}

func publicInt32Ptr(value int32) *int32 {
	return &value
}
