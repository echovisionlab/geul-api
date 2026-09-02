package campaign

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestCampaignTranslationJobApplyFenceUsesRootExistenceOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE campaign (
			id TEXT PRIMARY KEY,
			content_document_id TEXT,
			source_locale TEXT NOT NULL,
			status TEXT NOT NULL
		)
	`).Error)

	for _, status := range []string{
		managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
		managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String(),
		managev1.CampaignStatus_CAMPAIGN_STATUS_SENDING.String(),
		managev1.CampaignStatus_CAMPAIGN_STATUS_SENT.String(),
		managev1.CampaignStatus_CAMPAIGN_STATUS_FAILED.String(),
	} {
		t.Run(status, func(t *testing.T) {
			campaignID := uuid.NewString()
			documentID := uuid.New()
			require.NoError(t, db.Exec(
				"INSERT INTO campaign (id, content_document_id, source_locale, status) VALUES (?, ?, ?, ?)",
				campaignID, documentID, "en", status,
			).Error)

			domain, err := campaignTranslationJobApplyFence(
				campaignContentEntity,
				campaignID,
			)(t.Context(), db, documentID)
			require.NoError(t, err)
			require.Equal(t, "en", domain.SourceLocale)

			if status != managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String() {
				_, editErr := campaignEmailContentFence(
					campaignContentEntity,
					campaignID,
				)(t.Context(), db, documentID)
				require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(editErr))
			}
		})
	}

	deletedID := uuid.NewString()
	deletedDocumentID := uuid.New()
	require.NoError(t, db.Exec(
		"INSERT INTO campaign (id, content_document_id, source_locale, status) VALUES (?, ?, ?, ?)",
		deletedID, deletedDocumentID, "en", managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
	).Error)
	require.NoError(t, db.Exec("DELETE FROM campaign WHERE id = ?", deletedID).Error)
	_, err = campaignTranslationJobApplyFence(
		campaignContentEntity,
		deletedID,
	)(t.Context(), db, deletedDocumentID)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
