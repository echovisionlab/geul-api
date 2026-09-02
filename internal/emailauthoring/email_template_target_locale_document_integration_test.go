//go:build integration

package emailauthoring

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEmailTemplateTargetCollaborationAndProviderKeepSharedRevisionIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	baseContext, spiceDB := testutil.IntegrationAdminContext(t, db)
	admin := auth.GetUser(baseContext)
	require.NotNil(t, admin)
	ctx := testutil.NewAuditContext(t, string(admin.IdentityID), string(admin.MemberID))
	store := testutil.NewEmailContentBlockStore(t, spiceDB)
	references := integrationCampaignDeliveryReferences{}

	template, err := NewEmailTemplateService(
		db, nil, emailTemplateRuntimeFixture{}, "", "", spiceDB,
		WithEmailTemplateContentBlockStore(store),
		WithEmailTemplateCampaignDeliveryReferences(references),
	).CreateEmailTemplate(ctx, connect.NewRequest(&managev1.CreateEmailTemplateRequest{
		Key: "exact_target_" + strings.ReplaceAll(uuid.NewString(), "-", ""), Name: "Exact target Template",
		Subject: "Source subject", SourceLocale: "en",
	}))
	require.NoError(t, err)

	contributorID := testutil.IntegrationUUID()
	testutil.InsertAuthorizedDocumentContributor(t, db, spiceDB, contributorID)
	internal := NewAuditedInternalEmailTemplateService(
		db, apitelemetry.NewDurableWriter(db), spiceDB,
		WithInternalEmailTemplateContentBlockStore(store),
		WithInternalEmailTemplateCheckpoints(testcollaboration.NewCheckpoints(db, spiceDB)),
		WithInternalEmailTemplateCampaignDeliveryReferences(references),
	)
	sourceWrite, err := internal.ApplyBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyEmailTemplateBlockBatchRequest{
		EmailTemplateId: template.Msg.Id,
		Locale:          "en",
		Batch: testutil.NewParagraphBatch(
			template.Msg.Document, template.Msg.DocumentRevision, "en", "Source body", []string{contributorID},
		),
	}))
	require.NoError(t, err)

	documentID, err := loadCampaignEmailContentDocumentID(ctx, db, emailTemplateContentEntity, template.Msg.Id)
	require.NoError(t, err)
	expectedDocumentRevision := uuid.MustParse(sourceWrite.Msg.DocumentRevision)
	expectedMissing := false
	var createdTarget emailTemplateTargetMutationResult
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var createErr error
		createdTarget, createErr = applyEmailTemplateTargetMutation(
			ctx, tx, store,
			emailTemplateTargetMutationInput{
				TemplateID: template.Msg.Id, DocumentID: documentID, Locale: "ko",
				Batch: contentblock.Batch{
					DocumentID: documentID, ExpectedRevision: expectedDocumentRevision,
				},
				ExpectedDocumentRevision: expectedDocumentRevision,
				ExpectedLocaleExists:     &expectedMissing,
				AllowCreate:              true,
				SeedSourceOnCreate:       true,
				Now:                      time.Now().UTC(),
				Fence: campaignEmailContentFence(
					references, emailTemplateContentEntity, template.Msg.Id,
				),
			},
		)
		return createErr
	}))
	require.True(t, createdTarget.LocaleCreated)
	require.Equal(t, sourceWrite.Msg.DocumentRevision, createdTarget.Result.DocumentRevision.String())
	require.NotEmpty(t, createdTarget.TargetRevision)

	targetState, err := loadEmailTemplateExactLocaleState(
		ctx, db, store, template.Msg.Id, documentID, "ko", false,
	)
	require.NoError(t, err)
	require.NotNil(t, targetState.TargetMetadata)
	sourceDocument, err := LoadTemplateTranslationSourceDocument(ctx, db, store, template.Msg.Id)
	require.NoError(t, err)
	blockID := sourceDocument.ContentBlockDocument.GetLocaleOverlay().GetBlocks()[0].GetBlockId()

	invalidContributors := []string{contributorID, uuid.NewString()}
	_, err = internal.ApplyBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyEmailTemplateBlockBatchRequest{
		EmailTemplateId: template.Msg.Id, Locale: "ko",
		ExpectedTargetRevision: &targetState.TargetRevision,
		Batch: emailTemplateTargetParagraphBatch(
			blockID, sourceWrite.Msg.DocumentRevision, "ko", "거부할 번역", invalidContributors,
		),
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	targetWrite, err := internal.ApplyBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyEmailTemplateBlockBatchRequest{
		EmailTemplateId: template.Msg.Id, Locale: "ko",
		ExpectedTargetRevision: &targetState.TargetRevision,
		Batch: emailTemplateTargetParagraphBatch(
			blockID, sourceWrite.Msg.DocumentRevision, "ko", "번역 본문", []string{contributorID},
		),
	}))
	require.NoError(t, err)
	require.Equal(t, sourceWrite.Msg.DocumentRevision, targetWrite.Msg.DocumentRevision)
	require.NotEmpty(t, targetWrite.Msg.GetTargetRevision())

	staleInterchangeSubject := "must not win"
	staleTargetRevision := targetState.TargetRevision
	err = db.Transaction(func(tx *gorm.DB) error {
		_, applyErr := ApplyTemplateTranslationInterchange(
			ctx, tx, store, references, template.Msg.Id, "en",
			TemplateTranslationInterchangeMutation{
				TargetLocale: "ko", ExpectedDocumentRevision: sourceWrite.Msg.DocumentRevision,
				ExpectedTargetRevision: &staleTargetRevision, ExpectedPresence: true,
				Subject: &staleInterchangeSubject, ContributorMemberID: contributorID, Now: time.Now().UTC(),
			},
		)
		return applyErr
	})
	var targetConflict *translation.TargetRevisionConflict
	require.ErrorAs(t, err, &targetConflict)
	afterStaleInterchange, err := loadEmailTemplateExactLocaleState(
		ctx, db, store, template.Msg.Id, documentID, "ko", false,
	)
	require.NoError(t, err)
	require.Equal(t, targetWrite.Msg.GetTargetRevision(), afterStaleInterchange.TargetRevision)

	var audit struct {
		ActorKind     string `gorm:"column:actor_kind"`
		ActorMemberID string `gorm:"column:actor_member_id"`
		Attributes    []byte
	}
	require.NoError(t, db.Raw(`
		SELECT actor_kind, actor_member_id::text AS actor_member_id, attributes
		FROM domain_audit
		WHERE action = ? AND target_type = 'email_template' AND target_id = ?
		  AND attributes @> '{"changed_fields":["locale_content"],"locale":"ko"}'::jsonb
		ORDER BY occurred_at DESC, audit_id DESC
		LIMIT 1`, sharedtelemetry.AuditEmailTemplateUpdated, template.Msg.Id).
		Scan(&audit).Error)
	require.Equal(t, "member", audit.ActorKind)
	require.Equal(t, contributorID, audit.ActorMemberID)

	targetSubject := "Provider subject"
	candidate := &translation.Candidate{
		Title:                   &targetSubject,
		ContentDocumentRevision: sourceWrite.Msg.DocumentRevision,
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{
			Locale: "ko",
		},
		ContentBlockLocaleDeletes: []string{blockID},
	}
	job := &model.TranslationJob{
		EntityType: emailTemplateContentEntity, EntityID: template.Msg.Id,
		SourceLocale: "en", TargetLocale: "ko",
		RequestedByMemberID: contributorID,
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return ApplyTemplateTranslationCandidate(
			ctx, tx, store, references, job, candidate,
			translation.EntryWrite{Title: &targetSubject, Now: time.Now().UTC()},
			apitelemetry.NewDurableWriter(db),
		)
	}))

	afterProvider, err := LoadTemplateTranslationSourceDocument(ctx, db, store, template.Msg.Id)
	require.NoError(t, err)
	require.Equal(t, sourceWrite.Msg.DocumentRevision, afterProvider.ContentDocumentRevision)
	audit = struct {
		ActorKind     string `gorm:"column:actor_kind"`
		ActorMemberID string `gorm:"column:actor_member_id"`
		Attributes    []byte
	}{}
	require.NoError(t, db.Raw(`
		SELECT actor_kind, actor_member_id::text AS actor_member_id, attributes
		FROM domain_audit
		WHERE action = ? AND target_type = 'email_template' AND target_id = ?
		  AND attributes @> '{"changed_fields":["locale_content"],"locale":"ko"}'::jsonb
		ORDER BY occurred_at DESC, audit_id DESC
		LIMIT 1`, sharedtelemetry.AuditEmailTemplateUpdated, template.Msg.Id).
		Scan(&audit).Error)
	require.Equal(t, "member", audit.ActorKind)
	require.Equal(t, contributorID, audit.ActorMemberID)
	var targetLocaleBlockCount int64
	require.NoError(t, db.Table("content_block_locale").
		Where("block_id = ? AND locale = 'ko'", blockID).Count(&targetLocaleBlockCount).Error)
	require.Zero(t, targetLocaleBlockCount)
	var targetProjection string
	require.NoError(t, db.Table("email_template_translation").
		Select("content_html").Where("entity_id = ? AND locale = 'ko'", template.Msg.Id).
		Scan(&targetProjection).Error)
	require.Contains(t, targetProjection, "Source body")

	// A source edit must refresh the target's materialized fallback while
	// retaining the target-owned sparse overlay. The provider above deleted the
	// target value for this Block, so delivery must follow the new source text.
	updatedSource, err := internal.ApplyBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyEmailTemplateBlockBatchRequest{
		EmailTemplateId: template.Msg.Id, Locale: "en",
		Batch: emailTemplateTargetParagraphBatch(
			blockID, sourceWrite.Msg.DocumentRevision, "en", "Updated source body", []string{contributorID},
		),
	}))
	require.NoError(t, err)
	require.NotEqual(t, sourceWrite.Msg.DocumentRevision, updatedSource.Msg.DocumentRevision)
	require.NoError(t, db.Table("email_template_translation").
		Select("content_html").Where("entity_id = ? AND locale = 'ko'", template.Msg.Id).
		Scan(&targetProjection).Error)
	require.Contains(t, targetProjection, "Updated source body")
}

func emailTemplateTargetParagraphBatch(
	blockID string,
	expectedRevision string,
	locale string,
	text string,
	contributors []string,
) *contentv1.RichTextBlockMutationBatch {
	return &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL,
		ExpectedRevision:        expectedRevision,
		ContributorMemberIds:    contributors,
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: locale,
			Mutations: []*contentv1.RichTextBlockLocaleMutation{{
				Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{
					Upsert: &contentv1.UpsertRichTextBlockLocale{Block: &contentv1.RichTextBlockLocale{
						BlockId: blockID,
						Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
							Props: &contentv1.ParagraphLocaleProps{},
							Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
								Text: &contentv1.RichTextStyledText{Text: text},
							}}},
						}},
					}},
				},
			}},
		}},
	}
}
