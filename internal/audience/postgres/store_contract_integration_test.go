//go:build integration

package postgres_test

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/audience/audiencetest"
	audiencepostgres "github.com/echovisionlab/geul-api/internal/audience/postgres"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestPostgresStoreKeepsTheStoreContractIntegration(t *testing.T) {
	t.Parallel()
	audiencetest.RunStoreContract(t, func(t *testing.T) audiencetest.Harness {
		db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
		return audiencetest.Harness{
			Harness:  mutationtest.PostgresHarness(db, audiencepostgres.NewUnits(db, campaign.SegmentUsage{})),
			Fixtures: postgresFixtures{db: db},
		}
	})
}

// postgresFixtures seeds the rows other domains own with SQL.
type postgresFixtures struct{ db *gorm.DB }

var campaignStatuses = map[string]model.CampaignStatus{
	audiencetest.CampaignDraft:     model.CampaignStatusDraft,
	audiencetest.CampaignScheduled: model.CampaignStatusScheduled,
	audiencetest.CampaignSending:   model.CampaignStatusSending,
}

func (f postgresFixtures) MemberTag(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, f.db.Exec(`INSERT INTO user_tag (id, name, created_at) VALUES (?, ?, ?)`, id, "Tag "+id, time.Now().UTC()).Error)
	return id
}

func (f postgresFixtures) Member(t *testing.T, eligible bool) string {
	t.Helper()
	identityID := uuid.NewString()
	memberID := testutil.SeedPostIntegrationIdentity(t, f.db, identityID, "Audience Member "+identityID)
	if !eligible {
		require.NoError(t, f.db.Exec(`UPDATE kratos.identities SET external_id = NULL WHERE id = ?::uuid`, identityID).Error)
	}
	return memberID
}

func (f postgresFixtures) Campaign(t *testing.T, segmentID, status string) string {
	t.Helper()
	now := time.Now().UTC()
	documentID := uuid.NewString()
	row := model.Campaign{
		ID: uuid.NewString(), Name: "Audience campaign", Subject: "Audience campaign",
		TargetMode: model.CampaignTargetModeSegment, SegmentID: &segmentID, ContentDocumentID: &documentID,
		Status: string(model.CampaignStatusDraft), RecipientScope: "SUBSCRIBED_USERS", CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO content_document (id, profile) VALUES (?::uuid, 'email')`, documentID).Error; err != nil {
			return err
		}
		return tx.Create(&row).Error
	}))
	if status != audiencetest.CampaignDraft {
		updates := map[string]any{"status": string(campaignStatuses[status])}
		if status == audiencetest.CampaignScheduled {
			updates["scheduled_at"] = now.Add(time.Hour)
		}
		require.NoError(t, f.db.Model(&model.Campaign{}).Where("id = ?", row.ID).Updates(updates).Error)
	}
	return row.ID
}

func (f postgresFixtures) DeliveryRun(t *testing.T, campaignID, segmentID, status string) string {
	t.Helper()
	now := time.Now().UTC()
	var source struct {
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	require.NoError(t, f.db.Table("campaign").Select("updated_at").Where("id = ?", campaignID).Take(&source).Error)
	run := model.CampaignDeliveryRun{
		ID: uuid.NewString(), RunKind: "campaign", CampaignID: &campaignID, Status: status, ScheduledAt: now,
		RenderSnapshot: model.JSONFields{
			"subject": "Audience", "content_html": "<p>Audience</p>", "source_locale": "en",
			"translations": []any{model.JSONFields{"locale": "en", "subject": "Audience", "content_html": "<p>Audience</p>"}},
		},
		SnapshotSchemaVersion: 1, DefinitionSealed: true, SourceCampaignUpdatedAt: &source.UpdatedAt,
		AudienceSegmentID: &segmentID, TargetQueryVersion: 2, TargetMode: "users_by_filter",
		TargetRecipientScope: "SUBSCRIBED_USERS", CreatedAt: now, UpdatedAt: now,
	}
	if status == audiencetest.RunSending {
		run.StartedAt = &now
	}
	require.NoError(t, f.db.Create(&run).Error)
	return run.ID
}

func (f postgresFixtures) file(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, f.db.Exec(
		`INSERT INTO file (id, file_name, mime_type, file_size, extension) VALUES (?, ?, 'audio/wav', 1, 'wav')`, id, "audience-"+id,
	).Error)
	return id
}

func (f postgresFixtures) AttachmentPolicy(t *testing.T, segmentIDs ...string) audience.DownloadPolicy {
	t.Helper()
	fileID := f.file(t)
	documentID, postID, blockID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		for _, statement := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'post', ?::uuid)`, []any{documentID, uuid.NewString()}},
			{`INSERT INTO post (id, content_document_id) VALUES (?::uuid, ?::uuid)`, []any{postID, documentID}},
			{`INSERT INTO content_block (id, document_id, parent_block_id, container_slot, position, kind, shared_data)
			  VALUES (?::uuid, ?::uuid, NULL, 'root', 0, 'file', '{}'::jsonb)`, []any{blockID, documentID}},
			{`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id, download_audience)
			  VALUES (?::uuid, 'file', 'active', ?::uuid, 'restricted')`, []any{blockID, fileID}},
		} {
			if err := tx.Exec(statement.sql, statement.args...).Error; err != nil {
				return err
			}
		}
		for _, segmentID := range segmentIDs {
			if err := tx.Create(&model.ContentBlockAttachmentDownloadAudienceSegment{
				BlockID: blockID, ReferencePath: "file", AudienceSegmentID: segmentID, CreatedAt: time.Now().UTC(),
			}).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	return audience.DownloadPolicy{
		Owner: "post", OwnerID: postID, ItemID: blockID, FileID: fileID, Audience: "restricted",
		SegmentIDs: slices.Sorted(slices.Values(segmentIDs)),
	}
}

func (f postgresFixtures) TrackPolicy(t *testing.T, segmentIDs ...string) audience.DownloadPolicy {
	t.Helper()
	fileID := f.file(t)
	documentID, releaseID, trackID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'compact', ?::uuid)`, documentID, uuid.NewString()).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO release (id, type, content_document_id) VALUES (?::uuid, 'RELEASE_TYPE_ALBUM', ?::uuid)`, releaseID, documentID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO track (id, release_id, track_number, title, audio_original_file_id, download_audience)
			VALUES (?::uuid, ?::uuid, 1, 'Audience Track', ?::uuid, 'restricted')
		`, trackID, releaseID, fileID).Error; err != nil {
			return err
		}
		for _, segmentID := range segmentIDs {
			if err := tx.Create(&model.TrackDownloadAudienceSegment{
				TrackID: trackID, AudienceSegmentID: segmentID, CreatedAt: time.Now().UTC(),
			}).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	return audience.DownloadPolicy{
		Owner: "release", OwnerID: releaseID, ItemID: trackID, FileID: fileID, Audience: "restricted",
		SegmentIDs: slices.Sorted(slices.Values(segmentIDs)),
	}
}

func (f postgresFixtures) CampaignOf(t *testing.T, id string) audiencetest.Campaign {
	t.Helper()
	var row model.Campaign
	require.NoError(t, f.db.First(&row, "id = ?", id).Error)
	var status string
	for name, stored := range campaignStatuses {
		if string(stored) == row.Status {
			status = name
		}
	}
	return audiencetest.Campaign{SegmentID: *row.SegmentID, Status: status, Scheduled: row.ScheduledAt != nil}
}

func (f postgresFixtures) DeliveryRunOf(t *testing.T, id string) audiencetest.DeliveryRun {
	t.Helper()
	var row model.CampaignDeliveryRun
	require.NoError(t, f.db.First(&row, "id = ?", id).Error)
	return audiencetest.DeliveryRun{SegmentID: *row.AudienceSegmentID, Status: row.Status, Completed: row.CompletedAt != nil}
}
