//go:build integration

package campaign

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestCampaignAIReplacementUsesCurrentTargetRevisionIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := auth.WithUser(campaignRequestContext(t), admin.AuthUserInfo())
	store := testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient)
	created, err := NewCampaignService(
		db,
		newCampaignRuntimeFixture(nil, nil),
		"",
		"",
		stack.SpiceDBClient,
		WithCampaignContentBlockStore(store),
	).CreateCampaign(ctx, connect.NewRequest(&managev1.CreateCampaignRequest{
		Name: "AI target replacement", Subject: "Source subject", SourceLocale: "en",
		Target: campaignAllTarget(),
	}))
	require.NoError(t, err)

	contributorID := testutil.IntegrationUUID()
	testutil.InsertAuthorizedDocumentContributor(t, db, stack.SpiceDBClient, contributorID)
	internal := NewAuditedInternalCampaignService(
		db, apitelemetry.NewDurableWriter(db),
		WithInternalCampaignContentBlockStore(store),
		WithInternalCampaignSpiceDB(stack.SpiceDBClient),
		WithInternalCampaignCheckpoints(testcollaboration.NewCheckpoints(db, stack.SpiceDBClient)),
	)
	sourceWrite, err := internal.ApplyBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyCampaignBlockBatchRequest{
		CampaignId: created.Msg.Campaign.Id,
		Locale:     "en",
		Batch: testutil.NewParagraphBatch(
			created.Msg.Campaign.Document,
			created.Msg.Campaign.DocumentRevision,
			"en",
			"Source body",
			[]string{contributorID},
		),
	}))
	require.NoError(t, err)

	source, err := LoadTranslationSourceDocument(ctx, db, store, created.Msg.Campaign.Id)
	require.NoError(t, err)
	job := &model.TranslationJob{
		EntityType: campaignContentEntity, EntityID: created.Msg.Campaign.Id,
		SourceLocale: "en", TargetLocale: "ko",
		RequestedByMemberID: contributorID,
	}
	plan, err := translation.BuildRichTextExtractionPlan(
		job,
		source,
		translation.RichTextDocumentFields{Title: true},
	)
	require.NoError(t, err)
	results := make(map[string]translation.UnitResult, len(plan.Units))
	for _, unit := range plan.Units {
		translated := "AI body"
		if unit.FieldName == "title" {
			translated = "AI subject"
		}
		results[unit.UnitID] = translation.UnitResult{UnitID: unit.UnitID, TranslatedText: translated}
	}
	candidate, err := translation.BuildRichTextCandidate(plan, source, results)
	require.NoError(t, err)
	require.Equal(t, sourceWrite.Msg.DocumentRevision, candidate.ContentDocumentRevision)

	require.NoError(t, db.Exec(`
		INSERT INTO campaign_translation (entity_id, locale, subject, created_at, updated_at)
		VALUES (?, 'ko', NULL, NOW(), NOW())`,
		created.Msg.Campaign.Id,
	).Error)
	documentID, err := loadCampaignEmailContentDocumentID(ctx, db, campaignContentEntity, created.Msg.Campaign.Id)
	require.NoError(t, err)
	targetState, err := loadCampaignExactLocaleState(
		ctx, db, store, created.Msg.Campaign.Id, documentID, "ko", false,
	)
	require.NoError(t, err)
	require.NotNil(t, targetState.TargetMetadata)
	blockID := source.ContentBlockDocument.GetLocaleOverlay().GetBlocks()[0].GetBlockId()
	targetWrite, err := internal.ApplyBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyCampaignBlockBatchRequest{
		CampaignId:             created.Msg.Campaign.Id,
		Locale:                 "ko",
		ExpectedTargetRevision: &targetState.TargetRevision,
		Batch: &contentv1.RichTextBlockMutationBatch{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL,
			ExpectedRevision:        sourceWrite.Msg.DocumentRevision,
			ContributorMemberIds:    []string{contributorID},
			LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
				Locale: "ko",
				Mutations: []*contentv1.RichTextBlockLocaleMutation{{
					Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
						Block: campaignTranslationParagraph(blockID, "Manual target body"),
					}},
				}},
			}},
		},
	}))
	require.NoError(t, err)
	require.Equal(t, candidate.ContentDocumentRevision, targetWrite.Msg.DocumentRevision)
	require.NotEmpty(t, targetWrite.Msg.GetTargetRevision())

	staleInterchangeSubject := "must not win"
	staleTargetRevision := targetState.TargetRevision
	err = db.Transaction(func(tx *gorm.DB) error {
		_, applyErr := ApplyTranslationInterchange(
			ctx, tx, store, created.Msg.Campaign.Id, "en",
			TranslationInterchangeMutation{
				TargetLocale: "ko", ExpectedDocumentRevision: sourceWrite.Msg.DocumentRevision,
				ExpectedTargetRevision: &staleTargetRevision, ExpectedPresence: true,
				Subject: &staleInterchangeSubject, ContributorMemberID: contributorID, Now: time.Now().UTC(),
			},
		)
		return applyErr
	})
	var targetConflict *translation.TargetRevisionConflict
	require.ErrorAs(t, err, &targetConflict)
	afterStaleInterchange, err := loadCampaignExactLocaleState(
		ctx, db, store, created.Msg.Campaign.Id, documentID, "ko", false,
	)
	require.NoError(t, err)
	require.Equal(t, targetWrite.Msg.GetTargetRevision(), afterStaleInterchange.TargetRevision)

	now := time.Now().UTC()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return ApplyTranslationCandidate(ctx, tx, store, job, candidate, translation.EntryWrite{
			Title: candidate.Title, Now: now,
		}, apitelemetry.NewDurableWriter(db))
	}))
	currentSourceAfterProvider, err := LoadTranslationSourceDocument(ctx, db, store, created.Msg.Campaign.Id)
	require.NoError(t, err)
	require.Equal(t, candidate.ContentDocumentRevision, currentSourceAfterProvider.ContentDocumentRevision)
	var providerAuditActor string
	require.NoError(t, db.Raw(`
		SELECT actor_member_id::text
		FROM domain_audit
		WHERE action = ? AND target_type = 'campaign' AND target_id = ?
		  AND attributes @> '{"changed_fields":["locale_content"],"locale":"ko"}'::jsonb
		ORDER BY occurred_at DESC, audit_id DESC
		LIMIT 1`, sharedtelemetry.AuditCampaignUpdated, created.Msg.Campaign.Id).
		Scan(&providerAuditActor).Error)
	require.Equal(t, contributorID, providerAuditActor)

	localized, locale, err := ResolveLocalizedCampaign(
		ctx, db, store, model.Campaign{ID: created.Msg.Campaign.Id}, "ko",
	)
	require.NoError(t, err)
	require.Equal(t, "ko", locale)
	require.Equal(t, "AI subject", localized.Subject)
	require.Contains(t, ptrStringValue(localized.ContentHTML), "AI body")

	var targetBefore campaignTranslationPersistenceRow
	require.NoError(t, db.Table("campaign_translation").
		Select("subject, content_html, content_text, updated_at").
		Where("entity_id = ? AND locale = ?", created.Msg.Campaign.Id, "ko").
		Take(&targetBefore).Error)
	queuedJob := model.TranslationJob{
		ID: uuid.NewString(), EntityType: campaignContentEntity, EntityID: created.Msg.Campaign.Id,
		TargetLocale: "fr", SourceLocale: "en",
		RequestedByMemberID: contributorID,
		OperationID:         uuid.NewString(), Status: "queued", RequestedAt: now,
		RequestArtifactDigest: translation.RequestArtifactDigest([]byte("request"), []byte("{}")),
		RequestXLIFF:          []byte("request"), RequestManifest: []byte("{}"),
		CreatedAt: now, UpdatedAt: now,
	}
	runningAt := now.Add(time.Second)
	runningJob := model.TranslationJob{
		ID: uuid.NewString(), EntityType: campaignContentEntity, EntityID: created.Msg.Campaign.Id,
		TargetLocale: "de", SourceLocale: "en",
		RequestedByMemberID: contributorID,
		OperationID:         uuid.NewString(), Status: "running", RequestedAt: now, StartedAt: &runningAt,
		RequestArtifactDigest: translation.RequestArtifactDigest([]byte("request"), []byte("{}")),
		RequestXLIFF:          []byte("request"), RequestManifest: []byte("{}"),
		CreatedAt: now, UpdatedAt: runningAt,
	}
	require.NoError(t, db.Create(&queuedJob).Error)
	require.NoError(t, db.Create(&runningJob).Error)
	loadJobs := func() []campaignTranslationJobPersistenceRow {
		var jobs []campaignTranslationJobPersistenceRow
		require.NoError(t, db.Table("translation_job").
			Select("id, status, updated_at").
			Where("id IN ?", []string{queuedJob.ID, runningJob.ID}).
			Order("id").
			Find(&jobs).Error)
		return jobs
	}
	jobsBefore := loadJobs()
	require.Len(t, jobsBefore, 2)
	currentSource, err := LoadTranslationSourceDocument(ctx, db, store, created.Msg.Campaign.Id)
	require.NoError(t, err)
	newSourceBlockID := uuid.NewString()
	sourceChanged, err := internal.ApplyBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyCampaignBlockBatchRequest{
		CampaignId: created.Msg.Campaign.Id,
		Locale:     "en",
		Batch: &contentv1.RichTextBlockMutationBatch{
			BlockCatalogFingerprint: created.Msg.Campaign.Document.GetBlockCatalogFingerprint(),
			Profile:                 created.Msg.Campaign.Document.GetProfile(),
			ExpectedRevision:        currentSource.ContentDocumentRevision,
			ContributorMemberIds:    []string{contributorID},
			BaseMutations: []*contentv1.RichTextBlockMutation{{
				Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
					Node: &contentv1.RichTextBlockNode{
						Block: &contentv1.RichTextBlock{
							Id: newSourceBlockID,
							Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{
								Props: &contentv1.ParagraphProps{},
							}},
						},
						Placement: &contentv1.ContentBlockPlacement{Index: 1},
					},
				}},
			}},
			LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
				Locale: "en",
				Mutations: []*contentv1.RichTextBlockLocaleMutation{{
					Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
						Block: campaignTranslationParagraph(newSourceBlockID, "New source-only body"),
					}},
				}},
			}},
		},
	}))
	require.NoError(t, err)
	require.True(t, sourceChanged.Msg.SourceChanged)

	var targetAfter campaignTranslationPersistenceRow
	require.NoError(t, db.Table("campaign_translation").
		Select("subject, content_html, content_text, updated_at").
		Where("entity_id = ? AND locale = ?", created.Msg.Campaign.Id, "ko").
		Take(&targetAfter).Error)
	require.Equal(t, targetBefore, targetAfter, "source edits must not mutate an existing Campaign target row")
	require.Equal(t, jobsBefore, loadJobs(), "source edits must preserve queued and running Campaign jobs until explicit cancellation or terminal completion")

	localized, locale, err = ResolveLocalizedCampaign(
		ctx, db, store, model.Campaign{ID: created.Msg.Campaign.Id}, "ko",
	)
	require.NoError(t, err)
	require.Equal(t, "ko", locale)
	require.Contains(t, ptrStringValue(localized.ContentHTML), "AI body")
	require.Contains(t, ptrStringValue(localized.ContentHTML), "New source-only body")

	var campaign model.Campaign
	require.NoError(t, db.First(&campaign, "id = ?", created.Msg.Campaign.Id).Error)
	var snapshot CampaignDeliverySnapshot
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var snapshotErr error
		snapshot, snapshotErr = campaignRenderSnapshot(ctx, tx, store, campaign, nil)
		return snapshotErr
	}))
	var koreanSnapshot *CampaignDeliverySnapshotTranslation
	for index := range snapshot.Translations {
		if snapshot.Translations[index].Locale == "ko" {
			koreanSnapshot = &snapshot.Translations[index]
			break
		}
	}
	require.NotNil(t, koreanSnapshot)
	require.Contains(t, koreanSnapshot.ContentHTML, "AI body")
	require.Contains(t, koreanSnapshot.ContentHTML, "New source-only body")
}

func TestCampaignSharedPresentationEditProjectsSourceLocaleIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := auth.WithUser(context.Background(), admin.AuthUserInfo())
	store := testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient)
	created, err := NewCampaignService(
		db,
		newCampaignRuntimeFixture(nil, nil),
		"",
		"",
		stack.SpiceDBClient,
		WithCampaignContentBlockStore(store),
	).CreateCampaign(ctx, connect.NewRequest(&managev1.CreateCampaignRequest{
		Name: "Shared presentation edit", Subject: "Source subject", SourceLocale: "en",
		Target: campaignAllTarget(),
	}))
	require.NoError(t, err)

	contributorID := testutil.IntegrationUUID()
	testutil.InsertAuthorizedDocumentContributor(t, db, stack.SpiceDBClient, contributorID)
	internal := NewInternalCampaignService(
		db,
		WithInternalCampaignContentBlockStore(store),
		WithInternalCampaignSpiceDB(stack.SpiceDBClient),
		WithInternalCampaignCheckpoints(testcollaboration.NewCheckpoints(db, stack.SpiceDBClient)),
	)
	seeded, err := internal.ApplyBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyCampaignBlockBatchRequest{
		CampaignId: created.Msg.Campaign.Id,
		Locale:     "en",
		Batch: testutil.NewParagraphBatch(
			created.Msg.Campaign.Document,
			created.Msg.Campaign.DocumentRevision,
			"en",
			"Source body",
			[]string{contributorID},
		),
	}))
	require.NoError(t, err)

	source, err := LoadTranslationSourceDocument(ctx, db, store, created.Msg.Campaign.Id)
	require.NoError(t, err)
	blockID := source.ContentBlockDocument.GetLocaleOverlay().GetBlocks()[0].GetBlockId()
	background := "#112233"
	applied, err := internal.ApplyBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyCampaignBlockBatchRequest{
		CampaignId: created.Msg.Campaign.Id,
		Locale:     "en",
		Batch: &contentv1.RichTextBlockMutationBatch{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL,
			ExpectedRevision:        seeded.Msg.DocumentRevision,
			ContributorMemberIds:    []string{contributorID},
			BaseMutations: []*contentv1.RichTextBlockMutation{{
				Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
					Node: &contentv1.RichTextBlockNode{
						Block: &contentv1.RichTextBlock{
							Id: blockID,
							Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{
								Props: &contentv1.ParagraphProps{BackgroundColor: &background},
							}},
						},
						Placement: &contentv1.ContentBlockPlacement{Index: 0},
					},
				}},
			}},
		},
	}))
	require.NoError(t, err)
	require.True(t, applied.Msg.Changed)
	require.False(t, applied.Msg.SourceChanged)
	require.Empty(t, applied.Msg.ChangedLocales)

	localized, locale, err := ResolveLocalizedCampaign(
		ctx, db, store, model.Campaign{ID: created.Msg.Campaign.Id}, "en",
	)
	require.NoError(t, err)
	require.Equal(t, "en", locale)
	require.Contains(t, ptrStringValue(localized.ContentHTML), "Source body")
	require.Contains(t, ptrStringValue(localized.ContentHTML), background)
}

type campaignTranslationPersistenceRow struct {
	Subject     *string   `gorm:"column:subject"`
	ContentHTML *string   `gorm:"column:content_html"`
	ContentText *string   `gorm:"column:content_text"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

type campaignTranslationJobPersistenceRow struct {
	ID        string    `gorm:"column:id"`
	Status    string    `gorm:"column:status"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func campaignTranslationParagraph(blockID string, text string) *contentv1.RichTextBlockLocale {
	return &contentv1.RichTextBlockLocale{
		BlockId: blockID,
		Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
			Props: &contentv1.ParagraphLocaleProps{},
			Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
				Text: &contentv1.RichTextStyledText{Text: text},
			}}},
		}},
	}
}
