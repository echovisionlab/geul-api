//go:build integration

package campaignpostgres

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/model"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDeletedIdentityPreservesMemberExclusionAndFrozenDeliveryDefinitionIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true, BootstrapKratosStub: true}).DB
	now := time.Now().UTC()
	includedIdentityID := uuid.NewString()
	excludedIdentityID := uuid.NewString()
	includedEmail := "included-" + uuid.NewString() + "@example.test"
	excludedEmail := "excluded-" + uuid.NewString() + "@example.test"
	for _, fixture := range []orytest.KratosIdentityFixture{
		{ID: includedIdentityID, Email: includedEmail, CreatedAt: now},
		{ID: excludedIdentityID, Email: excludedEmail, CreatedAt: now},
	} {
		orytest.SeedKratosIdentityFixture(t, db, fixture)
	}
	includedMemberID := seedCampaignActiveMemberEmailPair(t, db, includedIdentityID, includedEmail)
	excludedMemberID := seedCampaignActiveMemberEmailPair(t, db, excludedIdentityID, excludedEmail)

	criteriaID := uuid.NewString()
	require.NoError(t, db.Create(&model.MemberCriteria{
		ID: criteriaID, Name: "Frozen exclusion", Kind: model.MemberCriteriaKindFilter,
		CreatedAt: now, UpdatedAt: &now,
	}).Error)
	require.NoError(t, db.Create(&model.MemberCriteriaExcludedMember{
		MemberCriteriaID: criteriaID,
		MemberID:         excludedMemberID,
	}).Error)

	campaignID := uuid.NewString()
	campaignDocumentID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile) VALUES (?, 'email')`,
		campaignDocumentID,
	).Error)
	require.NoError(t, db.Create(&model.Campaign{
		ID: campaignID, ContentDocumentID: &campaignDocumentID,
		Name: "Frozen identity campaign", Subject: "Frozen identity campaign",
		Status:     managev1.CampaignStatus_CAMPAIGN_STATUS_SENDING.String(),
		TargetMode: model.CampaignTargetModeMemberCriteria, CriteriaID: &criteriaID,
		RecipientScope: "ALL_MATCHING_USERS",
		CreatedAt:      now, UpdatedAt: now,
	}).Error)
	runID := uuid.NewString()
	renderSnapshot := model.JSONFields{
		"subject": "Frozen", "content_html": "<p>Frozen</p>", "source_locale": "en",
		"translations": []any{map[string]any{
			"locale": "en", "subject": "Frozen", "content_html": "<p>Frozen</p>",
		}},
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model.CampaignDeliveryRun{
			ID: runID, RunKind: campaign.EmailDeliveryRunKindCampaign, CampaignID: &campaignID,
			Status: campaign.CampaignDeliveryRunStatusSending, ScheduledAt: now, StartedAt: &now,
			TemplateData: model.JSONFields{}, RenderSnapshot: renderSnapshot,
			SnapshotSchemaVersion:   campaign.CampaignDeliverySnapshotSchemaVersion,
			SourceCampaignUpdatedAt: &now,
			MemberCriteriaID:        &criteriaID,
			TargetQueryVersion:      campaign.CampaignDeliveryTargetQueryVersion,
			TargetMode:              campaign.CampaignDeliveryTargetModeUsersByFilter,
			TargetRecipientScope:    "ALL_MATCHING_USERS",
			TargetCount:             1, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.EmailDeliveryRunTargetExcludedMember{
			RunID: runID, IdentityID: excludedIdentityID, MemberID: excludedMemberID,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.CampaignDeliveryRun{}).
			Where("id = ? AND definition_sealed = FALSE", runID).
			Update("definition_sealed", true).Error
	}))
	recipientID := uuid.NewString()
	require.NoError(t, db.Create(&model.CampaignDeliveryRecipient{
		ID: recipientID, RunID: runID,
		RecipientEmail: includedEmail, NormalizedRecipientEmail: includedEmail,
		IdentityID: &includedIdentityID, MemberID: &includedMemberID,
		RecipientContextType: campaign.BulkEmailContextAccountCurrent,
		Status:               campaign.CampaignDeliveryRecipientStatusPending,
		CreatedAt:            now, UpdatedAt: now,
	}).Error)

	var frozenBefore model.CampaignDeliveryRun
	require.NoError(t, db.First(&frozenBefore, "id = ?", runID).Error)
	require.NoError(t, db.Exec("DELETE FROM kratos.identities WHERE id = ?", excludedIdentityID).Error)

	var criteriaExclusions int64
	require.NoError(t, db.Model(&model.MemberCriteriaExcludedMember{}).
		Where("member_criteria_id = ? AND member_id = ?", criteriaID, excludedMemberID).
		Count(&criteriaExclusions).Error)
	require.EqualValues(t, 1, criteriaExclusions)
	var runExclusions int64
	require.NoError(t, db.Model(&model.EmailDeliveryRunTargetExcludedMember{}).
		Where("run_id = ? AND member_id = ?", runID, excludedMemberID).
		Count(&runExclusions).Error)
	require.EqualValues(t, 1, runExclusions)

	require.NoError(t, db.Exec("DELETE FROM kratos.identities WHERE id = ?", includedIdentityID).Error)
	var recipientAfter model.CampaignDeliveryRecipient
	require.NoError(t, db.First(&recipientAfter, "id = ?", recipientID).Error)
	require.Equal(t, includedIdentityID, ptrStringValue(recipientAfter.IdentityID))
	require.Equal(t, includedMemberID, ptrStringValue(recipientAfter.MemberID))
	require.Equal(t, campaign.CampaignDeliveryRecipientStatusPending, recipientAfter.Status)
	require.Equal(t, includedEmail, recipientAfter.NormalizedRecipientEmail)

	var frozenAfter model.CampaignDeliveryRun
	require.NoError(t, db.First(&frozenAfter, "id = ?", runID).Error)
	require.Equal(t, frozenBefore.DefinitionSealed, frozenAfter.DefinitionSealed)
	require.Equal(t, frozenBefore.RenderSnapshot, frozenAfter.RenderSnapshot)
	require.Equal(t, frozenBefore.Status, frozenAfter.Status)
	require.Equal(t, frozenBefore.TargetCount, frozenAfter.TargetCount)
}

func seedCampaignActiveMemberEmailPair(
	t *testing.T,
	db *gorm.DB,
	identityID string,
	email string,
) string {
	t.Helper()
	memberID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(
		"UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid",
		memberID,
		identityID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO account_identity (id, created_at)
		 SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid
		 ON CONFLICT (id) DO NOTHING`,
		identityID,
	).Error)
	require.NoError(t, db.Create(&model.Member{
		ID: memberID, AccountIdentityID: &identityID, Nickname: "Campaign recipient " + memberID,
		Onboarded: true, PrimaryEmail: &email, AvailableEmails: []string{email},
		SocialLinks: map[string]string{}, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return memberID
}
