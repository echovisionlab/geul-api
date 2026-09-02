//go:build integration

package programevent

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestInternalProgramEventServiceBlockDocumentLifecycleIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Program Event Internal Admin")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	ctx := programEventAuditedMemberContext(t, adminID, integrationMemberID(adminID))
	suffix := strings.ReplaceAll(integrationTestUUID(), "-", "")
	store := newProgramEventIntegrationContentBlockStore(t, spiceDB)
	files := newProgramEventIntegrationFileService(db, spiceDB)

	typeSvc := NewProgramEventTypeService(db, spiceDB)
	typeResp, err := typeSvc.CreateProgramEventType(ctx, connect.NewRequest(&managev1.CreateProgramEventTypeRequest{
		Slug:              "internal-program-event-type-" + suffix,
		Locale:            "en",
		Name:              "Internal Live " + suffix,
		RequiresPlace:     ptrBool(false),
		RequiresStreamUrl: ptrBool(false),
	}))
	require.NoError(t, err)

	eventSvc := NewProgramEventService(db, newProgramEventRuntime("https://cdn.example.com"), spiceDB, newProgramEventCreditMemberSummaries(db, "https://cdn.example.com"), files)
	eventSvc.contentBlocks = store
	startsAt := time.Now().UTC().Add(3 * time.Hour)
	created, err := eventSvc.CreateProgramEvent(ctx, connect.NewRequest(&managev1.CreateProgramEventRequest{
		Title:        "Internal Program Event " + suffix,
		Slug:         "internal-program-event-" + suffix,
		SourceLocale: "en",
		TypeId:       typeResp.Msg.Id,
		StartsAt:     timestamppb.New(startsAt),
		Timezone:     "Asia/Seoul",
		Summary:      ptrString("Initial source summary"),
		LocationMode: managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE,
	}))
	require.NoError(t, err)
	require.NotEmpty(t, created.Msg.Id)

	sessionID := insertProgramEventIntegrationSession(t, db, adminID)
	publisher := &capturingAsyncPublisher{}
	missingHydrator := NewInternalProgramEventService(
		db,
		publisher,
		WithInternalProgramEventSpiceDB(spiceDB),
		WithInternalProgramEventCheckpoints(programEventIntegrationCheckpoints(db, spiceDB)),
		WithInternalProgramEventContentBlockStore(store),
	)
	_, err = missingHydrator.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId:   created.Msg.Id,
		Locale:    "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	internalSvc := NewAuditedInternalProgramEventService(
		db,
		publisher,
		apitelemetry.NewDurableWriter(db),
		WithInternalProgramEventSpiceDB(spiceDB),
		WithInternalProgramEventCheckpoints(programEventIntegrationCheckpoints(db, spiceDB)),
		WithInternalProgramEventContentBlockStore(store),
		WithInternalProgramEventMediaHydrator(files),
	)
	loaded, err := internalSvc.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId:   created.Msg.Id,
		Locale:    "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)
	require.Equal(t, contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT, loaded.Msg.Document.Profile)
	require.NotEmpty(t, loaded.Msg.Document.BlockCatalogFingerprint)
	require.Empty(t, loaded.Msg.Document.Base.Nodes)
	require.NotEmpty(t, loaded.Msg.DocumentRevision)
	require.Equal(t, "en", loaded.Msg.Locale)
	require.True(t, loaded.Msg.LocaleExists)

	memberID := integrationMemberID(adminID)
	systemCtx := programEventAuditedSystemContext(t)
	blockID := integrationTestUUID()
	sourceApplied, err := internalSvc.ApplyProgramEventBlockBatch(systemCtx, connect.NewRequest(&intrav1.ApplyProgramEventBlockBatchRequest{
		EventId: created.Msg.Id,
		Locale:  "en",
		Batch: programEventParagraphMutationBatch(
			loaded.Msg.Document,
			loaded.Msg.DocumentRevision,
			blockID,
			"en",
			"Updated source body",
			[]string{memberID},
			true,
		),
	}))
	require.NoError(t, err)
	require.True(t, sourceApplied.Msg.Changed)
	require.NotEqual(t, loaded.Msg.DocumentRevision, sourceApplied.Msg.DocumentRevision)
	require.True(t, sourceApplied.Msg.SourceChanged)
	sourceLoaded, err := internalSvc.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId:   created.Msg.Id,
		Locale:    "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)
	require.Len(t, sourceLoaded.Msg.Document.Base.Nodes, 1)
	require.Equal(t, "Updated source body", programEventParagraphText(t, sourceLoaded.Msg.Document, "en", blockID))

	sourceSummary := "Updated source summary"
	metadataUpdated, err := internalSvc.UpdateProgramEventLocaleMetadata(systemCtx, connect.NewRequest(&intrav1.UpdateProgramEventLocaleMetadataRequest{
		EventId:              created.Msg.Id,
		Locale:               "en",
		Summary:              &intrav1.NullableStringMutation{Operation: &intrav1.NullableStringMutation_Set{Set: sourceSummary}},
		ExpectedRevision:     sourceApplied.Msg.DocumentRevision,
		ContributorMemberIds: []string{memberID},
	}))
	require.NoError(t, err)
	require.True(t, metadataUpdated.Msg.Changed)
	require.NotEqual(t, sourceApplied.Msg.DocumentRevision, metadataUpdated.Msg.DocumentRevision)
	require.True(t, metadataUpdated.Msg.SourceChanged)

	targetRevision := seedProgramEventTargetLocaleForIntegration(
		t, db, store, created.Msg.Id, "ko", metadataUpdated.Msg.DocumentRevision, memberID,
	)
	targetApplied, err := internalSvc.ApplyProgramEventBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyProgramEventBlockBatchRequest{
		EventId:                created.Msg.Id,
		Locale:                 "ko",
		ExpectedTargetRevision: &targetRevision,
		Batch: programEventParagraphMutationBatch(
			sourceLoaded.Msg.Document,
			metadataUpdated.Msg.DocumentRevision,
			blockID,
			"ko",
			"번역 본문",
			[]string{memberID},
			false,
		),
	}))
	require.NoError(t, err)
	require.True(t, targetApplied.Msg.Changed)
	require.Equal(t, metadataUpdated.Msg.DocumentRevision, targetApplied.Msg.DocumentRevision)
	require.NotNil(t, targetApplied.Msg.TargetRevision)
	targetLoaded, err := internalSvc.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId:   created.Msg.Id,
		Locale:    "ko",
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)
	require.Equal(t, "번역 본문", programEventParagraphText(t, targetLoaded.Msg.Document, "ko", blockID))
	current, err := internalSvc.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId:   created.Msg.Id,
		Locale:    "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)
	require.Equal(t, targetApplied.Msg.DocumentRevision, current.Msg.DocumentRevision)
	require.Equal(t, "Updated source body", programEventParagraphText(t, current.Msg.Document, "en", blockID))

	staleInterchangeSummary := "must not win"
	staleInterchangeCandidate := &translation.Candidate{
		Summary:                   &staleInterchangeSummary,
		ContentBlockLocaleOverlay: programEventParagraphOverlay(blockID, "ko", "must not win"),
	}
	staleInterchangeJob := &model.TranslationJob{
		EntityType: EntityType, EntityID: created.Msg.Id,
		SourceLocale: "en", TargetLocale: "ko", RequestedByMemberID: memberID,
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		return applyProgramEventTypedTranslationCandidateWithDB(
			ctx, tx, store, staleInterchangeJob, staleInterchangeCandidate,
			translation.EntryWrite{Now: time.Now().UTC()}, &targetRevision, false,
			apitelemetry.NewDurableWriter(db),
		)
	})
	var targetConflict *translation.TargetRevisionConflict
	require.ErrorAs(t, err, &targetConflict)
	afterStaleInterchange, err := internalSvc.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId: created.Msg.Id, Locale: "ko",
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)
	require.Equal(t, targetApplied.Msg.GetTargetRevision(), afterStaleInterchange.Msg.GetTargetRevision())

	_, err = internalSvc.ApplyProgramEventBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyProgramEventBlockBatchRequest{
		EventId: created.Msg.Id,
		Locale:  "en",
		Batch: programEventParagraphMutationBatch(
			current.Msg.Document,
			loaded.Msg.DocumentRevision,
			blockID,
			"en",
			"stale source body",
			[]string{memberID},
			false,
		),
	}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	afterStale, err := internalSvc.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId:   created.Msg.Id,
		Locale:    "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)
	require.Equal(t, current.Msg.DocumentRevision, afterStale.Msg.DocumentRevision)
	var persisted struct {
		Revision string `gorm:"column:revision"`
	}
	require.NoError(t, db.Raw(`
		SELECT document.revision::text AS revision
		FROM program_event AS event
		JOIN content_document AS document ON document.id = event.content_document_id
		WHERE event.id = ?
	`, created.Msg.Id).Scan(&persisted).Error)
	require.Equal(t, current.Msg.DocumentRevision, persisted.Revision)
	var blockCount int64
	require.NoError(t, db.Table("content_block").Where("document_id = (SELECT content_document_id FROM program_event WHERE id = ?)", created.Msg.Id).Count(&blockCount).Error)
	require.EqualValues(t, 1, blockCount)
	var localeCount int64
	require.NoError(t, db.Table("content_block_locale").Where("block_id = ?", blockID).Count(&localeCount).Error)
	require.EqualValues(t, 2, localeCount)

	_, err = internalSvc.ApplyProgramEventBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyProgramEventBlockBatchRequest{
		EventId: created.Msg.Id,
		Locale:  "en",
		Batch: programEventParagraphMutationBatch(
			current.Msg.Document,
			current.Msg.DocumentRevision,
			blockID,
			"en",
			"duplicate contributor must roll back",
			[]string{memberID, memberID},
			false,
		),
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = internalSvc.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId:   integrationTestUUID(),
		Locale:    "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	providerSummary := "Provider target summary"
	providerRequesterID := integrationTestUUID()
	require.NoError(t, db.Exec(`
		INSERT INTO public.member (id, nickname, onboarded, primary_email, available_emails)
		VALUES (?::uuid, 'program-event-provider-requester', true, 'program-event-provider@example.test',
		        ARRAY['program-event-provider@example.test']::text[])
	`, providerRequesterID).Error)
	providerCandidate := &translation.Candidate{
		Summary:                 &providerSummary,
		ContentDocumentRevision: current.Msg.DocumentRevision,
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{
			Locale: "ko",
		},
		ContentBlockLocaleDeletes: []string{blockID},
	}
	providerJob := &model.TranslationJob{
		EntityType: EntityType, EntityID: created.Msg.Id,
		SourceLocale: "en", TargetLocale: "ko", RequestedByMemberID: providerRequesterID,
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return ApplyTypedTranslationCandidateWithDB(
			ctx, tx, store, providerJob, providerCandidate,
			translation.EntryWrite{Now: time.Now().UTC()}, apitelemetry.NewDurableWriter(db),
		)
	}))
	afterProvider, err := LoadTranslationSourceDocument(ctx, db, store, created.Msg.Id)
	require.NoError(t, err)
	require.Equal(t, current.Msg.DocumentRevision, afterProvider.ContentDocumentRevision)
	require.NoError(t, db.Table("content_block_locale").Where("block_id = ? AND locale = 'ko'", blockID).Count(&localeCount).Error)
	require.Zero(t, localeCount)
	var providerAuditActor string
	require.NoError(t, db.Raw(`
		SELECT actor_member_id::text
		FROM domain_audit
		WHERE action = ? AND target_type = 'program_event' AND target_id = ?
		  AND attributes @> '{"changed_fields":["locale_content"],"locale":"ko"}'::jsonb
		ORDER BY occurred_at DESC, audit_id DESC
		LIMIT 1`, sharedtelemetry.AuditProgramEventUpdated, created.Msg.Id).
		Scan(&providerAuditActor).Error)
	require.Equal(t, providerRequesterID, providerAuditActor)
}

func TestProgramEventSourceEditAndSwitchPreserveTargetAndRequestedJobIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Program Event translation invariant admin")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	ctx := programEventIntegrationAdminCtx(adminID)
	suffix := strings.ReplaceAll(integrationTestUUID(), "-", "")
	store := newProgramEventIntegrationContentBlockStore(t, spiceDB)
	files := newProgramEventIntegrationFileService(db, spiceDB)

	typeService := NewProgramEventTypeService(db, spiceDB)
	typeResponse, err := typeService.CreateProgramEventType(ctx, connect.NewRequest(&managev1.CreateProgramEventTypeRequest{
		Slug:              "program-event-translation-invariant-type-" + suffix,
		Locale:            "en",
		Name:              "Translation invariant " + suffix,
		RequiresPlace:     ptrBool(false),
		RequiresStreamUrl: ptrBool(false),
	}))
	require.NoError(t, err)

	eventService := NewProgramEventService(db, newProgramEventRuntime("https://cdn.example.com"), spiceDB, newProgramEventCreditMemberSummaries(db, "https://cdn.example.com"), files)
	eventService.contentBlocks = store
	created, err := eventService.CreateProgramEvent(ctx, connect.NewRequest(&managev1.CreateProgramEventRequest{
		Title:        "Program Event translation invariant " + suffix,
		Slug:         "program-event-translation-invariant-" + suffix,
		SourceLocale: "en",
		TypeId:       typeResponse.Msg.Id,
		StartsAt:     timestamppb.New(time.Now().UTC().Add(3 * time.Hour)),
		Timezone:     "Asia/Seoul",
		Summary:      ptrString("Source summary before edit"),
		LocationMode: managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE,
	}))
	require.NoError(t, err)

	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO program_event_translation (entity_id, locale, summary, created_at, updated_at)
		VALUES (?, 'ko', NULL, ?, ?)
	`, created.Msg.Id, now, now).Error)
	jobID := integrationTestUUID()
	require.NoError(t, db.Create(&model.TranslationJob{
		ID:                    jobID,
		EntityType:            EntityType,
		EntityID:              created.Msg.Id,
		TargetLocale:          "ko",
		SourceLocale:          "en",
		RequestedByMemberID:   integrationMemberID(adminID),
		RequestArtifactDigest: strings.Repeat("a", 64),
		OperationID:           integrationTestUUID(),
		Status:                "queued",
		RequestedAt:           now,
		RequestXLIFF:          []byte("request"),
		RequestManifest:       []byte("{}"),
		CreatedAt:             now,
		UpdatedAt:             now,
	}).Error)

	loadTarget := func() model.ProgramEventTranslation {
		var target model.ProgramEventTranslation
		require.NoError(t, db.Where("entity_id = ? AND locale = 'ko'", created.Msg.Id).Take(&target).Error)
		return target
	}
	loadJob := func() model.TranslationJob {
		var job model.TranslationJob
		require.NoError(t, db.First(&job, "id = ?", jobID).Error)
		return job
	}
	targetBefore := loadTarget()
	jobBefore := loadJob()

	internalService := NewInternalProgramEventService(
		db,
		&capturingAsyncPublisher{},
		WithInternalProgramEventSpiceDB(spiceDB),
		WithInternalProgramEventCheckpoints(programEventIntegrationCheckpoints(db, spiceDB)),
		WithInternalProgramEventContentBlockStore(store),
		WithInternalProgramEventMediaHydrator(files),
	)
	memberID := integrationMemberID(adminID)
	updatedSummary := "Source summary after edit"
	metadataUpdated, err := internalService.UpdateProgramEventLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdateProgramEventLocaleMetadataRequest{
		EventId:              created.Msg.Id,
		Locale:               "en",
		Summary:              &intrav1.NullableStringMutation{Operation: &intrav1.NullableStringMutation_Set{Set: updatedSummary}},
		ExpectedRevision:     created.Msg.DocumentRevision,
		ContributorMemberIds: []string{memberID},
	}))
	require.NoError(t, err)
	require.True(t, metadataUpdated.Msg.Changed)
	require.Equal(t, targetBefore, loadTarget(), "source edits must not rewrite an existing target")
	require.Equal(t, jobBefore, loadJob(), "source edits must not cancel or supersede a requested job")

	var sourceLocale string
	require.NoError(t, db.Table("program_event").Where("id = ?", created.Msg.Id).Pluck("source_locale", &sourceLocale).Error)
	require.Equal(t, "en", sourceLocale, "Program Event source locale remains root-owned")
	require.Equal(t, targetBefore, loadTarget(), "source edits must not rewrite an existing target")
	require.Equal(t, jobBefore, loadJob(), "source edits must not cancel or supersede a requested job")
}

func TestProgramEventFileBlockKeepsBaseIdentityAndLocalizedCaptionIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Program Event Media Name Admin")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	ctx := programEventAuditedMemberContext(t, adminID, integrationMemberID(adminID))
	suffix := strings.ReplaceAll(integrationTestUUID(), "-", "")
	store := newProgramEventIntegrationContentBlockStore(t, spiceDB)
	files := newProgramEventIntegrationFileService(db, spiceDB)

	typeSvc := NewProgramEventTypeService(db, spiceDB)
	typeResp, err := typeSvc.CreateProgramEventType(ctx, connect.NewRequest(&managev1.CreateProgramEventTypeRequest{
		Slug:              "program-event-media-name-" + suffix,
		Locale:            "en",
		Name:              "Media name " + suffix,
		RequiresPlace:     ptrBool(false),
		RequiresStreamUrl: ptrBool(false),
	}))
	require.NoError(t, err)

	eventSvc := NewProgramEventService(db, newProgramEventRuntime("https://cdn.example.com"), spiceDB, newProgramEventCreditMemberSummaries(db, "https://cdn.example.com"), files)
	eventSvc.contentBlocks = store
	created, err := eventSvc.CreateProgramEvent(ctx, connect.NewRequest(&managev1.CreateProgramEventRequest{
		Title:        "Program event media name " + suffix,
		Slug:         "program-event-media-name-" + suffix,
		SourceLocale: "en",
		TypeId:       typeResp.Msg.Id,
		StartsAt:     timestamppb.New(time.Now().UTC().Add(3 * time.Hour)),
		Timezone:     "Asia/Seoul",
		LocationMode: managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE,
	}))
	require.NoError(t, err)

	sessionID := insertProgramEventIntegrationSession(t, db, adminID)
	internalSvc := NewAuditedInternalProgramEventService(
		db,
		&capturingAsyncPublisher{},
		apitelemetry.NewDurableWriter(db),
		WithInternalProgramEventSpiceDB(spiceDB),
		WithInternalProgramEventCheckpoints(programEventIntegrationCheckpoints(db, spiceDB)),
		WithInternalProgramEventContentBlockStore(store),
		WithInternalProgramEventMediaHydrator(files),
	)
	loaded, err := internalSvc.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId:   created.Msg.Id,
		Locale:    "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)

	fileID := seedImageBindingUploadedFileFixture(t, db, "program-event-block/file.webp")
	blockID := integrationTestUUID()
	memberID := integrationMemberID(adminID)
	sourceApplied, err := internalSvc.ApplyProgramEventBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyProgramEventBlockBatchRequest{
		EventId: created.Msg.Id,
		Locale:  "en",
		Batch: programEventFileMutationBatch(
			loaded.Msg.Document,
			loaded.Msg.DocumentRevision,
			blockID,
			0,
			fileID,
			"source-v1.webp",
			"en",
			"Source alt",
			"Source caption",
			[]string{memberID},
			true,
		),
	}))
	require.NoError(t, err)
	require.True(t, sourceApplied.Msg.Changed)
	sourceLoaded, err := internalSvc.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId:   created.Msg.Id,
		Locale:    "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)
	requireProgramEventHydratedBlockMedia(t, sourceLoaded.Msg.BlockMedia, fileID, blockID)

	duplicateBlockID := integrationTestUUID()
	duplicated, err := internalSvc.ApplyProgramEventBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyProgramEventBlockBatchRequest{
		EventId: created.Msg.Id,
		Locale:  "en",
		Batch: programEventFileMutationBatch(
			sourceLoaded.Msg.Document,
			sourceApplied.Msg.DocumentRevision,
			duplicateBlockID,
			1,
			fileID,
			"duplicate.webp",
			"en",
			"Duplicate alt",
			"Duplicate caption",
			[]string{memberID},
			true,
		),
	}))
	require.NoError(t, err)
	require.True(t, duplicated.Msg.Changed)
	duplicatedLoaded, err := internalSvc.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId:   created.Msg.Id,
		Locale:    "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)
	requireProgramEventHydratedBlockMedia(t, duplicatedLoaded.Msg.BlockMedia, fileID, blockID, duplicateBlockID)

	targetRevision := seedProgramEventTargetLocaleForIntegration(
		t, db, store, created.Msg.Id, "ko", duplicated.Msg.DocumentRevision, memberID,
	)
	targetApplied, err := internalSvc.ApplyProgramEventBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyProgramEventBlockBatchRequest{
		EventId:                created.Msg.Id,
		Locale:                 "ko",
		ExpectedTargetRevision: &targetRevision,
		Batch: programEventFileMutationBatch(
			duplicatedLoaded.Msg.Document,
			duplicated.Msg.DocumentRevision,
			blockID,
			0,
			fileID,
			"source-v1.webp",
			"ko",
			"현지화 alt",
			"현지화된 캡션",
			[]string{memberID},
			false,
		),
	}))
	require.NoError(t, err)
	require.True(t, targetApplied.Msg.Changed)
	require.Equal(t, duplicated.Msg.DocumentRevision, targetApplied.Msg.DocumentRevision)
	targetLoaded, err := internalSvc.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId:   created.Msg.Id,
		Locale:    "ko",
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)
	requireProgramEventFileDocument(t, targetLoaded.Msg.Document, blockID, fileID, "source-v1.webp", "ko", "현지화 alt", "현지화된 캡션")
	requireProgramEventHydratedBlockMedia(t, targetLoaded.Msg.BlockMedia, fileID, blockID, duplicateBlockID)

	sourceBeforeRename, err := internalSvc.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId: created.Msg.Id, Locale: "en",
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)
	sourceRenamed, err := internalSvc.ApplyProgramEventBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyProgramEventBlockBatchRequest{
		EventId: created.Msg.Id,
		Locale:  "en",
		Batch: programEventFileMutationBatch(
			sourceBeforeRename.Msg.Document,
			targetApplied.Msg.DocumentRevision,
			blockID,
			0,
			fileID,
			"source-v2.webp",
			"en",
			"Renamed source alt",
			"Renamed source caption",
			[]string{memberID},
			true,
		),
	}))
	require.NoError(t, err)
	require.True(t, sourceRenamed.Msg.Changed)
	require.NotEqual(t, sourceRenamed.Msg.DocumentRevision, targetApplied.Msg.DocumentRevision)
	sourceRenamedLoaded, err := internalSvc.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
		EventId:   created.Msg.Id,
		Locale:    "ko",
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)
	requireProgramEventFileDocument(t, sourceRenamedLoaded.Msg.Document, blockID, fileID, "source-v2.webp", "ko", "현지화 alt", "현지화된 캡션")
	requireProgramEventHydratedBlockMedia(t, sourceRenamedLoaded.Msg.BlockMedia, fileID, blockID, duplicateBlockID)
	managed, err := eventSvc.GetProgramEvent(ctx, connect.NewRequest(&managev1.GetProgramEventRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	requireProgramEventHydratedBlockMedia(t, managed.Msg.BlockMedia, fileID, blockID, duplicateBlockID)

	missingAssetFileID := seedImageBindingUploadedFileFixture(t, db, "program-event-block/missing.webp")
	require.NoError(t, db.Where("source_file_id = ?", missingAssetFileID).Delete(&model.PublicAsset{}).Error)
	nonReadyAssetFileID := seedImageBindingUploadedFileFixture(t, db, "program-event-block/not-ready.webp")
	require.NoError(t, db.Model(&model.PublicAsset{}).
		Where("source_file_id = ?", nonReadyAssetFileID).
		Update("status", model.PublicAssetStatusAllocated).Error)
	currentDocument := sourceRenamedLoaded.Msg.Document
	currentRevision := sourceRenamed.Msg.DocumentRevision
	for index, scenario := range []struct {
		name   string
		fileID string
	}{
		{name: "missing ready asset", fileID: missingAssetFileID},
		{name: "non-ready asset", fileID: nonReadyAssetFileID},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			blockID := integrationTestUUID()
			applied, err := internalSvc.ApplyProgramEventBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyProgramEventBlockBatchRequest{
				EventId: created.Msg.Id,
				Locale:  "en",
				Batch: programEventFileMutationBatch(
					currentDocument,
					currentRevision,
					blockID,
					uint32(index+2),
					scenario.fileID,
					"invalid-asset.webp",
					"en",
					"Invalid asset alt",
					"Invalid asset caption",
					[]string{memberID},
					true,
				),
			}))
			require.NoError(t, err)
			require.True(t, applied.Msg.Changed)
			loaded, err := internalSvc.LoadProgramEventBlockDocument(ctx, connect.NewRequest(&intrav1.LoadProgramEventBlockDocumentRequest{
				EventId:   created.Msg.Id,
				Locale:    "en",
				Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
			}))
			require.NoError(t, err)
			var item *contentv1.ContentBlockMediaItem
			for _, candidate := range loaded.Msg.BlockMedia {
				if candidate.GetSelector().GetBlockId() == blockID {
					item = candidate
					break
				}
			}
			require.NotNil(t, item)
			require.Equal(t, scenario.fileID, item.GetAttachment().GetActiveFileId())
			currentDocument = loaded.Msg.Document
			currentRevision = applied.Msg.DocumentRevision
		})
	}

	var fileReferenceCount int64
	require.NoError(t, db.Table("content_block_attachment").Where("file_id = ?", fileID).Count(&fileReferenceCount).Error)
	require.EqualValues(t, 2, fileReferenceCount)
}

func newProgramEventIntegrationContentBlockStore(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
) *contentblock.Store {
	t.Helper()
	store, err := contentblock.NewGeneratedStore(programEventIntegrationFileReuseAuthorizer{})
	require.NoError(t, err)
	return store
}

func seedProgramEventTargetLocaleForIntegration(
	t *testing.T,
	db *gorm.DB,
	store *contentblock.Store,
	eventID string,
	locale string,
	documentRevision string,
	contributorID string,
) string {
	t.Helper()
	documentID, err := loadProgramEventContentDocumentID(t.Context(), db, eventID, false)
	require.NoError(t, err)
	expectedRevision := uuid.MustParse(documentRevision)
	contributor := uuid.MustParse(contributorID)
	var output programEventTargetMutationResult
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		output, applyErr = applyProgramEventTargetMutation(
			t.Context(), tx, store,
			programEventTargetMutationInput{
				EventID: eventID, DocumentID: documentID, Locale: locale,
				Batch: contentblock.Batch{
					DocumentID: documentID, ExpectedRevision: expectedRevision,
					ContributorMemberIDs: []uuid.UUID{contributor},
				},
				ExpectedDocumentRevision: expectedRevision,
				AllowCreate:              true,
				SeedSourceOnCreate:       true,
				Now:                      time.Now().UTC(),
				Fence: programEventContentDocumentFence(
					eventID,
					func(context.Context, *gorm.DB) error { return nil },
				),
			},
		)
		return applyErr
	}))
	require.True(t, output.LocaleCreated)
	require.NotEmpty(t, output.TargetRevision)
	return output.TargetRevision
}

type programEventIntegrationFileReuseAuthorizer struct{}

func (programEventIntegrationFileReuseAuthorizer) AuthorizeFileReuse(
	context.Context,
	*gorm.DB,
	contentblock.Document,
	contentblock.FullBlock,
	contentblock.FileReference,
	contentblock.File,
) error {
	return nil
}

type programEventIntegrationFileService struct{}

func newProgramEventIntegrationFileService(*gorm.DB, *auth.SpiceDBClient) *programEventIntegrationFileService {
	return &programEventIntegrationFileService{}
}

func (*programEventIntegrationFileService) DeleteFileByID(context.Context, string) error { return nil }

func (*programEventIntegrationFileService) HydrateAuthorizedProgramEventBlockMediaWithDB(
	_ context.Context,
	_ *gorm.DB,
	_ string,
	_ uuid.UUID,
	_ *auth.UserInfo,
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
			copy.Delivery = &commonv1.MediaDelivery{
				FileId:   fileID,
				Inline:   &commonv1.ExpiringMediaRef{FileId: fileID, Url: "https://media.example.com/inline/" + fileID, Purpose: commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_INLINE},
				Download: &commonv1.ExpiringMediaRef{FileId: fileID, Url: "https://media.example.com/download/" + fileID, Purpose: commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_DOWNLOAD},
			}
			copy.DownloadAvailability = contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_AVAILABLE
			copy.DownloadAction = contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_DOWNLOAD
		}
		result = append(result, copy)
	}
	return result, nil
}

func insertProgramEventIntegrationSession(t *testing.T, db *gorm.DB, identityID string) string {
	t.Helper()
	sessionID := integrationTestUUID()
	require.NoError(t, db.Exec(`
		INSERT INTO kratos.sessions (
			id, identity_id, active, authenticated_at, expires_at,
			created_at, updated_at, nid, authentication_methods
		)
		SELECT ?::uuid, id, TRUE, NOW(), NOW() + INTERVAL '1 hour',
		       NOW(), NOW(), nid, '[]'::jsonb
		FROM kratos.identities
		WHERE id = ?::uuid
	`, sessionID, identityID).Error)
	t.Cleanup(func() { _ = db.Exec("DELETE FROM kratos.sessions WHERE id = ?::uuid", sessionID).Error })
	return sessionID
}

func programEventParagraphMutationBatch(
	document *contentv1.LocalizedRichTextDocument,
	expectedRevision string,
	blockID string,
	locale string,
	text string,
	contributors []string,
	includeBase bool,
) *contentv1.RichTextBlockMutationBatch {
	batch := &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(),
		Profile:                 document.GetProfile(),
		ExpectedRevision:        expectedRevision,
		ContributorMemberIds:    contributors,
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

func programEventFileMutationBatch(
	document *contentv1.LocalizedRichTextDocument,
	expectedRevision string,
	blockID string,
	index uint32,
	fileID string,
	name string,
	locale string,
	alt string,
	caption string,
	contributors []string,
	includeBase bool,
) *contentv1.RichTextBlockMutationBatch {
	batch := &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(),
		Profile:                 document.GetProfile(),
		ExpectedRevision:        expectedRevision,
		ContributorMemberIds:    contributors,
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
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
		}},
	}
	if includeBase {
		batch.BaseMutations = []*contentv1.RichTextBlockMutation{{
			Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
				Node: &contentv1.RichTextBlockNode{
					Block: &contentv1.RichTextBlock{
						Id: blockID,
						Value: &contentv1.RichTextBlock_File{File: &contentv1.FileBlock{Props: &contentv1.FileProps{
							Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: fileID}},
							Name:       &name,
						}}},
					},
					Placement: &contentv1.ContentBlockPlacement{Index: index},
				},
			}},
		}}
	}
	return batch
}

func programEventParagraphText(t *testing.T, document *contentv1.LocalizedRichTextDocument, locale string, blockID string) string {
	t.Helper()
	overlay := document.GetLocaleOverlay()
	require.Equal(t, locale, overlay.GetLocale())
	for _, block := range overlay.GetBlocks() {
		if block.GetBlockId() == blockID {
			paragraph := block.GetParagraph()
			require.NotNil(t, paragraph)
			require.Len(t, paragraph.GetContent(), 1)
			return paragraph.GetContent()[0].GetText().GetText()
		}
	}
	t.Fatalf("locale overlay %q Block %q not found", locale, blockID)
	return ""
}

func requireProgramEventFileDocument(
	t *testing.T,
	document *contentv1.LocalizedRichTextDocument,
	blockID string,
	fileID string,
	name string,
	locale string,
	alt string,
	caption string,
) {
	t.Helper()
	var base *contentv1.RichTextBlock
	for _, node := range document.GetBase().GetNodes() {
		if node.GetBlock().GetId() == blockID {
			base = node.GetBlock()
			break
		}
	}
	require.NotNil(t, base, "File Block %q not found", blockID)
	require.Equal(t, fileID, base.GetFile().GetProps().GetAttachment().GetActiveFileId())
	require.Equal(t, name, base.GetFile().GetProps().GetName())
	overlay := document.GetLocaleOverlay()
	require.Equal(t, locale, overlay.GetLocale())
	for _, block := range overlay.GetBlocks() {
		if block.GetBlockId() == blockID {
			require.Equal(t, alt, block.GetFile().GetProps().GetAlt())
			require.Equal(t, caption, block.GetFile().GetProps().GetCaption())
			return
		}
	}
	t.Fatalf("locale overlay %q File Block %q not found", locale, blockID)
}

func requireProgramEventHydratedBlockMedia(
	t *testing.T,
	items []*contentv1.ContentBlockMediaItem,
	fileID string,
	blockIDs ...string,
) {
	t.Helper()
	require.Len(t, items, len(blockIDs))
	wanted := make(map[string]struct{}, len(blockIDs))
	for _, blockID := range blockIDs {
		wanted[blockID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		blockID := item.GetSelector().GetBlockId()
		_, expected := wanted[blockID]
		require.True(t, expected, "unexpected Block media selector %q", blockID)
		require.Equal(t, "file", item.GetSelector().GetReferencePath())
		require.Equal(t, fileID, item.GetAttachment().GetActiveFileId())
		require.Equal(t, fileID, item.GetDelivery().GetFileId())
		require.NotEmpty(t, item.GetDelivery().GetInline().GetUrl())
		require.NotEmpty(t, item.GetDelivery().GetDownload().GetUrl())
		require.Equal(t, contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_AVAILABLE, item.GetDownloadAvailability())
		require.Equal(t, contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_DOWNLOAD, item.GetDownloadAction())
		_, duplicate := seen[blockID]
		require.False(t, duplicate, "duplicate Block media selector %q", blockID)
		seen[blockID] = struct{}{}
	}
	require.Len(t, seen, len(wanted))
}
