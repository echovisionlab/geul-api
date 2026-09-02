//go:build integration

package programevent

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

type programEventAuditRow struct {
	Action        string `gorm:"column:action"`
	TargetType    string `gorm:"column:target_type"`
	TargetID      string `gorm:"column:target_id"`
	ActorMemberID string `gorm:"column:actor_member_id"`
	ActorService  string `gorm:"column:actor_service"`
	RequestID     string `gorm:"column:request_id"`
	Attributes    []byte `gorm:"column:attributes"`
}

func TestProgramEventDomainAuditMutationVariantsIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := integrationTestUUID()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Program event audit admin")
	ctx := programEventAuditedMemberContext(t, identityID, memberID)
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	store := newProgramEventIntegrationContentBlockStore(t, spiceDB)
	files := newProgramEventIntegrationFileService(db, spiceDB)
	writer := apitelemetry.NewDurableWriter(db)
	typeService := NewAuditedProgramEventTypeService(db, writer, spiceDB)
	seriesService := NewAuditedProgramEventSeriesService(db, newProgramEventRuntime("https://cdn.example.test"), writer, spiceDB)
	eventService := NewAuditedProgramEventService(
		db,
		newProgramEventRuntime("https://cdn.example.test"),
		files,
		writer,
		spiceDB,
		newProgramEventCreditMemberSummaries(db, "https://cdn.example.test"),
		WithProgramEventContentBlockStore(store),
	)

	typeCreated, err := typeService.CreateProgramEventType(ctx, connect.NewRequest(&managev1.CreateProgramEventTypeRequest{Slug: "audit-type-" + integrationTestUUID(), Locale: "en", Name: "Audit type"}))
	require.NoError(t, err)
	typeID := typeCreated.Msg.Id
	sortOrder := int32(7)
	_, err = typeService.UpdateProgramEventType(ctx, connect.NewRequest(&managev1.UpdateProgramEventTypeRequest{Id: typeID, SortOrder: &sortOrder}))
	require.NoError(t, err)
	localizedTypeName := "감사 이벤트 타입"
	_, err = typeService.UpdateProgramEventType(ctx, connect.NewRequest(&managev1.UpdateProgramEventTypeRequest{Id: typeID, Locale: "ko", Name: &localizedTypeName}))
	require.NoError(t, err)

	seriesCreated, err := seriesService.CreateProgramEventSeries(ctx, connect.NewRequest(&managev1.CreateProgramEventSeriesRequest{Title: "Audit series", Slug: "audit-series-" + integrationTestUUID()}))
	require.NoError(t, err)
	seriesID := seriesCreated.Msg.Id
	seriesPosterOne := seedImageBindingUploadedFileFixtureForKind(t, db, "program-event-audit/series-poster-one.webp", "poster")
	seriesPosterTwo := seedImageBindingUploadedFileFixtureForKind(t, db, "program-event-audit/series-poster-two.webp", "poster")
	seriesTitle := "Audit series updated"
	publishedSeries := managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED
	_, err = seriesService.UpdateProgramEventSeries(ctx, connect.NewRequest(&managev1.UpdateProgramEventSeriesRequest{Id: seriesID, Title: &seriesTitle, Status: &publishedSeries, PosterFileId: &seriesPosterOne}))
	require.NoError(t, err)
	_, err = seriesService.UpdateProgramEventSeries(ctx, connect.NewRequest(&managev1.UpdateProgramEventSeriesRequest{Id: seriesID, PosterFileId: &seriesPosterTwo}))
	require.NoError(t, err)
	emptySeriesPoster := ""
	_, err = seriesService.UpdateProgramEventSeries(ctx, connect.NewRequest(&managev1.UpdateProgramEventSeriesRequest{Id: seriesID, PosterFileId: &emptySeriesPoster}))
	require.NoError(t, err)

	seriesOrder := int32(0)
	eventCreated, err := eventService.CreateProgramEvent(ctx, connect.NewRequest(&managev1.CreateProgramEventRequest{
		Title: "Audited event", Slug: "audit-event-" + integrationTestUUID(), SourceLocale: "en", TypeId: typeID, SeriesId: &seriesID, SeriesOrder: &seriesOrder,
		StartsAt: timestamppb.New(time.Now().UTC().Add(time.Hour)), Timezone: "UTC", LocationMode: managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE,
	}))
	require.NoError(t, err)
	eventID := eventCreated.Msg.Id
	internalEventService := NewAuditedInternalProgramEventService(
		db,
		&capturingAsyncPublisher{},
		apitelemetry.NewDurableWriter(db),
		WithInternalProgramEventSpiceDB(spiceDB),
		WithInternalProgramEventCheckpoints(programEventIntegrationCheckpoints(db, spiceDB)),
		WithInternalProgramEventContentBlockStore(store),
		WithInternalProgramEventMediaHydrator(files),
	)
	artistID, labelID, clientID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	artistSlug, labelSlug := "event-audit-artist-"+integrationTestUUID(), "event-audit-label-"+integrationTestUUID()
	artistDocumentID := seedServiceIntegrationContentDocument(t, db, creativeContentProfile)
	require.NoError(t, db.Create(&model.Artist{ID: artistID, ContentDocumentID: &artistDocumentID, Slug: &artistSlug, Status: "ARTIST_STATUS_DRAFT", CreatedAt: time.Now().UTC()}).Error)
	labelDocumentID := seedServiceIntegrationContentDocument(t, db, creativeContentProfile)
	require.NoError(t, db.Create(&model.Label{ID: labelID, ContentDocumentID: &labelDocumentID, Slug: &labelSlug, Status: "LABEL_STATUS_DRAFT", CreatedAt: time.Now().UTC()}).Error)
	require.NoError(t, db.Create(&model.Client{ID: clientID, Name: "Event audit client", CreatedAt: time.Now().UTC()}).Error)
	relationRequest := &managev1.UpdateProgramEventRequest{Id: eventID, ReplaceArtists: true, Artists: []*managev1.ProgramEventArtist{{ArtistId: artistID}}, ReplaceLabels: true, Labels: []*managev1.ProgramEventLabel{{LabelId: labelID}}, ReplaceClients: true, Clients: []*managev1.ProgramEventClient{{ClientId: clientID}}}
	_, err = eventService.UpdateProgramEvent(ctx, connect.NewRequest(relationRequest))
	require.NoError(t, err)
	relationNoopCount := programEventAuditCount(t, db, eventID)
	_, err = eventService.UpdateProgramEvent(ctx, connect.NewRequest(relationRequest))
	require.NoError(t, err)
	require.Equal(t, relationNoopCount, programEventAuditCount(t, db, eventID), "same relations are a no-op")
	contributors := []string{memberID}
	sessionID := insertProgramEventIntegrationSession(t, db, identityID)
	loaded, err := internalEventService.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId:   eventID,
		Locale:    "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)

	sourceTitle := "Audited event source revision"
	metadataUpdated, err := internalEventService.UpdateProgramEventLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdateProgramEventLocaleMetadataRequest{
		EventId:              eventID,
		Locale:               "en",
		Title:                &sourceTitle,
		ExpectedRevision:     loaded.Msg.DocumentRevision,
		ContributorMemberIds: contributors,
	}))
	require.NoError(t, err)
	require.True(t, metadataUpdated.Msg.Changed)
	blockID := integrationTestUUID()
	_, err = internalEventService.ApplyProgramEventBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyProgramEventBlockBatchRequest{
		EventId: eventID,
		Locale:  "en",
		Batch: programEventParagraphMutationBatch(
			loaded.Msg.Document,
			metadataUpdated.Msg.DocumentRevision,
			blockID,
			"en",
			"program event source",
			contributors,
			true,
		),
	}))
	require.NoError(t, err)
	newSlug := "audit-event-updated-" + integrationTestUUID()
	_, err = eventService.UpdateProgramEvent(ctx, connect.NewRequest(&managev1.UpdateProgramEventRequest{Id: eventID, Slug: &newSlug}))
	require.NoError(t, err)
	failingMetadataService := NewAuditedProgramEventService(
		db, newProgramEventRuntime(""), referenceNoopFileDeleter{}, failingDomainAuditAppender{}, spiceDB,
		newProgramEventCreditMemberSummaries(db, ""),
	)
	failingSlug := "must-roll-back-event-" + integrationTestUUID()
	_, err = failingMetadataService.UpdateProgramEvent(ctx, connect.NewRequest(&managev1.UpdateProgramEventRequest{Id: eventID, Slug: &failingSlug}))
	require.Error(t, err)
	var persistedSlug string
	require.NoError(t, db.Raw(`SELECT slug FROM program_event WHERE id = ?`, eventID).Scan(&persistedSlug).Error)
	require.Equal(t, newSlug, persistedSlug)
	posterOne := seedImageBindingUploadedFileFixtureForKind(t, db, "program-event-audit/poster-one.webp", "poster")
	posterTwo := seedImageBindingUploadedFileFixtureForKind(t, db, "program-event-audit/poster-two.webp", "poster")
	_, err = eventService.AddProgramEventMedia(ctx, connect.NewRequest(&managev1.AddProgramEventMediaRequest{EventId: eventID, FileId: posterOne, Role: "poster"}))
	require.NoError(t, err)
	_, err = eventService.AddProgramEventMedia(ctx, connect.NewRequest(&managev1.AddProgramEventMediaRequest{EventId: eventID, FileId: posterTwo, Role: "poster", MakePrimary: true}))
	require.NoError(t, err)
	empty := ""
	_, err = eventService.UpdateProgramEvent(ctx, connect.NewRequest(&managev1.UpdateProgramEventRequest{Id: eventID, PosterFileId: &empty}))
	require.NoError(t, err)
	galleryOne := seedImageBindingUploadedFileFixtureForKind(t, db, "program-event-audit/gallery-one.webp", "poster")
	galleryTwo := seedImageBindingUploadedFileFixtureForKind(t, db, "program-event-audit/gallery-two.webp", "poster")
	_, err = eventService.AddProgramEventMedia(ctx, connect.NewRequest(&managev1.AddProgramEventMediaRequest{EventId: eventID, FileId: galleryOne, Role: "gallery"}))
	require.NoError(t, err)
	_, err = eventService.AddProgramEventMedia(ctx, connect.NewRequest(&managev1.AddProgramEventMediaRequest{EventId: eventID, FileId: galleryTwo, Role: "gallery"}))
	require.NoError(t, err)
	media, err := eventService.loadProgramEvent(ctx, eventID)
	require.NoError(t, err)
	var galleryOneID, galleryTwoID string
	for _, item := range media.Media {
		if item.FileId == galleryOne {
			galleryOneID = item.Id
		}
		if item.FileId == galleryTwo {
			galleryTwoID = item.Id
		}
	}
	require.NotEmpty(t, galleryOneID)
	require.NotEmpty(t, galleryTwoID)
	caption := "updated caption"
	_, err = eventService.AddProgramEventMedia(ctx, connect.NewRequest(&managev1.AddProgramEventMediaRequest{EventId: eventID, FileId: galleryOne, Role: "gallery", Caption: &caption}))
	require.NoError(t, err)
	_, err = eventService.ReorderProgramEventMedia(ctx, connect.NewRequest(&managev1.ReorderProgramEventMediaRequest{EventId: eventID, Role: "gallery", MediaIds: []string{galleryTwoID, galleryOneID}}))
	require.NoError(t, err)
	_, err = eventService.DeleteProgramEventMedia(ctx, connect.NewRequest(&managev1.DeleteProgramEventMediaRequest{EventId: eventID, MediaId: galleryOneID}))
	require.NoError(t, err)

	credit, err := eventService.AddProgramEventCredit(ctx, connect.NewRequest(&managev1.AddProgramEventCreditRequest{EventId: eventID, DisplayName: stringPtr("Credit")}))
	require.NoError(t, err)
	creditRole := "host"
	_, err = eventService.UpdateProgramEventCredit(ctx, connect.NewRequest(&managev1.UpdateProgramEventCreditRequest{EventId: eventID, CreditId: credit.Msg.Id, CreditRole: &creditRole}))
	require.NoError(t, err)
	directCreditNoopCount := programEventAuditCount(t, db, eventID)
	_, err = eventService.UpdateProgramEventCredit(ctx, connect.NewRequest(&managev1.UpdateProgramEventCreditRequest{EventId: eventID, CreditId: credit.Msg.Id, CreditRole: &creditRole}))
	require.NoError(t, err)
	_, err = eventService.ReorderProgramEventCredits(ctx, connect.NewRequest(&managev1.ReorderProgramEventCreditsRequest{EventId: eventID, CreditIds: []string{credit.Msg.Id}}))
	require.NoError(t, err)
	require.Equal(t, directCreditNoopCount, programEventAuditCount(t, db, eventID), "same direct credit update/order is a no-op")
	_, err = eventService.DeleteProgramEventCredit(ctx, connect.NewRequest(&managev1.DeleteProgramEventCreditRequest{EventId: eventID, CreditId: credit.Msg.Id}))
	require.NoError(t, err)
	bulkNameOne, bulkNameTwo := "Bulk credit one", "Bulk credit two"
	_, err = eventService.UpdateProgramEvent(ctx, connect.NewRequest(&managev1.UpdateProgramEventRequest{Id: eventID, ReplaceCredits: true, Credits: []*managev1.ProgramEventCredit{{DisplayName: &bulkNameOne, SortOrder: 1}, {DisplayName: &bulkNameTwo, SortOrder: 0}}}))
	require.NoError(t, err)
	bulkEvent, err := eventService.loadProgramEvent(ctx, eventID)
	require.NoError(t, err)
	require.Len(t, bulkEvent.Credits, 2)
	bulkFirst, bulkSecond := bulkEvent.Credits[0], bulkEvent.Credits[1]
	bulkRole := "director"
	_, err = eventService.UpdateProgramEvent(ctx, connect.NewRequest(&managev1.UpdateProgramEventRequest{Id: eventID, ReplaceCredits: true, Credits: []*managev1.ProgramEventCredit{{Id: bulkFirst.Id, DisplayName: bulkFirst.DisplayName, CreditRole: &bulkRole, SortOrder: 0}}}))
	require.NoError(t, err)
	bulkAuditCount := programEventAuditCount(t, db, eventID)
	_, err = eventService.UpdateProgramEvent(ctx, connect.NewRequest(&managev1.UpdateProgramEventRequest{Id: eventID, ReplaceCredits: true, Credits: []*managev1.ProgramEventCredit{{Id: bulkFirst.Id, DisplayName: bulkFirst.DisplayName, CreditRole: &bulkRole, SortOrder: 0}}}))
	require.NoError(t, err)
	require.Equal(t, bulkAuditCount, programEventAuditCount(t, db, eventID), "same bulk credit shape is a no-op")
	_, err = eventService.UpdateProgramEvent(ctx, connect.NewRequest(&managev1.UpdateProgramEventRequest{Id: eventID, ReplaceCredits: true, Credits: []*managev1.ProgramEventCredit{{Id: integrationTestUUID(), DisplayName: &bulkNameOne}}}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = eventService.UpdateProgramEvent(ctx, connect.NewRequest(&managev1.UpdateProgramEventRequest{Id: eventID, ReplaceCredits: true, Credits: []*managev1.ProgramEventCredit{{Id: bulkFirst.Id, DisplayName: bulkFirst.DisplayName}, {Id: bulkFirst.Id, DisplayName: bulkFirst.DisplayName}}}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = eventService.PublishProgramEvent(ctx, connect.NewRequest(&managev1.PublishProgramEventRequest{Id: eventID}))
	require.NoError(t, err)
	_, err = eventService.ArchiveProgramEvent(ctx, connect.NewRequest(&managev1.ArchiveProgramEventRequest{Id: eventID}))
	require.NoError(t, err)
	_, err = eventService.PublishProgramEvent(ctx, connect.NewRequest(&managev1.PublishProgramEventRequest{Id: eventID}))
	require.NoError(t, err)

	eventAuditBeforeSeriesDelete := programEventAuditCount(t, db, eventID)
	_, err = seriesService.DeleteProgramEventSeries(ctx, connect.NewRequest(&managev1.DeleteProgramEventSeriesRequest{Id: seriesID}))
	require.NoError(t, err)
	require.Equal(t, eventAuditBeforeSeriesDelete+1, programEventAuditCount(t, db, eventID), "series deletion must record the Event-owned relation reset")
	var detached struct {
		SeriesID *string `gorm:"column:series_id"`
	}
	require.NoError(t, db.Raw(`SELECT series_id::text FROM program_event WHERE id = ?`, eventID).Scan(&detached).Error)
	require.Nil(t, detached.SeriesID)
	_, err = eventService.DeleteProgramEvent(ctx, connect.NewRequest(&managev1.DeleteProgramEventRequest{Id: eventID}))
	require.NoError(t, err)
	_, err = typeService.DeleteProgramEventType(ctx, connect.NewRequest(&managev1.DeleteProgramEventTypeRequest{Id: typeID}))
	require.NoError(t, err)

	rows := programEventAuditRows(t, db)
	for _, row := range rows {
		require.Equal(t, memberID, row.ActorMemberID)
		require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), row.RequestID)
	}
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventTypeCreated), "program_event_type", typeID, `{}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventTypeUpdated), "program_event_type", typeID, `{"changed_fields":["sort_order"]}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventTypeDeleted), "program_event_type", typeID, `{}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventSeriesCreated), "program_event_series", seriesID, `{}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventSeriesUpdated), "program_event_series", seriesID, `{"changed_fields":["title"]}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventSeriesUpdated), "program_event_series", seriesID, `{"changed_fields":["status"],"previous_state":"draft","new_state":"published"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventSeriesUpdated), "program_event_series", seriesID, `{"changed_fields":["poster"],"collection_operation":"added","file_id":"`+seriesPosterOne+`"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventSeriesUpdated), "program_event_series", seriesID, `{"changed_fields":["poster"],"collection_operation":"added","file_id":"`+seriesPosterTwo+`"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventSeriesUpdated), "program_event_series", seriesID, `{"changed_fields":["poster"],"collection_operation":"removed","file_id":"`+seriesPosterTwo+`"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventSeriesDeleted), "program_event_series", seriesID, `{}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventCreated), "program_event", eventID, `{}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["artists","clients","labels"]}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["locale_content"],"locale":"en","item_operation":"updated"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["slug"]}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["series"]}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["poster"],"collection_operation":"added","file_id":"`+posterOne+`"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["poster"],"collection_operation":"added","file_id":"`+posterTwo+`"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["poster"],"collection_operation":"removed","file_id":"`+posterTwo+`"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["media"],"item_operation":"created","item_id":"`+galleryOneID+`"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["media"],"item_operation":"updated","item_id":"`+galleryOneID+`"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["media"],"item_ids":["`+galleryTwoID+`","`+galleryOneID+`"]}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["media"],"item_operation":"deleted","item_id":"`+galleryOneID+`"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["credits"],"item_operation":"created","item_id":"`+credit.Msg.Id+`"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["credits"],"item_operation":"updated","item_id":"`+credit.Msg.Id+`"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["credits"],"item_operation":"deleted","item_id":"`+credit.Msg.Id+`"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["credits"],"item_operation":"created","item_id":"`+bulkFirst.Id+`"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["credits"],"item_operation":"created","item_id":"`+bulkSecond.Id+`"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["credits"],"item_operation":"updated","item_id":"`+bulkFirst.Id+`"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["credits"],"item_operation":"deleted","item_id":"`+bulkSecond.Id+`"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["status"],"previous_state":"draft","new_state":"published"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["status"],"previous_state":"published","new_state":"archived"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["status"],"previous_state":"archived","new_state":"published"}`)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventDeleted), "program_event", eventID, `{}`)
}

func programEventAuditCount(t *testing.T, db *gorm.DB, eventID string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("domain_audit").Where("target_type = 'program_event' AND target_id = ?", eventID).Count(&count).Error)
	return count
}

func programEventAuditRows(t *testing.T, db *gorm.DB) []programEventAuditRow {
	t.Helper()
	var rows []programEventAuditRow
	require.NoError(t, db.Raw(`SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id, actor_service, request_id::text AS request_id, attributes FROM public.domain_audit ORDER BY occurred_at, audit_id`).Scan(&rows).Error)
	return rows
}

func requireProgramEventAudit(t *testing.T, rows []programEventAuditRow, action, targetType, targetID, attributes string) {
	t.Helper()
	for _, row := range rows {
		if row.Action == action && row.TargetType == targetType && row.TargetID == targetID {
			var actual, expected any
			require.NoError(t, json.Unmarshal(row.Attributes, &actual))
			require.NoError(t, json.Unmarshal([]byte(attributes), &expected))
			if reflect.DeepEqual(expected, actual) {
				return
			}
		}
	}
	require.Failf(t, "missing audit", "%s %s %s %s", action, targetType, targetID, attributes)
}
