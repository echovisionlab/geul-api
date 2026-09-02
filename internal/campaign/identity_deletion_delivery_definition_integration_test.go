//go:build integration

package campaign

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDeletedIdentityPreservesMemberExclusionAndFrozenDeliveryDefinitionIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	now := time.Now().UTC()
	includedIdentityID := uuid.NewString()
	excludedIdentityID := uuid.NewString()
	includedEmail := "included-" + uuid.NewString() + "@example.test"
	excludedEmail := "excluded-" + uuid.NewString() + "@example.test"
	for _, fixture := range []testutil.KratosIdentityFixture{
		{ID: includedIdentityID, Email: includedEmail, CreatedAt: now},
		{ID: excludedIdentityID, Email: excludedEmail, CreatedAt: now},
	} {
		testutil.SeedKratosIdentityFixture(t, db, fixture)
	}
	includedMemberID := seedCampaignActiveMemberEmailPair(t, db, includedIdentityID, includedEmail)
	excludedMemberID := seedCampaignActiveMemberEmailPair(t, db, excludedIdentityID, excludedEmail)

	segmentID := uuid.NewString()
	require.NoError(t, db.Create(&model.AudienceSegment{
		ID: segmentID, Name: "Frozen exclusion", SegmentType: managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER.String(),
		CreatedAt: now, UpdatedAt: &now,
	}).Error)
	require.NoError(t, db.Create(&model.AudienceSegmentExcludedMember{
		AudienceSegmentID: segmentID,
		MemberID:          excludedMemberID,
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
		TargetMode: model.CampaignTargetModeSegment, SegmentID: &segmentID,
		RecipientScope: campaignRecipientScopeAllMatchingUsers,
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
			ID: runID, RunKind: EmailDeliveryRunKindCampaign, CampaignID: &campaignID,
			Status: CampaignDeliveryRunStatusSending, ScheduledAt: now, StartedAt: &now,
			TemplateData: model.JSONFields{}, RenderSnapshot: renderSnapshot,
			SnapshotSchemaVersion:   CampaignDeliverySnapshotSchemaVersion,
			SourceCampaignUpdatedAt: &now,
			AudienceSegmentID:       &segmentID,
			TargetQueryVersion:      CampaignDeliveryTargetQueryVersion,
			TargetMode:              CampaignDeliveryTargetModeUsersByFilter,
			TargetRecipientScope:    campaignRecipientScopeAllMatchingUsers,
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
		RecipientContextType: BulkEmailContextAccountCurrent,
		Status:               CampaignDeliveryRecipientStatusPending,
		CreatedAt:            now, UpdatedAt: now,
	}).Error)

	var frozenBefore model.CampaignDeliveryRun
	require.NoError(t, db.First(&frozenBefore, "id = ?", runID).Error)
	require.NoError(t, db.Exec("DELETE FROM kratos.identities WHERE id = ?", excludedIdentityID).Error)

	var segmentExclusions int64
	require.NoError(t, db.Model(&model.AudienceSegmentExcludedMember{}).
		Where("audience_segment_id = ? AND member_id = ?", segmentID, excludedMemberID).
		Count(&segmentExclusions).Error)
	require.EqualValues(t, 1, segmentExclusions)
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
	require.Equal(t, CampaignDeliveryRecipientStatusPending, recipientAfter.Status)
	require.Equal(t, includedEmail, recipientAfter.NormalizedRecipientEmail)

	var frozenAfter model.CampaignDeliveryRun
	require.NoError(t, db.First(&frozenAfter, "id = ?", runID).Error)
	require.Equal(t, frozenBefore.DefinitionSealed, frozenAfter.DefinitionSealed)
	require.Equal(t, frozenBefore.RenderSnapshot, frozenAfter.RenderSnapshot)
	require.Equal(t, frozenBefore.Status, frozenAfter.Status)
	require.Equal(t, frozenBefore.TargetCount, frozenAfter.TargetCount)
}
