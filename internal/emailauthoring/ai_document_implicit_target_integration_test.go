//go:build integration

package emailauthoring

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestEmailTemplateAIDocumentImplicitTargetWriteIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	baseContext, spiceDB := testutil.IntegrationAdminContext(t, db)
	admin := auth.GetUser(baseContext)
	require.NotNil(t, admin)
	ctx := testutil.NewAuditContext(t, string(admin.IdentityID), string(admin.MemberID))
	store := testutil.NewEmailContentBlockStore(t, spiceDB)
	references := integrationCampaignDeliveryReferences{}
	owner := NewAuditedInternalEmailTemplateService(
		db, apitelemetry.NewDurableWriter(db), spiceDB,
		WithInternalEmailTemplateContentBlockStore(store),
		WithInternalEmailTemplateCheckpoints(testcollaboration.NewCheckpoints(db, spiceDB)),
		WithInternalEmailTemplateCampaignDeliveryReferences(references),
	)
	application, err := NewAIDocumentService(owner)
	require.NoError(t, err)

	for _, test := range []struct {
		name       string
		setSubject bool
		setBody    bool
	}{
		{name: "subject", setSubject: true},
		{name: "body", setBody: true},
		{name: "subject and body", setSubject: true, setBody: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			template, err := NewEmailTemplateService(
				db, nil, emailTemplateRuntimeFixture{}, "", "", spiceDB,
				WithEmailTemplateContentBlockStore(store),
				WithEmailTemplateCampaignDeliveryReferences(references),
			).CreateEmailTemplate(ctx, connect.NewRequest(&managev1.CreateEmailTemplateRequest{
				Key:  "implicit_target_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
				Name: "Implicit target Template", Subject: "Source subject", SourceLocale: "en",
			}))
			require.NoError(t, err)
			_, err = owner.ApplyBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyEmailTemplateBlockBatchRequest{
				EmailTemplateId: template.Msg.Id, Locale: "en",
				Batch: testutil.NewParagraphBatch(template.Msg.Document, template.Msg.DocumentRevision,
					"en", "Source body", []string{admin.MemberID.String()}),
			}))
			require.NoError(t, err)
			source, err := application.Load(ctx, template.Msg.Id, "en")
			require.NoError(t, err)
			missing, err := application.Load(ctx, template.Msg.Id, "ko")
			require.NoError(t, err)
			require.False(t, missing.LocaleExists)
			require.Nil(t, missing.TargetRevision)
			blockID := source.Document.GetLocaleOverlay().GetBlocks()[0].GetBlockId()
			batch := contentblock.Batch{
				DocumentID: missing.DocumentID, ExpectedRevision: uuid.MustParse(missing.DocumentRevision),
				ContributorMemberIDs: []uuid.UUID{uuid.MustParse(missing.ViewerMemberID)},
			}
			if test.setBody {
				batch, err = contentblock.BatchFromRichTextProto(missing.DocumentID,
					emailTemplateTargetParagraphBatch(blockID, missing.DocumentRevision, "ko", "Target body",
						[]string{missing.ViewerMemberID}))
				require.NoError(t, err)
			}
			mutation := AIDocumentMutation{
				TemplateID: missing.TemplateID, Locale: missing.Locale,
				ExpectedDocumentRevision: missing.DocumentRevision, ExpectedSource: missing.SourceLocale,
				ExpectedPresence: false, ContributorMember: uuid.MustParse(missing.ViewerMemberID),
				Batch: &batch, SetSubject: test.setSubject, Subject: "Target subject",
			}
			compiler := func(AIDocumentState) (AIDocumentMutation, error) { return mutation, nil }
			validated, err := application.ExecuteAIDocumentMutation(
				ctx, template.Msg.Id, "ko", AIDocumentExecutionValidate, compiler)
			require.NoError(t, err)
			require.True(t, validated.Changed)
			require.Equal(t, source.DocumentRevision, validated.DocumentRevision)
			require.NotNil(t, validated.TargetRevision)
			afterValidate, err := application.Load(ctx, template.Msg.Id, "ko")
			require.NoError(t, err)
			require.False(t, afterValidate.LocaleExists)
			require.Nil(t, afterValidate.TargetRevision)
			require.True(t, proto.Equal(missing.Document, afterValidate.Document))

			accepted, err := application.ExecuteAIDocumentMutation(
				ctx, template.Msg.Id, "ko", AIDocumentExecutionApply, compiler)
			require.NoError(t, err)
			require.True(t, accepted.Changed)
			require.Equal(t, source.DocumentRevision, accepted.DocumentRevision)
			require.NotNil(t, accepted.TargetRevision)
			require.NotEmpty(t, *accepted.TargetRevision)
			target, err := application.Load(ctx, template.Msg.Id, "ko")
			require.NoError(t, err)
			require.True(t, target.LocaleExists)
			require.Equal(t, accepted.TargetRevision, target.TargetRevision)
			wantSubject := "Source subject"
			if test.setSubject {
				wantSubject = "Target subject"
			}
			require.NotNil(t, target.Subject)
			require.Equal(t, wantSubject, *target.Subject)
			wantBody := "Source body"
			if test.setBody {
				wantBody = "Target body"
			}
			targetBlocks := target.Document.GetLocaleOverlay().GetBlocks()
			require.Len(t, targetBlocks, 1)
			require.Equal(t, blockID, targetBlocks[0].GetBlockId())
			require.Equal(t, wantBody, targetBlocks[0].GetParagraph().GetContent()[0].GetText().GetText())
			sourceAfter, err := application.Load(ctx, template.Msg.Id, "en")
			require.NoError(t, err)
			require.Equal(t, source.DocumentRevision, sourceAfter.DocumentRevision)
			require.Equal(t, source.Subject, sourceAfter.Subject)
			require.True(t, proto.Equal(source.Document, sourceAfter.Document))

			_, err = application.ExecuteAIDocumentMutation(
				ctx, template.Msg.Id, "ko", AIDocumentExecutionApply, compiler)
			var conflict *AIDocumentRevisionConflictError
			require.ErrorAs(t, err, &conflict)
			require.Equal(t, accepted.DocumentRevision, conflict.CurrentDocumentRevision)
			require.Equal(t, accepted.TargetRevision, conflict.CurrentTargetRevision)
			afterStale, err := application.Load(ctx, template.Msg.Id, "ko")
			require.NoError(t, err)
			require.Equal(t, target.TargetRevision, afterStale.TargetRevision)
			require.True(t, proto.Equal(target.Document, afterStale.Document))
		})
	}
}
