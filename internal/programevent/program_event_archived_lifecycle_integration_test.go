//go:build integration

package programevent

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestArchivedProgramEventAllowsAdminEditingAndAuthorReadOnlyIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Archived Program Event Admin")
	spiceDB := testutil.SetupOryStack(t).SpiceDBClient
	grantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	ctx := programEventIntegrationAdminCtx(adminID)
	suffix := integrationTestUUID()
	store := newProgramEventIntegrationContentBlockStore(t, spiceDB)
	files := newProgramEventIntegrationFileService(db, spiceDB)

	typeResponse, err := NewProgramEventTypeService(db, spiceDB).CreateProgramEventType(
		ctx,
		connect.NewRequest(&managev1.CreateProgramEventTypeRequest{
			Slug:              "archived-program-event-" + suffix,
			Locale:            "en",
			Name:              "Archived Program Event " + suffix,
			RequiresPlace:     ptrBool(false),
			RequiresStreamUrl: ptrBool(false),
		}),
	)
	require.NoError(t, err)

	eventService := NewProgramEventService(db, newProgramEventRuntime("https://cdn.example.com"), spiceDB, newProgramEventCreditMemberSummaries(db, "https://cdn.example.com"), files)
	eventService.contentBlocks = store
	created, err := eventService.CreateProgramEvent(ctx, connect.NewRequest(&managev1.CreateProgramEventRequest{
		Title:        "Archived Program Event " + suffix,
		Slug:         "archived-program-event-" + suffix,
		SourceLocale: "en",
		TypeId:       typeResponse.Msg.Id,
		StartsAt:     timestamppb.New(time.Now().UTC().Add(time.Hour)),
		Timezone:     "Asia/Seoul",
		LocationMode: managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE,
	}))
	require.NoError(t, err)
	sessionID := insertProgramEventIntegrationSession(t, db, adminID)
	internalService := NewAuditedInternalProgramEventService(
		db,
		&capturingAsyncPublisher{},
		apitelemetry.NewDurableWriter(db),
		WithInternalProgramEventSpiceDB(spiceDB),
		WithInternalProgramEventCheckpoints(programEventIntegrationCheckpoints(db, spiceDB)),
		WithInternalProgramEventContentBlockStore(store),
		WithInternalProgramEventMediaHydrator(files),
	)
	initial, err := internalService.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId:   created.Msg.Id,
		Locale:    "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)

	published, err := eventService.PublishProgramEvent(ctx, connect.NewRequest(&managev1.PublishProgramEventRequest{
		Id: created.Msg.Id,
	}))
	require.NoError(t, err)
	require.NotNil(t, published.Msg.PublishedAt)
	originalPublishedAt := published.Msg.PublishedAt.AsTime()

	archived, err := eventService.ArchiveProgramEvent(ctx, connect.NewRequest(&managev1.ArchiveProgramEventRequest{
		Id: created.Msg.Id,
	}))
	require.NoError(t, err)
	require.Equal(t, managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED, archived.Msg.Status)
	authorID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, authorID, "Archived Program Event Author")
	grantIntegrationGlobalRole(t, spiceDB, authorID, policyv1.Role.Author())
	authorCtx := programEventIntegrationAdminCtx(authorID)
	viewed, err := eventService.GetProgramEvent(authorCtx, connect.NewRequest(&managev1.GetProgramEventRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED, viewed.Msg.Status)
	require.NotNil(t, viewed.Msg.Document)
	authorSessionID := insertProgramEventIntegrationSession(t, db, authorID)
	authorBootstrap, err := internalService.LoadProgramEventBlockDocument(context.Background(), connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId: created.Msg.Id, Principal: &intrav1.CollaborationPrincipal{SessionId: authorSessionID},
		Locale: "en",
	}))
	require.NoError(t, err)
	require.NotNil(t, authorBootstrap.Msg.Document)
	authorTitle := "Author must not persist archived Event metadata"
	_, err = internalService.UpdateProgramEventLocaleMetadata(context.Background(), connect.NewRequest(&intrav1.UpdateProgramEventLocaleMetadataRequest{
		EventId: created.Msg.Id, Title: &authorTitle, ExpectedRevision: authorBootstrap.Msg.DocumentRevision,
		Locale:               "en",
		ContributorMemberIds: []string{integrationMemberID(authorID)},
	}))
	require.Error(t, err)
	_, err = eventService.PublishProgramEvent(ctx, connect.NewRequest(&managev1.PublishProgramEventRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	_, err = internalService.LoadProgramEventBlockDocument(context.Background(), connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId: created.Msg.Id, Principal: &intrav1.CollaborationPrincipal{SessionId: authorSessionID},
		Locale: "en",
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, err = eventService.GetProgramEvent(authorCtx, connect.NewRequest(&managev1.GetProgramEventRequest{Id: created.Msg.Id}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = eventService.ArchiveProgramEvent(ctx, connect.NewRequest(&managev1.ArchiveProgramEventRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	authorTimezone := "Europe/Paris"
	_, err = eventService.UpdateProgramEvent(authorCtx, connect.NewRequest(&managev1.UpdateProgramEventRequest{
		Id: created.Msg.Id, Timezone: &authorTimezone,
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	memberID := integrationMemberID(adminID)
	blockID := integrationTestUUID()
	blockApplied, err := internalService.ApplyProgramEventBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyProgramEventBlockBatchRequest{
		EventId: created.Msg.Id,
		Locale:  "en",
		Batch: programEventParagraphMutationBatch(
			initial.Msg.Document,
			initial.Msg.DocumentRevision,
			blockID,
			"en",
			"late source frame",
			[]string{memberID},
			true,
		),
	}))
	require.NoError(t, err)
	require.True(t, blockApplied.Msg.Changed)
	lateTitle := "Late source metadata"
	metadataUpdated, err := internalService.UpdateProgramEventLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdateProgramEventLocaleMetadataRequest{
		EventId:              created.Msg.Id,
		Locale:               "en",
		Title:                &lateTitle,
		ExpectedRevision:     blockApplied.Msg.DocumentRevision,
		ContributorMemberIds: []string{memberID},
	}))
	require.NoError(t, err)
	require.True(t, metadataUpdated.Msg.Changed)
	var persistedRevision string
	require.NoError(t, db.Raw(`
		SELECT document.revision::text AS revision
		FROM program_event AS event
		JOIN content_document AS document ON document.id = event.content_document_id
		WHERE event.id = ?
	`, created.Msg.Id).Scan(&persistedRevision).Error)
	require.Equal(t, metadataUpdated.Msg.DocumentRevision, persistedRevision)
	var persistedBlockCount int64
	require.NoError(t, db.Table("content_block").Where("document_id = (SELECT content_document_id FROM program_event WHERE id = ?)", created.Msg.Id).Count(&persistedBlockCount).Error)
	require.EqualValues(t, 1, persistedBlockCount)

	updatedTimezone := "UTC"
	updated, err := eventService.UpdateProgramEvent(ctx, connect.NewRequest(&managev1.UpdateProgramEventRequest{
		Id:       created.Msg.Id,
		Timezone: &updatedTimezone,
	}))
	require.NoError(t, err)
	require.True(t, updated.Msg.Changed)
	republished, err := eventService.PublishProgramEvent(ctx, connect.NewRequest(&managev1.PublishProgramEventRequest{
		Id: created.Msg.Id,
	}))
	require.NoError(t, err)
	require.Equal(t, managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED, republished.Msg.Status)
	require.NotNil(t, republished.Msg.PublishedAt)
	require.True(t, originalPublishedAt.Equal(republished.Msg.PublishedAt.AsTime()))
}
