//go:build integration

package campaign

import (
	"encoding/json"
	"testing"
	"time"

	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSESProviderOutcomeUpdatesCampaignHistoryIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	now := time.Now().UTC()
	campaignID := uuid.NewString()
	runID := uuid.NewString()
	recipientID := uuid.NewString()
	providerMessageID := "ses-provider-" + uuid.NewString()
	recipientEmail := "ses-callback-" + uuid.NewString() + "@example.test"
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
	result, err := applySESProviderOutcome(t.Context(), db, providerMessageID, SESProviderOutcomeDelivered, deliveryAt, "")
	require.NoError(t, err)
	require.Equal(t, 1, result.UpdatedRecipients)
	require.Equal(t, []string{emailutil.NormalizeAddressForDelivery(recipientEmail)}, result.MatchedRecipientEmails)

	complaintAt := deliveryAt.Add(time.Minute)
	result, err = applySESProviderOutcome(t.Context(), db, providerMessageID, SESProviderOutcomeComplained, complaintAt, "complaint")
	require.NoError(t, err)
	require.Equal(t, 1, result.UpdatedRecipients)
	result, err = applySESProviderOutcome(t.Context(), db, providerMessageID, SESProviderOutcomeComplained, complaintAt.Add(time.Minute), "complaint")
	require.NoError(t, err)
	require.Zero(t, result.UpdatedRecipients)

	var recipient model.CampaignDeliveryRecipient
	require.NoError(t, db.First(&recipient, "id = ?", recipientID).Error)
	require.Equal(t, CampaignDeliveryRecipientStatusComplained, recipient.Status)
	require.Equal(t, "complaint", ptrStringValue(recipient.ErrorType))
	require.WithinDuration(t, complaintAt, *recipient.TerminalAt, time.Millisecond)

	var run model.CampaignDeliveryRun
	require.NoError(t, db.First(&run, "id = ?", runID).Error)
	require.Zero(t, run.SentCount)
	require.Equal(t, 1, run.FailedCount)
}
