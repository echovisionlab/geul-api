//go:build integration

package campaign

import (
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSESProviderOutcomeUpdatesCampaignHistoryAndDeduplicatesSuppressionIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	now := time.Now().UTC()
	campaignID := uuid.NewString()
	runID := uuid.NewString()
	recipientID := uuid.NewString()
	providerMessageID := "ses-provider-" + uuid.NewString()
	recipientEmail := "ses-callback-" + uuid.NewString() + "@example.test"
	referenceID := providerMessageID
	identityID := uuid.NewString()
	renderSnapshotJSON, err := json.Marshal(CampaignDeliverySnapshot{
		Subject: "SES callback", ContentHTML: "<p>SES callback</p>", SourceLocale: "en",
		Translations: []CampaignDeliverySnapshotTranslation{{Locale: "en", Subject: "SES callback", ContentHTML: "<p>SES callback</p>"}},
	})
	require.NoError(t, err)
	var renderSnapshot model.JSONFields
	require.NoError(t, json.Unmarshal(renderSnapshotJSON, &renderSnapshot))

	campaignDocumentID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'email')`, campaignDocumentID).Error)
	require.NoError(t, db.Create(&model.Campaign{
		ID:                campaignID,
		ContentDocumentID: &campaignDocumentID,
		Name:              "SES callback campaign",
		Subject:           "SES callback campaign",
		Status:            "CAMPAIGN_STATUS_SENT",
		TargetMode:        model.CampaignTargetModeAll,
		RecipientScope:    campaignRecipientScopeAllMatchingUsers,
		CreatedAt:         now,
		UpdatedAt:         now,
	}).Error)
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: recipientEmail, Name: "SES callback recipient", CreatedAt: now,
	})
	memberID := seedCampaignActiveMemberEmailPair(t, db, identityID, recipientEmail)
	require.NoError(t, db.Create(&model.CampaignDeliveryRun{
		ID:                      runID,
		RunKind:                 EmailDeliveryRunKindCampaign,
		CampaignID:              &campaignID,
		Status:                  CampaignDeliveryRunStatusSent,
		ScheduledAt:             now,
		CompletedAt:             &now,
		TemplateData:            model.JSONFields{},
		RenderSnapshot:          renderSnapshot,
		SnapshotSchemaVersion:   1,
		DefinitionSealed:        true,
		SourceCampaignUpdatedAt: &now,
		TargetQueryVersion:      CampaignDeliveryTargetQueryVersion,
		TargetMode:              CampaignDeliveryTargetModeAllUsers,
		TargetRecipientScope:    campaignRecipientScopeAllMatchingUsers,
		TargetCount:             1,
		SentCount:               1,
		CreatedAt:               now,
		UpdatedAt:               now,
	}).Error)
	require.NoError(t, db.Create(&model.CampaignDeliveryRecipient{
		ID:                       recipientID,
		RunID:                    runID,
		RecipientEmail:           recipientEmail,
		NormalizedRecipientEmail: emailutil.NormalizeAddressForDelivery(recipientEmail),
		IdentityID:               &identityID,
		MemberID:                 &memberID,
		RecipientContextType:     BulkEmailContextAccountCurrent,
		Status:                   CampaignDeliveryRecipientStatusSent,
		ProviderMessageID:        &providerMessageID,
		TerminalAt:               &now,
		CreatedAt:                now,
		UpdatedAt:                now,
	}).Error)

	deliveryAt := now.Add(time.Minute)
	result, err := ApplySESProviderOutcome(t.Context(), db, providerMessageID, SESProviderOutcomeDelivered, deliveryAt, "")
	require.NoError(t, err)
	require.Equal(t, 1, result.UpdatedRecipients)
	require.Equal(t, []string{emailutil.NormalizeAddressForDelivery(recipientEmail)}, result.MatchedRecipientEmails)

	complaintAt := deliveryAt.Add(time.Minute)
	result, err = ApplySESProviderOutcome(t.Context(), db, providerMessageID, SESProviderOutcomeComplained, complaintAt, "complaint")
	require.NoError(t, err)
	require.Equal(t, 1, result.UpdatedRecipients)
	require.NoError(t, emaildelivery.SuppressEmailAddress(
		t.Context(), db, recipientEmail,
		emaildelivery.EmailSuppressionReasonSESComplaint,
		emaildelivery.EmailSuppressionSourceSESCallback,
		&referenceID, "complaint",
	))
	var firstSuppression model.EmailSuppression
	require.NoError(t, db.Where("LOWER(email) = ? AND released_at IS NULL", emailutil.NormalizeAddressForDelivery(recipientEmail)).First(&firstSuppression).Error)

	result, err = ApplySESProviderOutcome(t.Context(), db, providerMessageID, SESProviderOutcomeComplained, complaintAt.Add(time.Minute), "complaint")
	require.NoError(t, err)
	require.Zero(t, result.UpdatedRecipients)
	require.NoError(t, emaildelivery.SuppressEmailAddress(
		t.Context(), db, recipientEmail,
		emaildelivery.EmailSuppressionReasonSESComplaint,
		emaildelivery.EmailSuppressionSourceSESCallback,
		&referenceID, "complaint",
	))
	var duplicateSuppression model.EmailSuppression
	require.NoError(t, db.First(&duplicateSuppression, "id = ?", firstSuppression.ID).Error)
	require.Equal(t, firstSuppression.SuppressedAt, duplicateSuppression.SuppressedAt)

	require.NoError(t, emaildelivery.SuppressEmailAddress(
		t.Context(), db, recipientEmail,
		emaildelivery.EmailSuppressionReasonSESBounce,
		emaildelivery.EmailSuppressionSourceSESCallback,
		&referenceID, "late permanent bounce",
	))
	var afterLateBounce model.EmailSuppression
	require.NoError(t, db.First(&afterLateBounce, "id = ?", firstSuppression.ID).Error)
	require.Equal(t, emaildelivery.EmailSuppressionReasonSESComplaint, afterLateBounce.Reason)
	require.Equal(t, firstSuppression.SuppressedAt, afterLateBounce.SuppressedAt)

	var recipient model.CampaignDeliveryRecipient
	require.NoError(t, db.First(&recipient, "id = ?", recipientID).Error)
	require.Equal(t, CampaignDeliveryRecipientStatusComplained, recipient.Status)
	require.Equal(t, "complaint", ptrStringValue(recipient.ErrorType))
	require.WithinDuration(t, complaintAt, *recipient.TerminalAt, time.Millisecond)

	var run model.CampaignDeliveryRun
	require.NoError(t, db.First(&run, "id = ?", runID).Error)
	require.Zero(t, run.SentCount)
	require.Equal(t, 1, run.FailedCount)

	adminContext, spiceDB := testutil.IntegrationAdminContext(t, db)
	_, err = emaildelivery.NewEmailSuppressionService(db, spiceDB).ReleaseEmailSuppression(
		adminContext, connect.NewRequest(&managev1.ReleaseEmailSuppressionRequest{Email: recipientEmail}),
	)
	require.NoError(t, err)
	require.NoError(t, emaildelivery.SuppressEmailAddress(
		t.Context(), db, recipientEmail,
		emaildelivery.EmailSuppressionReasonSESComplaint,
		emaildelivery.EmailSuppressionSourceSESCallback,
		&referenceID, "replayed complaint",
	))
	var activeSuppressionCount int64
	require.NoError(t, db.Model(&model.EmailSuppression{}).
		Where("LOWER(email) = ? AND released_at IS NULL", emailutil.NormalizeAddressForDelivery(recipientEmail)).
		Count(&activeSuppressionCount).Error)
	require.Zero(t, activeSuppressionCount)
}
