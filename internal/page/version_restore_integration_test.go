//go:build integration

package page

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/translation"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

func TestPageVersionRestoreUsesOneRevisionAndPreservesTargetLocaleIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Page Version Restore Admin")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	ctx := withPageAuditedRequestContext(t, auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID: auth.IdentityID(adminID), MemberID: auth.MemberID(integrationMemberID(adminID)),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	}))

	store, err := contentblock.NewGeneratedStore(
		NewContentBlockFileReuseAuthorizer(spiceDB),
	)
	require.NoError(t, err)
	pageService := NewPageService(
		db,
		newPageRuntimeForTest(db, "https://cdn.example.com"),
		&recordingPageDeleteFileDeleter{},
		noopAsyncPublisher{},
		&fakeIdentityManager{identity: postIntegrationIdentity(adminID, "en")},
		spiceDB,
		WithPageContentBlockStore(store),
		WithPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
	)
	internalService := NewInternalPageService(
		db,
		noopAsyncPublisher{},
		spiceDB,
		newPageRuntimeForTest(db, "https://cdn.example.com"),
		WithInternalPageDomainAuditWriter(apitelemetry.NewDurableWriter(db)),
		WithInternalPageContentBlockStore(store),
		WithInternalPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
	)
	created, err := pageService.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{
		Title: "Typed Page Version Restore",
	}))
	require.NoError(t, err)
	attachInternalResourcePolicy(t, spiceDB, created.Msg.Id)

	require.NoError(t, upsertTypedBlockTranslationEntryMetadata(
		ctx,
		db,
		"page",
		created.Msg.Id,
		"ko",
		translation.EntryWrite{Now: time.Now().UTC()},
	))

	sectionID := integrationTestUUID()
	memberID := integrationMemberID(adminID)
	versionA, err := internalService.ApplyPageBlockBatch(
		ctx,
		connect.NewRequest(&intrav1.ApplyPageBlockBatchRequest{
			PageId: created.Msg.Id,
			Locale: "en",
			Batch: pageVersionExternalVideoBatch(
				created.Msg.Revision,
				sectionID,
				"Source A",
				"번역 A",
				memberID,
			),
		}),
	)
	require.NoError(t, err)
	require.True(t, versionA.Msg.Changed)
	versionA.Msg.DocumentRevision = applyPageVersionTargetCaption(
		t, ctx, db, store, created.Msg.Id, versionA.Msg.DocumentRevision, sectionID, "번역 A",
	)

	checkpoint, err := internalService.CreatePageVersionCheckpoint(
		withPageAuditedCollabRequestContext(t, ctx),
		connect.NewRequest(&intrav1.CreatePageVersionCheckpointRequest{
			PageId:               created.Msg.Id,
			ExpectedRevision:     versionA.Msg.DocumentRevision,
			ContributorMemberIds: []string{memberID},
			Locale:               "en",
		}),
	)
	require.NoError(t, err)
	require.True(t, checkpoint.Msg.Created)
	require.NotNil(t, checkpoint.Msg.VersionId)
	var checkpointVersion model.PageVersion
	require.NoError(t, db.First(&checkpointVersion, "id = ?", checkpoint.Msg.GetVersionId()).Error)
	require.NotEmpty(t, checkpointVersion.ContentSnapshot)

	changedTitle := "Changed before Page version restore"
	metadataB, err := internalService.UpdatePageLocaleMetadata(
		ctx,
		connect.NewRequest(&intrav1.UpdatePageLocaleMetadataRequest{
			PageId:               created.Msg.Id,
			Title:                &changedTitle,
			ExpectedRevision:     versionA.Msg.DocumentRevision,
			ContributorMemberIds: []string{memberID},
			Locale:               "en",
		}),
	)
	require.NoError(t, err)
	require.True(t, metadataB.Msg.Changed)

	versionB, err := internalService.ApplyPageBlockBatch(
		ctx,
		connect.NewRequest(&intrav1.ApplyPageBlockBatchRequest{
			PageId: created.Msg.Id,
			Locale: "en",
			Batch: pageVersionExternalVideoBatch(
				metadataB.Msg.DocumentRevision,
				sectionID,
				"Source B",
				"번역 B",
				memberID,
			),
		}),
	)
	require.NoError(t, err)
	require.True(t, versionB.Msg.Changed)
	versionB.Msg.DocumentRevision = applyPageVersionTargetCaption(
		t, ctx, db, store, created.Msg.Id, versionB.Msg.DocumentRevision, sectionID, "번역 B",
	)
	jobNow := time.Now().UTC()
	activeJob := model.TranslationJob{
		ID:                    integrationTestUUID(),
		EntityType:            "page",
		EntityID:              created.Msg.Id,
		RequestedByMemberID:   memberID,
		TargetLocale:          "fr",
		SourceLocale:          "en",
		RequestArtifactDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		OperationID:           integrationTestUUID(),
		Status:                "queued",
		RequestXLIFF:          []byte(`<?xml version="1.0" encoding="UTF-8"?><xliff xmlns="urn:oasis:names:tc:xliff:document:2.2" version="2.2" srcLang="en" trgLang="fr"></xliff>`),
		RequestManifest:       []byte(`{}`),
		RequestedAt:           jobNow,
		CreatedAt:             jobNow,
		UpdatedAt:             jobNow,
	}
	require.NoError(t, db.Create(&activeJob).Error)
	type storedTargetMetadata struct {
		Title     *string   `gorm:"column:title"`
		Summary   *string   `gorm:"column:summary"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	var targetBeforeSwitch storedTargetMetadata
	require.NoError(t, db.Table("page_translation").
		Where("entity_id = ? AND locale = ?", created.Msg.Id, "ko").
		Take(&targetBeforeSwitch).Error)
	currentRevision, err := uuid.Parse(versionB.Msg.DocumentRevision)
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return switchBlockVersionRestoreSourceLocale(
			ctx,
			tx,
			store,
			"page",
			created.Msg.Id,
			"ko",
			currentRevision,
			"en",
			jobNow.Add(time.Second),
		)
	}))
	require.Equal(t, "ko", loadPageVersionRootSourceLocale(t, db, created.Msg.Id))

	restored, err := pageService.RestorePageVersion(
		ctx,
		connect.NewRequest(&managev1.RestorePageVersionRequest{
			PageId:    created.Msg.Id,
			VersionId: checkpoint.Msg.GetVersionId(),
		}),
	)
	require.NoError(t, err)
	require.NotEqual(t, versionB.Msg.DocumentRevision, restored.Msg.Revision)
	require.Equal(t, "en", loadPageVersionRootSourceLocale(t, db, created.Msg.Id))
	var activeJobAfterRestore model.TranslationJob
	require.NoError(t, db.First(&activeJobAfterRestore, "id = ?", activeJob.ID).Error)
	require.Equal(t, activeJob.Status, activeJobAfterRestore.Status)
	require.Equal(t, activeJob.SourceLocale, activeJobAfterRestore.SourceLocale)
	require.Equal(t, "Typed Page Version Restore", restored.Msg.Title)
	require.Equal(t, "Source A", pageVersionExternalVideoCaption(
		t,
		restored.Msg.Document,
		"en",
		sectionID,
	))
	require.Equal(t, "번역 B", pageVersionExternalVideoCaption(
		t,
		restored.Msg.Document,
		"ko",
		sectionID,
	))

	var targetAfterRestore storedTargetMetadata
	require.NoError(t, db.Table("page_translation").
		Where("entity_id = ? AND locale = ?", created.Msg.Id, "ko").
		Take(&targetAfterRestore).Error)
	require.Equal(t, targetBeforeSwitch, targetAfterRestore)

	_, err = internalService.ApplyPageBlockBatch(
		ctx,
		connect.NewRequest(&intrav1.ApplyPageBlockBatchRequest{
			PageId: created.Msg.Id,
			Locale: "en",
			Batch: pageVersionExternalVideoBatch(
				versionB.Msg.DocumentRevision,
				sectionID,
				"Source B",
				"번역 B",
				memberID,
			),
		}),
	)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func pageVersionExternalVideoBatch(
	expectedRevision string,
	sectionID string,
	sourceCaption string,
	_ string,
	contributorMemberID string,
) *contentv1.PageSectionMutationBatch {
	localeMutation := func(locale string, caption string) *contentv1.PageLocaleMutationGroup {
		return &contentv1.PageLocaleMutationGroup{
			Locale: locale,
			Mutations: []*contentv1.PageSectionLocaleMutation{{
				Operation: &contentv1.PageSectionLocaleMutation_Upsert{Upsert: &contentv1.UpsertPageSectionLocale{
					Section: &contentv1.PageSectionLocale{
						SectionId: sectionID,
						Value: &contentv1.PageSectionLocale_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSectionLocale{
							Props: &contentv1.ExternalVideoSectionLocaleProps{Caption: &caption},
						}},
					},
				}},
			}},
		}
	}
	return &contentv1.PageSectionMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		ExpectedRevision:        expectedRevision,
		ContributorMemberIds:    []string{contributorMemberID},
		BaseMutations: []*contentv1.PageSectionMutation{{
			Operation: &contentv1.PageSectionMutation_Upsert{Upsert: &contentv1.UpsertPageSection{
				Node: &contentv1.PageSectionNode{
					Section: &contentv1.PageSection{
						Id:       sectionID,
						Settings: &contentv1.PageSectionSettings{},
						Value: &contentv1.PageSection_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSection{
							Props: &contentv1.ExternalVideoSectionProps{Uri: "https://video.example.test/watch"},
						}},
					},
					Placement: &contentv1.PageSectionPlacement{Index: 0},
				},
			}},
		}},
		LocaleMutationGroups: []*contentv1.PageLocaleMutationGroup{
			localeMutation("en", sourceCaption),
		},
	}
}

func applyPageVersionTargetCaption(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	pageID string,
	expectedRevision string,
	sectionID string,
	caption string,
) string {
	t.Helper()
	job := &model.TranslationJob{EntityType: "page", EntityID: pageID, TargetLocale: "ko"}
	candidate := &translation.Candidate{
		ContentDocumentRevision: expectedRevision,
		PageDocument: &contentv1.LocalizedPageDocument{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Locale:                  "ko",
			LocaleOverlay: &contentv1.PageLocaleOverlay{
				Locale: "ko",
				Sections: []*contentv1.PageSectionLocale{{
					SectionId: sectionID,
					Value: &contentv1.PageSectionLocale_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSectionLocale{
						Props: &contentv1.ExternalVideoSectionLocaleProps{Caption: &caption},
					}},
				}},
			},
		},
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		job.SourceLocale = loadPageVersionRootSourceLocale(t, tx, pageID)
		return ApplyTranslationCandidateWithDB(
			ctx,
			tx,
			store,
			job,
			candidate,
			translation.EntryWrite{Now: time.Now().UTC()},
			nil,
		)
	}))
	revision, err := loadCurrentBlockDocumentRevision(ctx, db, "page", pageID)
	require.NoError(t, err)
	return revision.String()
}

func loadPageVersionRootSourceLocale(t *testing.T, db *gorm.DB, pageID string) string {
	t.Helper()
	var sourceLocale string
	require.NoError(t, db.Table("page").
		Select("source_locale").
		Where("id = ?", pageID).
		Scan(&sourceLocale).Error)
	require.NotEmpty(t, sourceLocale)
	return sourceLocale
}

func pageVersionExternalVideoCaption(
	t *testing.T,
	document *contentv1.PageDocument,
	locale string,
	sectionID string,
) string {
	t.Helper()
	require.NotNil(t, document)
	for _, overlay := range document.LocaleOverlays {
		if overlay.GetLocale() != locale {
			continue
		}
		for _, section := range overlay.Sections {
			if section.GetSectionId() == sectionID && section.GetExternalVideo() != nil {
				return section.GetExternalVideo().GetProps().GetCaption()
			}
		}
	}
	t.Fatalf("Page version section %s locale %s was not found", sectionID, locale)
	return ""
}
