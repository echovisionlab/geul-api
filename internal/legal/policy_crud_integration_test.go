//go:build integration

package legal_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestLegalPolicyDeletionWithSentHistoryInEveryLifecycleIntegration(t *testing.T) {
	for _, kind := range []string{"terms", "privacy"} {
		for _, lifecycle := range []string{"DRAFT", "SCHEDULED", "ACTIVE", "ARCHIVED"} {
			t.Run(kind+"/"+lifecycle, func(t *testing.T) {
				db := newLegalIntegrationDB(t)
				updateEvent, effectiveEvent := email.EventTermsUpdate.String(), email.EventTermsEffective.String()
				if kind == "privacy" {
					updateEvent, effectiveEvent = email.EventPrivacyUpdate.String(), email.EventPrivacyEffective.String()
				}
				seedLegalDeliveryTemplateIntegration(t, db, updateEvent)
				seedLegalDeliveryTemplateIntegration(t, db, effectiveEvent)
				ctx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
				user := auth.GetUser(ctx)
				store := newLegalLifecycleContentBlockStore(t, spiceDB)
				terms := newTermsServiceForLegalIntegrationTest(db, "https://example.com", "https://cdn.example.com", spiceDB, legaldomain.WithTermsContentBlockStore(store))
				privacy := newPrivacyServiceForLegalIntegrationTest(db, "https://example.com", "https://cdn.example.com", spiceDB, legaldomain.WithPrivacyContentBlockStore(store))
				var id, revision string
				var version int32
				create := func() {
					if kind == "terms" {
						response, err := terms.CreateTermsVersion(ctx, connect.NewRequest(&managev1.CreateTermsVersionRequest{Document: legalPolicyDocumentFixture("en", "CRUD terms")}))
						require.NoError(t, err)
						id, revision, version = response.Msg.Id, response.Msg.Revision, response.Msg.Version
					} else {
						response, err := privacy.CreatePrivacyVersion(ctx, connect.NewRequest(&managev1.CreatePrivacyVersionRequest{Document: legalPolicyDocumentFixture("en", "CRUD privacy")}))
						require.NoError(t, err)
						id, revision, version = response.Msg.Id, response.Msg.Revision, response.Msg.Version
					}
				}
				create()
				requireRelationWhereCount(t, db, "email_delivery_run", kind+"_id = ?", 0, id)
				if kind == "terms" {
					_, err := terms.ScheduleTerms(ctx, connect.NewRequest(&managev1.ScheduleTermsRequest{Id: id, ExpectedRevision: revision, EffectiveFrom: timestamppb.New(time.Now().Add(24 * time.Hour))}))
					require.NoError(t, err)
				} else {
					_, err := privacy.SchedulePrivacy(ctx, connect.NewRequest(&managev1.SchedulePrivacyRequest{Id: id, ExpectedRevision: revision, EffectiveFrom: timestamppb.New(time.Now().Add(24 * time.Hour))}))
					require.NoError(t, err)
				}
				var sent model.CampaignDeliveryRun
				require.NoError(t, db.Where(kind+"_id = ? AND template_event_key = ?", id, updateEvent).Take(&sent).Error)
				require.NoError(t, db.Model(&sent).Updates(structured.Fields{"status": "sent", "sent_count": 1, "completed_at": time.Now().UTC()}).Error)
				if kind == "terms" {
					_, err := terms.ActivateTermsNow(ctx, connect.NewRequest(&managev1.ActivateTermsNowRequest{Id: id, ExpectedRevision: revision}))
					require.NoError(t, err)
				} else {
					_, err := privacy.ActivatePrivacyNow(ctx, connect.NewRequest(&managev1.ActivatePrivacyNowRequest{Id: id, ExpectedRevision: revision}))
					require.NoError(t, err)
				}
				var pending model.CampaignDeliveryRun
				require.NoError(t, db.Where(kind+"_id = ? AND template_event_key = ?", id, effectiveEvent).Take(&pending).Error)
				require.NoError(t, db.Model(&pending).Updates(structured.Fields{"status": "sending", "started_at": time.Now().UTC(), "target_count": 1}).Error)
				recipient := model.CampaignDeliveryRecipient{
					ID: uuid.NewString(), RunID: pending.ID, MemberID: ptrString(user.MemberID.String()), IdentityID: ptrString(user.IdentityID.String()),
					RecipientEmail: "policy-crud@example.com", NormalizedRecipientEmail: "policy-crud@example.com",
					RecipientContextType: "account_current", Status: "pending",
				}
				require.NoError(t, db.Create(&recipient).Error)
				require.NoError(t, db.Table(kind+"_history").Where("id = ?", id).Update("status", kindStatus(kind, lifecycle)).Error)
				var root struct{ ContentDocumentID string }
				require.NoError(t, db.Table(kind+"_history").Select("content_document_id").Where("id = ?", id).Take(&root).Error)
				if kind == "terms" {
					_, err := terms.DeleteTerms(ctx, connect.NewRequest(&managev1.DeleteTermsRequest{Id: id, ExpectedRevision: revision}))
					require.NoError(t, err)
				} else {
					_, err := privacy.DeletePrivacy(ctx, connect.NewRequest(&managev1.DeletePrivacyRequest{Id: id, ExpectedRevision: revision}))
					require.NoError(t, err)
				}
				requireRelationWhereCount(t, db, kind+"_history", "id = ?", 0, id)
				requireRelationWhereCount(t, db, "content_document", "id = ?", 0, root.ContentDocumentID)
				requireRelationWhereCount(t, db, "email_delivery_run", kind+"_id = ?", 2, id)
				var history model.CampaignDeliveryRun
				require.NoError(t, db.First(&history, "id = ?", sent.ID).Error)
				require.Equal(t, "sent", history.Status)
				require.Equal(t, 1, history.SentCount)
				require.Equal(t, sent.RenderSnapshot, history.RenderSnapshot)
				require.True(t, history.DefinitionSealed)
				history = model.CampaignDeliveryRun{}
				require.NoError(t, db.First(&history, "id = ?", pending.ID).Error)
				require.Equal(t, "cancelled", history.Status)
				require.Equal(t, 1, history.SkippedCount)
				require.NoError(t, db.First(&recipient, "id = ?", recipient.ID).Error)
				require.Equal(t, "skipped", recipient.Status)
				require.Equal(t, "policy_deleted", *recipient.ErrorType)
				require.NotNil(t, recipient.TerminalAt)
				deletedVersion := version
				create()
				require.Greater(t, version, deletedVersion, "a mailed version number must not be reused after deletion")
			})
		}
	}
}

func kindStatus(kind, lifecycle string) string {
	if kind == "terms" {
		return "TERMS_STATUS_" + lifecycle
	}
	return "PRIVACY_STATUS_" + lifecycle
}
