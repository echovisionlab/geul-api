//go:build integration

package postgres_test

import (
	"slices"
	"testing"
	"time"

	oauthappadapter "github.com/echovisionlab/geul-api/internal/adapters/oauthapp"

	campaignpostgres "github.com/echovisionlab/geul-api/internal/campaign/postgres"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	"github.com/echovisionlab/geul-api/internal/member/criteria/criteriatest"
	criteriapostgres "github.com/echovisionlab/geul-api/internal/member/criteria/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestPostgresStoreKeepsTheStoreContractIntegration(t *testing.T) {
	t.Parallel()
	criteriatest.RunStoreContract(t, func(t *testing.T) criteriatest.Harness {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
		return criteriatest.Harness{
			Harness:  mutationtest.PostgresHarness(db, criteriapostgres.NewUnits(db, campaignpostgres.CriteriaUsage{}, oauthappadapter.SignInAppUsage{})),
			Fixtures: postgresFixtures{db: db},
		}
	})
}

// postgresFixtures seeds the rows other domains own with SQL.
type postgresFixtures struct{ db *gorm.DB }

var campaignStatuses = map[string]model.CampaignStatus{
	criteriatest.CampaignDraft:     model.CampaignStatusDraft,
	criteriatest.CampaignScheduled: model.CampaignStatusScheduled,
	criteriatest.CampaignSending:   model.CampaignStatusSending,
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
	memberID := orytest.SeedPostIntegrationIdentity(t, f.db, identityID, "Criteria Member "+identityID)
	if !eligible {
		require.NoError(t, f.db.Exec(`UPDATE kratos.identities SET external_id = NULL WHERE id = ?::uuid`, identityID).Error)
	}
	return memberID
}

func (f postgresFixtures) Campaign(t *testing.T, criteriaID, status string) string {
	t.Helper()
	now := time.Now().UTC()
	documentID := uuid.NewString()
	row := model.Campaign{
		ID: uuid.NewString(), Name: "Criteria campaign", Subject: "Criteria campaign",
		TargetMode: model.CampaignTargetModeMemberCriteria, CriteriaID: &criteriaID, ContentDocumentID: &documentID,
		Status: string(model.CampaignStatusDraft), RecipientScope: "SUBSCRIBED_USERS", CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO content_document (id, profile) VALUES (?::uuid, 'email')`, documentID).Error; err != nil {
			return err
		}
		return tx.Create(&row).Error
	}))
	if status != criteriatest.CampaignDraft {
		updates := map[string]any{"status": string(campaignStatuses[status])}
		if status == criteriatest.CampaignScheduled {
			updates["scheduled_at"] = now.Add(time.Hour)
		}
		require.NoError(t, f.db.Model(&model.Campaign{}).Where("id = ?", row.ID).Updates(updates).Error)
	}
	return row.ID
}

func (f postgresFixtures) DeliveryRun(t *testing.T, campaignID, criteriaID, status string) string {
	t.Helper()
	now := time.Now().UTC()
	var source struct {
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	require.NoError(t, f.db.Table("campaign").Select("updated_at").Where("id = ?", campaignID).Take(&source).Error)
	run := model.CampaignDeliveryRun{
		ID: uuid.NewString(), RunKind: "campaign", CampaignID: &campaignID, Status: status, ScheduledAt: now,
		RenderSnapshot: model.JSONFields{
			"subject": "Criteria", "content_html": "<p>Criteria</p>", "source_locale": "en",
			"translations": []any{model.JSONFields{"locale": "en", "subject": "Criteria", "content_html": "<p>Criteria</p>"}},
		},
		SnapshotSchemaVersion: 1, DefinitionSealed: true, SourceCampaignUpdatedAt: &source.UpdatedAt,
		MemberCriteriaID: &criteriaID, TargetQueryVersion: 2, TargetMode: "users_by_filter",
		TargetRecipientScope: "SUBSCRIBED_USERS", CreatedAt: now, UpdatedAt: now,
	}
	if status == criteriatest.RunSending {
		run.StartedAt = &now
	}
	require.NoError(t, f.db.Create(&run).Error)
	return run.ID
}

func (f postgresFixtures) file(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, f.db.Exec(
		`INSERT INTO file (id, file_name, mime_type, file_size, extension) VALUES (?, ?, 'audio/wav', 1, 'wav')`, id, "criteria-"+id,
	).Error)
	return id
}

func (f postgresFixtures) AttachmentPolicy(t *testing.T, criteriaIDs ...string) membercriteria.DownloadPolicy {
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
			{`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id, download_access)
			  VALUES (?::uuid, 'file', 'active', ?::uuid, 'restricted')`, []any{blockID, fileID}},
		} {
			if err := tx.Exec(statement.sql, statement.args...).Error; err != nil {
				return err
			}
		}
		for _, criteriaID := range criteriaIDs {
			if err := tx.Create(&model.ContentBlockAttachmentDownloadMemberCriteria{
				BlockID: blockID, ReferencePath: "file", MemberCriteriaID: criteriaID, CreatedAt: time.Now().UTC(),
			}).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	return membercriteria.DownloadPolicy{
		Owner: "post", OwnerID: postID, ItemID: blockID, FileID: fileID, Criteria: "restricted",
		CriteriaIDs: slices.Sorted(slices.Values(criteriaIDs)),
	}
}

func (f postgresFixtures) TrackPolicy(t *testing.T, criteriaIDs ...string) membercriteria.DownloadPolicy {
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
			INSERT INTO track (id, release_id, track_number, title, audio_original_file_id, download_access)
			VALUES (?::uuid, ?::uuid, 1, 'Criteria Track', ?::uuid, 'restricted')
		`, trackID, releaseID, fileID).Error; err != nil {
			return err
		}
		for _, criteriaID := range criteriaIDs {
			if err := tx.Create(&model.TrackDownloadMemberCriteria{
				TrackID: trackID, MemberCriteriaID: criteriaID, CreatedAt: time.Now().UTC(),
			}).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	return membercriteria.DownloadPolicy{
		Owner: "release", OwnerID: releaseID, ItemID: trackID, FileID: fileID, Criteria: "restricted",
		CriteriaIDs: slices.Sorted(slices.Values(criteriaIDs)),
	}
}

func (f postgresFixtures) CampaignOf(t *testing.T, id string) criteriatest.Campaign {
	t.Helper()
	var row model.Campaign
	require.NoError(t, f.db.First(&row, "id = ?", id).Error)
	var status string
	for name, stored := range campaignStatuses {
		if string(stored) == row.Status {
			status = name
		}
	}
	return criteriatest.Campaign{CriteriaID: *row.CriteriaID, Status: status, Scheduled: row.ScheduledAt != nil}
}

func (f postgresFixtures) DeliveryRunOf(t *testing.T, id string) criteriatest.DeliveryRun {
	t.Helper()
	var row model.CampaignDeliveryRun
	require.NoError(t, f.db.First(&row, "id = ?", id).Error)
	return criteriatest.DeliveryRun{CriteriaID: *row.MemberCriteriaID, Status: row.Status, Completed: row.CompletedAt != nil}
}
