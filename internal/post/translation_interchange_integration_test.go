//go:build integration

package post_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	translationcore "github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type postInterchangeIntegrationAuditCapture struct {
	records []sharedtelemetry.AuditRecord
}

func (capture *postInterchangeIntegrationAuditCapture) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	capture.records = append(capture.records, record)
	return nil
}

func TestPostTranslationInterchangeUsesDerivedTargetRevisionAndStrictTimestampIntegration(t *testing.T) {
	db := testutil.NewPostIntegrationDB(t)
	adminIdentityID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, adminIdentityID, "Post interchange admin")
	spiceDB := testutil.PostIntegrationSpiceDB(t)
	testutil.GrantPostIntegrationRole(t, spiceDB, adminIdentityID, policyv1.Role.Admin())
	ctx := testutil.PostIntegrationContext(adminIdentityID)
	memberID := testutil.PostIntegrationMemberID(adminIdentityID)
	store := testutil.NewPostContentBlockStore(t)
	service := postintegration.NewPostDomainService(
		t, db, "", spiceDB,
		testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(adminIdentityID, "en")),
		store,
	)
	internal := postintegration.NewInternalPostDomainService(t, db, "", spiceDB, store)

	slug := "post-interchange-" + testutil.PostIntegrationUUID()
	created, err := service.CreatePost(ctx, connect.NewRequest(&managev1.CreatePostRequest{
		Title: "Post interchange", Slug: &slug, Document: testutil.EmptyPostDocument("en"),
	}))
	require.NoError(t, err)
	blockIDs := []string{
		testutil.PostIntegrationUUID(), testutil.PostIntegrationUUID(), testutil.PostIntegrationUUID(),
	}
	sourceWrite, err := internal.ApplyPostBlockBatch(t.Context(), connect.NewRequest(&intrav1.ApplyPostBlockBatchRequest{
		PostId: created.Msg.Id, Locale: "en",
		Batch: postParagraphBatch(
			created.Msg.Revision, memberID, "en", blockIDs, []string{"source first", "source second", ""}, true,
		),
	}))
	require.NoError(t, err)

	capture := &postInterchangeIntegrationAuditCapture{}
	interchange := postdomain.NewTranslationInterchange(capture)
	job := &model.TranslationJob{
		EntityType: "post", EntityID: created.Msg.Id, SourceLocale: "en", TargetLocale: "ko",
		RequestedByMemberID: memberID,
	}
	fixedNow := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	firstCandidate := &translationcore.Candidate{
		ContentDocumentRevision: sourceWrite.Msg.DocumentRevision,
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{
			Locale: "ko", Blocks: []*contentv1.RichTextBlockLocale{
				postParagraphLocaleBlock(blockIDs[0], "target first"),
			},
		},
	}
	var firstResult postdomain.TranslationInterchangeMutationResult
	require.NoError(t, db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		firstResult, err = interchange.ApplyCandidateWithDB(
			ctx, tx, store, job, firstCandidate, translationcore.EntryWrite{Now: fixedNow}, nil,
		)
		return err
	}))
	require.True(t, firstResult.Changed)
	require.NotEmpty(t, firstResult.Revision)
	requirePostTargetSeededFromSource(t, db, created.Msg.Id, "ko", len(blockIDs), len(blockIDs)-1, blockIDs[2])
	require.Len(t, capture.records, 1)

	adminSessionID := testutil.InsertPostIntegrationSession(t, db, adminIdentityID)
	firstLoaded := loadPostTargetRoom(t, internal, adminSessionID, created.Msg.Id, "ko")
	require.Equal(t, sourceWrite.Msg.DocumentRevision, firstLoaded.Msg.DocumentRevision)
	require.Equal(t, firstResult.Revision, *firstLoaded.Msg.TargetRevision)
	secondCandidate := &translationcore.Candidate{
		ContentDocumentRevision: sourceWrite.Msg.DocumentRevision,
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{
			Locale: "ko", Blocks: []*contentv1.RichTextBlockLocale{
				postParagraphLocaleBlock(blockIDs[1], "target second"),
			},
		},
	}
	var secondResult postdomain.TranslationInterchangeMutationResult
	require.NoError(t, db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		secondResult, err = interchange.ApplyCandidateWithDB(
			ctx,
			tx,
			store,
			job,
			secondCandidate,
			translationcore.EntryWrite{Now: fixedNow},
			&firstResult.Revision,
		)
		return err
	}))
	require.True(t, secondResult.Changed)
	require.NotEqual(t, firstResult.Revision, secondResult.Revision, "target updated_at must strictly advance")
	require.Len(t, capture.records, 2)
	requirePostTargetOverlayCount(t, db, created.Msg.Id, "ko", int64(len(blockIDs)))

	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, applyErr := interchange.ApplyCandidateWithDB(
			ctx,
			tx,
			store,
			job,
			secondCandidate,
			translationcore.EntryWrite{Now: fixedNow},
			&firstResult.Revision,
		)
		return applyErr
	})
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	secondLoaded := loadPostTargetRoom(t, internal, adminSessionID, created.Msg.Id, "ko")
	require.Equal(t, sourceWrite.Msg.DocumentRevision, secondLoaded.Msg.DocumentRevision)
	require.Equal(t, secondResult.Revision, *secondLoaded.Msg.TargetRevision)
	require.Equal(t, "target first", postLocalizedParagraphText(t, secondLoaded.Msg.Document, blockIDs[0]))
	require.Equal(t, "target second", postLocalizedParagraphText(t, secondLoaded.Msg.Document, blockIDs[1]))
}

func postParagraphLocaleBlock(blockID string, value string) *contentv1.RichTextBlockLocale {
	return &contentv1.RichTextBlockLocale{
		BlockId: blockID,
		Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
			Props: &contentv1.ParagraphLocaleProps{},
			Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
				Text: &contentv1.RichTextStyledText{Text: value},
			}}},
		}},
	}
}
