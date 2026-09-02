//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestArchivedWorkAllowsAdminEditsButRestrictsLifecycleIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Archived Work Admin")
	ctx := workIntegrationAdminCtx(adminID)
	workService := newWorkIntegrationService(t, db, adminID, referenceNoopFileDeleter{})
	workSpiceDB := integrationSpiceDB(t)
	isPresent := true

	created, err := workService.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title:     "Archived Work " + integrationTestUUID(),
		Type:      managev1.WorkType_WORK_TYPE_ARTICLE,
		Year:      2026,
		Month:     8,
		IsPresent: &isPresent,
	}))
	require.NoError(t, err)
	require.NotNil(t, created.Msg.Document)
	require.NotEmpty(t, created.Msg.Revision)
	published, err := workService.PublishWork(ctx, connect.NewRequest(&managev1.PublishWorkRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.WorkStatus_WORK_STATUS_PUBLISHED, published.Msg.Status)
	versionTitle := "Archived Work Version"
	versionDocument := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: created.Msg.Document.GetBlockCatalogFingerprint(),
		Profile:                 created.Msg.Document.GetProfile(),
		Locale:                  created.Msg.Document.GetSourceLocale(),
		Base:                    created.Msg.Document.GetBase(),
		LocaleOverlay:           created.Msg.Document.GetLocaleOverlays()[0],
	}
	contentSnapshot, err := workdomain.EncodeVersionContentSnapshot(
		"en",
		&versionTitle,
		nil,
		versionDocument,
	)
	require.NoError(t, err)
	version := model.WorkVersion{
		WorkID:          created.Msg.Id,
		Version:         1,
		Title:           &versionTitle,
		ContentSnapshot: contentSnapshot,
		CreatedAt:       time.Now().UTC(),
	}
	require.NoError(t, db.Create(&version).Error)

	var originalPublishedAt time.Time
	require.NoError(t, db.Raw("SELECT published_at FROM work WHERE id = ?", created.Msg.Id).Scan(&originalPublishedAt).Error)
	require.False(t, originalPublishedAt.IsZero())
	require.NoError(t, db.Table("work").Where("id = ?", created.Msg.Id).
		Update("status", managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()).Error)

	fetched, err := workService.GetWork(ctx, connect.NewRequest(&managev1.GetWorkRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.WorkStatus_WORK_STATUS_ARCHIVED, fetched.Msg.Status)
	versions, err := workService.ListWorkVersions(ctx, connect.NewRequest(&managev1.ListWorkVersionsRequest{WorkId: created.Msg.Id}))
	require.NoError(t, err)
	require.Len(t, versions.Msg.Versions, 1)
	restored, err := workService.RestoreWorkVersion(ctx, connect.NewRequest(&managev1.RestoreWorkVersionRequest{
		WorkId: created.Msg.Id, VersionId: version.ID,
	}))
	require.NoError(t, err)

	updatedSlug := "updated-while-archived-" + integrationTestUUID()
	updated, err := workService.UpdateWork(ctx, connect.NewRequest(&managev1.UpdateWorkRequest{
		Id:   created.Msg.Id,
		Slug: &updatedSlug,
	}))
	require.NoError(t, err)
	require.Equal(t, updatedSlug, updated.Msg.GetSlug())

	_, err = workService.UnpublishWork(ctx, connect.NewRequest(&managev1.UnpublishWorkRequest{Id: created.Msg.Id}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	_, err = workService.DeleteWork(ctx, connect.NewRequest(&managev1.DeleteWorkRequest{Id: created.Msg.Id}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	blockStore, err := contentblock.NewGeneratedStore(newContentBlockFileReuseAuthorizer(workSpiceDB))
	require.NoError(t, err)
	internalWork := workdomain.NewInternalWorkService(
		db,
		noopAsyncPublisher{},
		newWorkRuntimeForTest(db, ""),
		workSpiceDB,
		workdomain.WithInternalWorkDomainAuditWriter(apitelemetry.NewDurableWriter(db)),
		workdomain.WithInternalWorkContentBlockStore(blockStore),
		workdomain.WithInternalWorkContentBlockMediaHydrator(passthroughWorkContentBlockMediaHydrator{}),
		workdomain.WithInternalWorkCheckpoints(testcollaboration.NewCheckpoints(db, workSpiceDB)),
	)
	blockID := integrationTestUUID()
	blockUpdated, err := internalWork.ApplyWorkBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyWorkBlockBatchRequest{
		WorkId: created.Msg.Id,
		Locale: "en",
		Batch: &contentv1.RichTextBlockMutationBatch{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
			ExpectedRevision:        restored.Msg.Revision,
			ContributorMemberIds:    []string{integrationMemberID(adminID)},
			BaseMutations: []*contentv1.RichTextBlockMutation{{
				Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
					Node: &contentv1.RichTextBlockNode{
						Block: &contentv1.RichTextBlock{
							Id: blockID,
							Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{
								Props: &contentv1.ParagraphProps{},
							}},
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
							Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
								Props: &contentv1.ParagraphLocaleProps{},
								Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
									Text: &contentv1.RichTextStyledText{Text: "Archived Work Admin edit"},
								}}},
							}},
						},
					}},
				}},
			}},
		},
	}))
	require.NoError(t, err)
	require.NotEmpty(t, blockUpdated.Msg.DocumentRevision)
	authorID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, authorID, "Archived Work Author")
	grantIntegrationGlobalRole(t, workSpiceDB, authorID, policyv1.Role.Author())
	authorSessionID := insertWorkIntegrationSession(t, db, authorID)
	authorBootstrap, err := internalWork.LoadWorkBlockDocument(context.Background(), connect.NewRequest(&intrav1.LoadWorkBlockDocumentRequest{
		WorkId: created.Msg.Id, Locale: "en", Principal: &intrav1.CollaborationPrincipal{SessionId: authorSessionID},
	}))
	require.NoError(t, err)
	require.NotNil(t, authorBootstrap.Msg.Document)
	authorTitle := "Author must not persist archived Work metadata"
	_, err = internalWork.UpdateWorkLocaleMetadata(context.Background(), connect.NewRequest(&intrav1.UpdateWorkLocaleMetadataRequest{
		WorkId: created.Msg.Id, Locale: "en", Title: &authorTitle, ExpectedRevision: blockUpdated.Msg.DocumentRevision,
		ContributorMemberIds: []string{integrationMemberID(authorID)},
	}))
	require.Error(t, err)

	creditGroup, err := workService.CreateWorkCreditGroup(ctx, connect.NewRequest(&managev1.CreateWorkCreditGroupRequest{
		WorkId: created.Msg.Id,
		Name:   "archived edits",
	}))
	require.NoError(t, err)
	require.NotEmpty(t, creditGroup.Msg.Id)
	republished, err := workService.PublishWork(ctx, connect.NewRequest(&managev1.PublishWorkRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.WorkStatus_WORK_STATUS_PUBLISHED, republished.Msg.Status)
	var republishedAt time.Time
	require.NoError(t, db.Raw("SELECT published_at FROM work WHERE id = ?", created.Msg.Id).Scan(&republishedAt).Error)
	require.True(t, originalPublishedAt.Equal(republishedAt), "archived to published must preserve published_at")
	_, err = internalWork.LoadWorkBlockDocument(context.Background(), connect.NewRequest(&intrav1.LoadWorkBlockDocumentRequest{
		WorkId: created.Msg.Id, Locale: "en", Principal: &intrav1.CollaborationPrincipal{SessionId: authorSessionID},
	}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

}
