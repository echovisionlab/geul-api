//go:build integration

package audience_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

const (
	audienceTestDeliveryRunKindCampaign = "campaign"
	audienceTestRunScheduled            = "scheduled"
	audienceTestRunSending              = "sending"
	audienceTestRunCancelled            = "cancelled"
	audienceTestTargetQueryVersion      = int16(2)
	audienceTestTargetModeAllUsers      = "all_users"
	audienceTestTargetModeUsersByFilter = "users_by_filter"
)

func TestAudienceArchivePreservesCampaignAndUserTagReferencesUnit(t *testing.T) {
	db := newAudienceAccessMutationDB(t)
	spiceDB, adminCtx := audienceAccessAdminContext(t, db)
	segmentID := uuid.NewString()
	userTagID := uuid.NewString()
	seedAudienceAccessUserTag(t, db, userTagID)
	require.NoError(t, db.Exec(
		`INSERT INTO audience_segment (
			id, name, segment_type, created_at, updated_at
		) VALUES (?, 'Referenced', 'SEGMENT_TYPE_MEMBER_TAGS', ?, ?)`,
		segmentID,
		time.Now().UTC(),
		time.Now().UTC(),
	).Error)
	seedAudienceAccessSegmentPolicy(t, spiceDB, segmentID)
	require.NoError(t, db.Exec(
		`INSERT INTO audience_segment_user_tag (audience_segment_id, user_tag_id)
		 VALUES (?, ?)`,
		segmentID,
		userTagID,
	).Error)
	seedAudienceAccessCampaign(t, db, uuid.NewString(), segmentID,
		managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(), nil)

	audienceSvc := newAudienceServiceForTest(db, spiceDB)
	archived, err := audienceSvc.ArchiveSegment(
		adminCtx,
		connect.NewRequest(&managev1.ArchiveSegmentRequest{Id: segmentID}),
	)
	require.NoError(t, err)
	require.NotNil(t, archived.Msg.ArchivedAt)
	require.Equal(t, int32(1), archived.Msg.CampaignCount)

	memberSvc := memberdomain.NewMemberService(
		db, "", spiceDB, audienceIdentityManager{}, audienceNoopFileDeleter{}, "", noopMemberEmailPublisher{},
	)
	_, err = memberSvc.DeleteMemberTag(
		adminCtx,
		connect.NewRequest(&managev1.DeleteMemberTagRequest{Id: userTagID}),
	)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

}

func TestAudienceSegmentUpdateRejectsActiveCampaignAndRunUnit(t *testing.T) {
	db := newAudienceAccessMutationDB(t)
	spiceDB, adminCtx := audienceAccessAdminContext(t, db)
	segmentID := uuid.NewString()
	campaignID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO audience_segment (
			id, name, segment_type, created_at,
			updated_at
		) VALUES (?, 'Active delivery', 'SEGMENT_TYPE_ALL_MEMBERS', ?, ?)`,
		segmentID,
		now,
		now,
	).Error)
	seedAudienceAccessSegmentPolicy(t, spiceDB, segmentID)
	seedAudienceAccessCampaign(t, db, campaignID, segmentID,
		managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String(), nil)

	name := "Must not change"
	service := newAudienceServiceForTest(db, spiceDB)
	_, err := service.UpdateSegment(
		adminCtx,
		connect.NewRequest(&managev1.UpdateSegmentRequest{
			Id:   segmentID,
			Name: &name,
		}),
	)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	require.NoError(t, db.Table("campaign").
		Where("id = ?", campaignID).
		Update("status", managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String()).Error)
	seedAudienceAccessDeliveryRun(
		t, db, campaignID, segmentID, audienceTestRunSending, audienceTestTargetModeAllUsers,
	)
	_, err = service.UpdateSegment(
		adminCtx,
		connect.NewRequest(&managev1.UpdateSegmentRequest{
			Id:   segmentID,
			Name: &name,
		}),
	)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestAudienceArchiveCancelsScheduledWorkDetachesFilesAndRestoresUnit(t *testing.T) {
	db := newAudienceAccessMutationDB(t)
	spiceDB, adminCtx := audienceAccessAdminContext(t, db)
	segmentID := uuid.NewString()
	scheduledCampaignID := uuid.NewString()
	sendingCampaignID := uuid.NewString()
	fileID := uuid.NewString()
	postID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO audience_segment (
			id, name, segment_type, created_at,
			updated_at
		) VALUES (?, 'Archive lifecycle', 'SEGMENT_TYPE_MEMBERS_BY_FILTER', ?, ?)`,
		segmentID,
		now,
		now,
	).Error)
	seedAudienceAccessSegmentPolicy(t, spiceDB, segmentID)
	scheduledAt := now.Add(time.Hour)
	seedAudienceAccessCampaign(t, db, scheduledCampaignID, segmentID,
		managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String(), &scheduledAt)
	seedAudienceAccessCampaign(t, db, sendingCampaignID, segmentID,
		managev1.CampaignStatus_CAMPAIGN_STATUS_SENDING.String(), nil)
	scheduledRunID := seedAudienceAccessDeliveryRun(
		t, db, scheduledCampaignID, segmentID, audienceTestRunScheduled, audienceTestTargetModeUsersByFilter,
	)
	sendingRunID := seedAudienceAccessDeliveryRun(
		t, db, sendingCampaignID, segmentID, audienceTestRunSending, audienceTestTargetModeUsersByFilter,
	)
	require.NoError(t, db.Exec(
		`INSERT INTO file (id, file_name, mime_type, file_size, extension)
		 VALUES (?, 'audience-access', 'text/plain', 1, 'txt')`,
		fileID,
	).Error)
	blockID := seedAudiencePostFileAttachment(t, db, postID, fileID)
	require.NoError(t, db.Table("content_block_attachment").
		Where("block_id = ? AND reference_path = 'file'", blockID).
		Update("download_audience", "restricted").Error)
	require.NoError(t, db.Create(&model.ContentBlockAttachmentDownloadAudienceSegment{
		BlockID: blockID, ReferencePath: "file", AudienceSegmentID: segmentID, CreatedAt: now,
	}).Error)

	service := newAudienceServiceForTest(db, spiceDB)
	archived, err := service.ArchiveSegment(
		adminCtx,
		connect.NewRequest(&managev1.ArchiveSegmentRequest{Id: segmentID}),
	)
	require.NoError(t, err)
	require.NotNil(t, archived.Msg.ArchivedAt)
	require.Equal(t, int32(2), archived.Msg.CampaignCount)
	require.Equal(t, int32(2), archived.Msg.DeliveryRunCount)
	require.Zero(t, archived.Msg.DownloadPolicyReferenceCount)

	var campaign struct {
		Status      string     `gorm:"column:status"`
		ScheduledAt *time.Time `gorm:"column:scheduled_at"`
	}
	require.NoError(t, db.Table("campaign").
		Select("status", "scheduled_at").
		Where("id = ?", scheduledCampaignID).
		Take(&campaign).Error)
	require.Equal(
		t,
		managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
		campaign.Status,
	)
	require.Nil(t, campaign.ScheduledAt)

	var runStatuses []struct {
		ID          string     `gorm:"column:id"`
		Status      string     `gorm:"column:status"`
		CompletedAt *time.Time `gorm:"column:completed_at"`
	}
	require.NoError(t, db.Table("email_delivery_run").
		Select("id", "status", "completed_at").
		Where("id IN ?", []string{scheduledRunID, sendingRunID}).
		Order("id ASC").
		Scan(&runStatuses).Error)
	require.Len(t, runStatuses, 2)
	statusByID := make(map[string]struct {
		Status      string
		CompletedAt *time.Time
	}, len(runStatuses))
	for _, run := range runStatuses {
		statusByID[run.ID] = struct {
			Status      string
			CompletedAt *time.Time
		}{Status: run.Status, CompletedAt: run.CompletedAt}
	}
	require.Equal(
		t,
		audienceTestRunCancelled,
		statusByID[scheduledRunID].Status,
	)
	require.NotNil(t, statusByID[scheduledRunID].CompletedAt)
	require.Equal(t, audienceTestRunSending, statusByID[sendingRunID].Status)
	require.Nil(t, statusByID[sendingRunID].CompletedAt)

	var policySegmentCount int64
	require.NoError(t, db.Table("content_block_attachment_download_audience_segment").
		Where("block_id = ? AND reference_path = 'file'", blockID).
		Count(&policySegmentCount).Error)
	require.Zero(t, policySegmentCount)

	updatedName := "Active delivery keeps segment frozen"
	_, err = service.UpdateSegment(
		adminCtx,
		connect.NewRequest(&managev1.UpdateSegmentRequest{
			Id:   segmentID,
			Name: &updatedName,
		}),
	)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	restored, err := service.RestoreSegment(
		adminCtx,
		connect.NewRequest(&managev1.RestoreSegmentRequest{Id: segmentID}),
	)
	require.NoError(t, err)
	require.Nil(t, restored.Msg.ArchivedAt)
	require.Zero(t, restored.Msg.DownloadPolicyReferenceCount)
}

func TestArchivedAudienceSegmentAdminCanUpdateMetadataAndConfigUnit(t *testing.T) {
	db := newAudienceAccessMutationDB(t)
	spiceDB, adminCtx := audienceAccessAdminContext(t, db)
	segmentID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO audience_segment (
			id, name, segment_type, created_at, updated_at
		) VALUES (?, 'Archived segment', 'SEGMENT_TYPE_MEMBERS_BY_FILTER', ?, ?)`,
		segmentID,
		now,
		now,
	).Error)
	seedAudienceAccessSegmentPolicy(t, spiceDB, segmentID)

	service := newAudienceServiceForTest(db, spiceDB)
	archived, err := service.ArchiveSegment(
		adminCtx,
		connect.NewRequest(&managev1.ArchiveSegmentRequest{Id: segmentID}),
	)
	require.NoError(t, err)
	require.NotNil(t, archived.Msg.ArchivedAt)

	name := "Archived segment renamed by admin"
	description := "Archived segment description updated by admin"
	createdAfter := timestamppb.New(now.Add(-time.Hour))
	updated, err := service.UpdateSegment(
		adminCtx,
		connect.NewRequest(&managev1.UpdateSegmentRequest{
			Id:          segmentID,
			Name:        &name,
			Description: &description,
			Config: &managev1.SegmentConfig{
				AccountRoles: []policyv1.AuthorizationRole{policyv1.AuthorizationRole_ADMIN},
				CreatedAfter: createdAfter,
			},
		}),
	)
	require.NoError(t, err)
	require.NotNil(t, updated.Msg.ArchivedAt)
	require.Equal(t, name, updated.Msg.Name)
	require.Equal(t, description, updated.Msg.GetDescription())
	require.Equal(t, []policyv1.AuthorizationRole{policyv1.AuthorizationRole_ADMIN}, updated.Msg.Config.AccountRoles)
	require.True(t, updated.Msg.Config.CreatedAfter.AsTime().Equal(createdAfter.AsTime()))
}

func TestAudienceAdminListDefaultsToActiveAndCanIncludeArchivedUnit(t *testing.T) {
	db := newAudienceAccessMutationDB(t)
	spiceDB, adminCtx := audienceAccessAdminContext(t, db)
	activeID := uuid.NewString()
	archivedID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO audience_segment (
			id, name, segment_type, archived_at,
			created_at, updated_at
		) VALUES
			(?, 'Active', 'SEGMENT_TYPE_ALL_MEMBERS', NULL, ?, ?),
			(?, 'Archived', 'SEGMENT_TYPE_ALL_MEMBERS', ?, ?, ?)`,
		activeID,
		now,
		now,
		archivedID,
		now,
		now,
		now,
	).Error)

	service := newAudienceServiceForTest(db, spiceDB)
	active, err := service.ListSegmentsAdmin(
		adminCtx,
		connect.NewRequest(&managev1.ListSegmentsAdminRequest{}),
	)
	require.NoError(t, err)
	require.Equal(t, int32(1), active.Msg.Pagination.Total)
	require.Len(t, active.Msg.Segments, 1)
	require.Equal(t, activeID, active.Msg.Segments[0].Id)

	all, err := service.ListSegmentsAdmin(
		adminCtx,
		connect.NewRequest(&managev1.ListSegmentsAdminRequest{
			IncludeArchived: true,
		}),
	)
	require.NoError(t, err)
	require.Equal(t, int32(2), all.Msg.Pagination.Total)
	require.Len(t, all.Msg.Segments, 2)
	require.Equal(t, activeID, all.Msg.Segments[0].Id)
	require.Equal(t, archivedID, all.Msg.Segments[1].Id)
	require.NotNil(t, all.Msg.Segments[1].ArchivedAt)
}

func newAudienceAccessMutationDB(t *testing.T) *gorm.DB {
	t.Helper()
	return newAudienceIntegrationDB(t)
}

func seedAudienceAccessCampaign(
	t *testing.T,
	db *gorm.DB,
	campaignID string,
	segmentID string,
	status string,
	scheduledAt *time.Time,
) {
	t.Helper()
	now := time.Now().UTC()
	campaign := &model.Campaign{
		ID:             campaignID,
		Name:           "Audience access campaign " + campaignID,
		Subject:        "Audience access subject",
		TargetMode:     model.CampaignTargetModeSegment,
		SegmentID:      &segmentID,
		Status:         managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
		RecipientScope: "SUBSCRIBED_USERS",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	createAudienceCampaignFixture(t, db, campaign)
	var stored model.Campaign
	require.NoError(t, db.First(&stored, "id = ?", campaignID).Error)
	if status != campaign.Status || scheduledAt != nil {
		require.NoError(t, db.Model(&model.Campaign{}).Where("id = ?", campaignID).Updates(map[string]any{
			"status":       status,
			"scheduled_at": scheduledAt,
			"updated_at":   now,
		}).Error)
	}
}

func seedAudienceAccessDeliveryRun(
	t *testing.T,
	db *gorm.DB,
	campaignID string,
	segmentID string,
	status string,
	targetMode string,
) string {
	t.Helper()
	now := time.Now().UTC()
	sourceCampaignUpdatedAt := audienceCampaignUpdatedAtFixture(t, db, campaignID)
	run := &model.CampaignDeliveryRun{
		ID: uuid.NewString(), RunKind: audienceTestDeliveryRunKindCampaign,
		CampaignID: &campaignID, Status: status, ScheduledAt: now,
		RenderSnapshot: audienceCampaignRenderSnapshotFixture(
			"Audience access subject",
			"<p>Audience access body</p>",
		),
		SnapshotSchemaVersion: 1, DefinitionSealed: true,
		SourceCampaignUpdatedAt: &sourceCampaignUpdatedAt,
		AudienceSegmentID:       &segmentID,
		TargetQueryVersion:      audienceTestTargetQueryVersion,
		TargetMode:              targetMode,
		TargetRecipientScope:    "SUBSCRIBED_USERS",
		CreatedAt:               now, UpdatedAt: now,
	}
	if status == audienceTestRunSending {
		run.StartedAt = &now
	}
	require.NoError(t, db.Create(run).Error)
	require.Equal(t, segmentID, *run.AudienceSegmentID)
	return run.ID
}

func seedAudienceAccessUserTag(t *testing.T, db *gorm.DB, tagID string) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO user_tag (id, name, created_at) VALUES (?, ?, ?)`,
		tagID,
		"Tag "+tagID,
		time.Now().UTC(),
	).Error)
}

func seedAudienceAccessSegmentPolicy(t *testing.T, spiceDB *auth.SpiceDBClient, segmentID string) {
	t.Helper()
	policy, err := policyv1.AudienceSegment.TouchPolicy(segmentID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		deletePolicy, deleteErr := policyv1.AudienceSegment.DeletePolicy(segmentID)
		require.NoError(t, deleteErr)
		_, deleteErr = spiceDB.ApplyRelationships(cleanupCtx, deletePolicy)
		require.NoError(t, deleteErr)
	})
}

func audienceAccessAdminContext(t *testing.T, db *gorm.DB) (*auth.SpiceDBClient, context.Context) {
	t.Helper()
	identityID := integrationTestUUID()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Audience access admin")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	return spiceDB, auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		MemberID:      auth.MemberID(memberID),
		SessionID:     auth.SessionID(integrationTestUUID()),
		Authenticated: true,
		Onboarded:     true,
	})
}
