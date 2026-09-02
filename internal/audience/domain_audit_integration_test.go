//go:build integration

package audience_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type audienceDomainAuditRecord struct {
	Action        string
	TargetType    string `gorm:"column:target_type"`
	TargetID      string `gorm:"column:target_id"`
	ActorMemberID string `gorm:"column:actor_member_id"`
	RequestID     string `gorm:"column:request_id"`
	Attributes    []byte `gorm:"column:attributes"`
}

type failingDomainAuditAppender struct{}

func (failingDomainAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return errors.New("audit unavailable")
}

func audienceAuditContext(
	t *testing.T,
	identityID string,
	memberID string,
) context.Context {
	t.Helper()
	request, err := sharedtelemetry.NewPropagatedRequestContext(
		integrationTestUUID(),
		sharedtelemetry.MemberActor{
			IdentityID: identityID,
			MemberID:   memberID,
			SessionID:  integrationTestUUID(),
		},
	)
	require.NoError(t, err)
	return auth.WithUser(
		sharedtelemetry.WithRequestContext(t.Context(), request),
		&auth.UserInfo{
			IdentityID:    auth.IdentityID(identityID),
			MemberID:      auth.MemberID(memberID),
			SessionID:     auth.SessionID(integrationTestUUID()),
			Authenticated: true,
			Onboarded:     true,
		},
	)
}

func seedAudiencePostFileAttachment(
	t *testing.T,
	db *gorm.DB,
	postID string,
	fileID string,
) string {
	t.Helper()
	documentID := uuid.NewString()
	blockID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'post', ?::uuid)`,
		documentID,
		uuid.NewString(),
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO post (id, content_document_id) VALUES (?::uuid, ?::uuid)`,
		postID,
		documentID,
	).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block (
			id, document_id, parent_block_id, container_slot, position, kind, shared_data
		) VALUES (?::uuid, ?::uuid, NULL, 'root', 0, 'file', '{}'::jsonb)
	`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id)
		VALUES (?::uuid, 'file', 'active', ?::uuid)
	`, blockID, fileID).Error)
	return blockID
}

func seedAudienceAdditionalPostFileAttachment(
	t *testing.T,
	db *gorm.DB,
	postID string,
	fileID string,
) string {
	t.Helper()
	var owner struct {
		ContentDocumentID string `gorm:"column:content_document_id"`
	}
	require.NoError(t, db.Table("post").
		Select("content_document_id").
		Where("id = ?", postID).
		Take(&owner).Error)
	blockID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO content_block (
			id, document_id, parent_block_id, container_slot, position, kind, shared_data
		) VALUES (?::uuid, ?::uuid, NULL, 'root', 1, 'file', '{}'::jsonb)
	`, blockID, owner.ContentDocumentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id)
		VALUES (?::uuid, 'file', 'active', ?::uuid)
	`, blockID, fileID).Error)
	return blockID
}

func seedAudienceAuditDeliveryRun(
	t *testing.T,
	db *gorm.DB,
	campaignID string,
	segmentID string,
	status string,
	now time.Time,
) *model.CampaignDeliveryRun {
	t.Helper()
	sourceCampaignUpdatedAt := audienceCampaignUpdatedAtFixture(t, db, campaignID)
	run := &model.CampaignDeliveryRun{
		ID: uuid.NewString(), RunKind: audienceTestDeliveryRunKindCampaign,
		CampaignID: &campaignID, Status: status, ScheduledAt: now,
		RenderSnapshot: audienceCampaignRenderSnapshotFixture(
			"Audience audit",
			"<p>Audience audit</p>",
		),
		SnapshotSchemaVersion: 1, DefinitionSealed: true,
		SourceCampaignUpdatedAt: &sourceCampaignUpdatedAt,
		AudienceSegmentID:       &segmentID,
		TargetQueryVersion:      audienceTestTargetQueryVersion,
		TargetMode:              audienceTestTargetModeUsersByFilter,
		TargetRecipientScope:    "SUBSCRIBED_USERS",
		CreatedAt:               now, UpdatedAt: now,
	}
	if status == audienceTestRunSending {
		run.StartedAt = &now
	}
	require.NoError(t, db.Create(run).Error)
	require.Equal(t, segmentID, *run.AudienceSegmentID)
	require.Equal(t, status, run.Status)
	return run
}

func TestAudienceDomainAuditsArchiveCascadeAndRestoreIntegration(t *testing.T) {
	db := newAudienceIntegrationDB(t)
	identityID := uuid.NewString()
	ory := testutil.SetupOryStack(t)
	memberID := seedExternalKratosIdentityWithTraits(
		t,
		db,
		identityID,
		"Audience audit admin",
	)
	adminSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = ory.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), adminSubject, policyv1.Role.Admin())
	require.NoError(t, err)
	ctx := audienceAuditContext(t, identityID, memberID)
	service := newAuditedAudienceServiceForTest(db, apitelemetry.NewDurableWriter(db), ory.SpiceDBClient)
	created, err := service.CreateSegment(ctx, connect.NewRequest(&managev1.CreateSegmentRequest{
		Name:        "Audience audit " + uuid.NewString(),
		SegmentType: managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER,
		Config:      &managev1.SegmentConfig{},
	}))
	require.NoError(t, err)
	segmentID := created.Msg.Id

	name := "Audience audit updated"
	_, err = service.UpdateSegment(ctx, connect.NewRequest(&managev1.UpdateSegmentRequest{
		Id: segmentID, Name: &name,
	}))
	require.NoError(t, err)
	_, err = service.UpdateSegment(ctx, connect.NewRequest(&managev1.UpdateSegmentRequest{
		Id: segmentID, Name: &name, Config: &managev1.SegmentConfig{},
	}))
	require.NoError(t, err)

	other, err := newAudienceServiceForTest(db, ory.SpiceDBClient).CreateSegment(ctx, connect.NewRequest(&managev1.CreateSegmentRequest{
		Name:        "Audience audit retained " + uuid.NewString(),
		SegmentType: managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER,
		Config:      &managev1.SegmentConfig{},
	}))
	require.NoError(t, err)
	otherSegmentID := other.Msg.Id
	now := time.Now().UTC()
	fileID := uuid.NewString()
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: "audience-audit", MimeType: "audio/wav",
		FileSize: 1, Extension: "wav", SHA256: make([]byte, 32), CreatedAt: now,
	}).Error)
	postID := uuid.NewString()
	postBlockID := seedAudiencePostFileAttachment(t, db, postID, fileID)
	secondPostBlockID := seedAudienceAdditionalPostFileAttachment(t, db, postID, fileID)
	require.NoError(t, db.Table("content_block_attachment").
		Where("block_id IN ? AND reference_path = 'file'", []string{postBlockID, secondPostBlockID}).
		Update("download_audience", "restricted").Error)
	require.NoError(t, db.Create(&[]model.ContentBlockAttachmentDownloadAudienceSegment{
		{BlockID: postBlockID, ReferencePath: "file", AudienceSegmentID: segmentID, CreatedAt: now},
		{BlockID: postBlockID, ReferencePath: "file", AudienceSegmentID: otherSegmentID, CreatedAt: now},
		{BlockID: secondPostBlockID, ReferencePath: "file", AudienceSegmentID: segmentID, CreatedAt: now},
	}).Error)
	releaseDocumentID := uuid.NewString()
	releaseID := uuid.NewString()
	trackID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'compact', ?::uuid)`,
		releaseDocumentID, uuid.NewString(),
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO release (id, type, content_document_id) VALUES (?::uuid, 'RELEASE_TYPE_ALBUM', ?::uuid)`,
		releaseID, releaseDocumentID,
	).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO track (id, release_id, track_number, title, audio_original_file_id, download_audience)
		VALUES (?::uuid, ?::uuid, 1, 'Audience audit Track', ?::uuid, 'restricted')
	`, trackID, releaseID, fileID).Error)
	require.NoError(t, db.Create(&[]model.TrackDownloadAudienceSegment{
		{TrackID: trackID, AudienceSegmentID: segmentID, CreatedAt: now},
		{TrackID: trackID, AudienceSegmentID: otherSegmentID, CreatedAt: now},
	}).Error)
	referenced, err := service.GetSegment(ctx, connect.NewRequest(&managev1.GetSegmentRequest{Id: segmentID}))
	require.NoError(t, err)
	require.Equal(t, int32(3), referenced.Msg.DownloadPolicyReferenceCount)

	content := "<p>Audience audit</p>"
	campaignID := uuid.NewString()
	campaign := model.Campaign{
		ID:   campaignID,
		Name: "Audience audit campaign", Subject: "Audience audit campaign",
		ContentHTML: &content, Status: managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
		TargetMode: model.CampaignTargetModeSegment, SegmentID: &segmentID,
		RecipientScope: "SUBSCRIBED_USERS",
		CreatedAt:      now, UpdatedAt: now,
	}
	createAudienceCampaignFixture(t, db, &campaign)
	campaign.Status = managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String()
	campaign.ScheduledAt = &now
	require.NoError(t, db.Model(&model.Campaign{}).Where("id = ?", campaignID).Updates(map[string]any{
		"status": campaign.Status, "scheduled_at": now,
	}).Error)
	scheduledRun := seedAudienceAuditDeliveryRun(t, db, campaignID, segmentID, audienceTestRunScheduled, now)
	require.NotNil(t, scheduledRun)

	sendingCampaignID := uuid.NewString()
	sendingCampaign := model.Campaign{
		ID:   sendingCampaignID,
		Name: "Audience audit sending campaign", Subject: "Audience audit sending campaign",
		ContentHTML: &content, Status: managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
		TargetMode: model.CampaignTargetModeSegment, SegmentID: &segmentID,
		RecipientScope: "SUBSCRIBED_USERS",
		CreatedAt:      now, UpdatedAt: now,
	}
	createAudienceCampaignFixture(t, db, &sendingCampaign)
	sendingCampaign.Status = managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String()
	sendingCampaign.ScheduledAt = &now
	require.NoError(t, db.Model(&model.Campaign{}).Where("id = ?", sendingCampaignID).Updates(map[string]any{
		"status": sendingCampaign.Status, "scheduled_at": now,
	}).Error)
	sendingRun := seedAudienceAuditDeliveryRun(t, db, sendingCampaignID, segmentID, audienceTestRunScheduled, now)
	require.NotNil(t, sendingRun)
	require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).Where("id = ?", sendingRun.ID).Updates(map[string]any{
		"status": audienceTestRunSending, "started_at": now,
	}).Error)

	_, err = service.ArchiveSegment(ctx, connect.NewRequest(&managev1.ArchiveSegmentRequest{Id: segmentID}))
	require.NoError(t, err)
	var campaignAfter model.Campaign
	require.NoError(t, db.First(&campaignAfter, "id = ?", campaignID).Error)
	require.Equal(t, managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(), campaignAfter.Status)
	require.Nil(t, campaignAfter.ScheduledAt)
	var sendingCampaignAfter model.Campaign
	require.NoError(t, db.First(&sendingCampaignAfter, "id = ?", sendingCampaignID).Error)
	require.Equal(t, managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(), sendingCampaignAfter.Status)
	require.Nil(t, sendingCampaignAfter.ScheduledAt)
	var scheduledRunAfter model.CampaignDeliveryRun
	require.NoError(t, db.First(&scheduledRunAfter, "id = ?", scheduledRun.ID).Error)
	require.Equal(t, audienceTestRunCancelled, scheduledRunAfter.Status)
	require.NotNil(t, scheduledRunAfter.CompletedAt)
	var runAfter model.CampaignDeliveryRun
	require.NoError(t, db.First(&runAfter, "id = ?", sendingRun.ID).Error)
	require.Equal(t, audienceTestRunSending, runAfter.Status)
	require.Nil(t, runAfter.CompletedAt)
	var postSegments []string
	require.NoError(t, db.Table("content_block_attachment_download_audience_segment").
		Where("block_id = ? AND reference_path = 'file'", postBlockID).
		Order("audience_segment_id ASC").Pluck("audience_segment_id", &postSegments).Error)
	require.Equal(t, []string{otherSegmentID}, postSegments)
	var secondPostPolicy struct {
		Audience     string `gorm:"column:download_audience"`
		SegmentCount int64  `gorm:"column:segment_count"`
	}
	require.NoError(t, db.Raw(`
		SELECT attachment.download_audience,
		       COUNT(policy_segment.audience_segment_id) AS segment_count
		FROM content_block_attachment AS attachment
		LEFT JOIN content_block_attachment_download_audience_segment AS policy_segment
		  ON policy_segment.block_id = attachment.block_id
		 AND policy_segment.reference_path = attachment.reference_path
		WHERE attachment.block_id = ?::uuid AND attachment.reference_path = 'file'
		GROUP BY attachment.download_audience
	`, secondPostBlockID).Scan(&secondPostPolicy).Error)
	require.Equal(t, "restricted", secondPostPolicy.Audience)
	require.Zero(t, secondPostPolicy.SegmentCount)
	var trackSegments []string
	require.NoError(t, db.Table("track_download_audience_segment").
		Where("track_id = ?", trackID).
		Order("audience_segment_id ASC").Pluck("audience_segment_id", &trackSegments).Error)
	require.Equal(t, []string{otherSegmentID}, trackSegments)

	var beforeRepeatedArchive int64
	require.NoError(t, db.Table("domain_audit").Count(&beforeRepeatedArchive).Error)
	_, err = service.ArchiveSegment(ctx, connect.NewRequest(&managev1.ArchiveSegmentRequest{Id: segmentID}))
	require.NoError(t, err)
	var afterRepeatedArchive int64
	require.NoError(t, db.Table("domain_audit").Count(&afterRepeatedArchive).Error)
	require.Equal(t, beforeRepeatedArchive, afterRepeatedArchive)

	_, err = service.RestoreSegment(ctx, connect.NewRequest(&managev1.RestoreSegmentRequest{Id: segmentID}))
	require.NoError(t, err)
	require.NoError(t, db.Table("content_block_attachment_download_audience_segment").
		Where("block_id = ? AND reference_path = 'file' AND audience_segment_id = ?", postBlockID, segmentID).
		Count(&afterRepeatedArchive).Error)
	require.Zero(t, afterRepeatedArchive)
	require.NoError(t, db.Table("content_block_attachment_download_audience_segment").
		Where("block_id = ? AND reference_path = 'file'", secondPostBlockID).
		Count(&afterRepeatedArchive).Error)
	require.Zero(t, afterRepeatedArchive)
	require.NoError(t, db.Table("content_block_attachment").
		Select("download_audience").
		Where("block_id = ? AND reference_path = 'file'", secondPostBlockID).
		Take(&secondPostPolicy).Error)
	require.Equal(t, "restricted", secondPostPolicy.Audience)
	require.NoError(t, db.First(&campaignAfter, "id = ?", campaignID).Error)
	require.Equal(t, managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(), campaignAfter.Status)

	var records []audienceDomainAuditRecord
	require.NoError(t, db.Raw(`
		SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id,
		       request_id::text AS request_id, attributes
		FROM public.domain_audit
		ORDER BY occurred_at, audit_id
	`).Scan(&records).Error)
	require.Len(t, records, 9)
	for _, record := range records {
		require.Equal(t, memberID, record.ActorMemberID)
		require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), record.RequestID)
	}
	requireAudienceAuditRecord(t, records, string(sharedtelemetry.AuditAudienceSegmentCreated), "audience_segment", segmentID, map[string]any{})
	requireAudienceAuditRecord(t, records, string(sharedtelemetry.AuditAudienceSegmentUpdated), "audience_segment", segmentID, map[string]any{"changed_fields": []any{"name"}})
	requireAudienceAuditRecord(t, records, string(sharedtelemetry.AuditCampaignUpdated), "campaign", campaignID, map[string]any{
		"changed_fields": []any{"status"}, "previous_state": "scheduled", "new_state": "draft",
	})
	requireAudienceAuditRecord(t, records, string(sharedtelemetry.AuditCampaignUpdated), "campaign", sendingCampaignID, map[string]any{
		"changed_fields": []any{"status"}, "previous_state": "scheduled", "new_state": "draft",
	})
	previousPolicySegmentIDs := []string{otherSegmentID, segmentID}
	sort.Strings(previousPolicySegmentIDs)
	policyAuditAttributes := func(itemID string) map[string]any {
		return map[string]any{
			"changed_fields":    []any{"file_download_audience_segment_ids"},
			"item_id":           itemID,
			"file_id":           fileID,
			"previous_item_ids": []any{previousPolicySegmentIDs[0], previousPolicySegmentIDs[1]},
			"item_ids":          []any{otherSegmentID},
		}
	}
	requireAudienceAuditRecordWithItem(t, records, string(sharedtelemetry.AuditPostUpdated), "post", postID, postBlockID, policyAuditAttributes(postBlockID))
	requireAudienceAuditRecordWithItem(t, records, string(sharedtelemetry.AuditPostUpdated), "post", postID, secondPostBlockID, map[string]any{
		"changed_fields":    []any{"file_download_audience_segment_ids"},
		"item_id":           secondPostBlockID,
		"file_id":           fileID,
		"previous_item_ids": []any{segmentID},
		"item_ids":          []any{},
	})
	requireAudienceAuditRecord(t, records, string(sharedtelemetry.AuditReleaseUpdated), "release", releaseID, policyAuditAttributes(trackID))
	// Archive and restore are both audience_segment.updated status transitions.
	var transitions []map[string]any
	for _, record := range records {
		if record.Action != string(sharedtelemetry.AuditAudienceSegmentUpdated) || record.TargetID != segmentID {
			continue
		}
		attributes := map[string]any{}
		require.NoError(t, json.Unmarshal(record.Attributes, &attributes))
		if fields, _ := attributes["changed_fields"].([]any); len(fields) == 1 && fields[0] == "status" {
			transitions = append(transitions, attributes)
		}
	}
	require.ElementsMatch(t, []map[string]any{
		{"changed_fields": []any{"status"}, "previous_state": "active", "new_state": "archived"},
		{"changed_fields": []any{"status"}, "previous_state": "archived", "new_state": "active"},
	}, transitions)
}

func TestAudienceAuditAppendFailureRollsBackCreateAndArchiveIntegration(t *testing.T) {
	db := newAudienceIntegrationDB(t)
	ory := testutil.SetupOryStack(t)
	identityID := uuid.NewString()
	memberID := seedExternalKratosIdentityWithTraits(
		t,
		db,
		identityID,
		"Audience rollback admin",
	)
	adminSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = ory.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), adminSubject, policyv1.Role.Admin())
	require.NoError(t, err)
	ctx := audienceAuditContext(t, identityID, memberID)
	failing := newAuditedAudienceServiceForTest(db, failingDomainAuditAppender{}, ory.SpiceDBClient)
	name := "Audience audit rollback " + uuid.NewString()
	_, err = failing.CreateSegment(ctx, connect.NewRequest(&managev1.CreateSegmentRequest{
		Name: name, SegmentType: managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER, Config: &managev1.SegmentConfig{},
	}))
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Table("audience_segment").Where("name = ?", name).Count(&count).Error)
	require.Zero(t, count)

	segment, err := newAudienceServiceForTest(db, ory.SpiceDBClient).CreateSegment(ctx, connect.NewRequest(&managev1.CreateSegmentRequest{
		Name: "Audience archive rollback " + uuid.NewString(), SegmentType: managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER, Config: &managev1.SegmentConfig{},
	}))
	require.NoError(t, err)
	fileID := uuid.NewString()
	require.NoError(t, db.Create(&model.File{ID: fileID, FileName: "rollback", MimeType: "audio/wav", FileSize: 1, Extension: "wav", SHA256: make([]byte, 32)}).Error)
	postID := uuid.NewString()
	blockID := seedAudiencePostFileAttachment(t, db, postID, fileID)
	require.NoError(t, db.Table("content_block_attachment").
		Where("block_id = ? AND reference_path = 'file'", blockID).
		Update("download_audience", "restricted").Error)
	require.NoError(t, db.Create(&model.ContentBlockAttachmentDownloadAudienceSegment{
		BlockID: blockID, ReferencePath: "file", AudienceSegmentID: segment.Msg.Id, CreatedAt: time.Now().UTC(),
	}).Error)
	now := time.Now().UTC()
	content := "<p>Audience rollback</p>"
	campaignID := uuid.NewString()
	campaign := model.Campaign{
		ID:   campaignID,
		Name: "Audience rollback campaign", Subject: "Audience rollback campaign",
		ContentHTML: &content, Status: managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
		TargetMode: model.CampaignTargetModeSegment, SegmentID: &segment.Msg.Id,
		RecipientScope: "SUBSCRIBED_USERS",
		CreatedAt:      now, UpdatedAt: now,
	}
	createAudienceCampaignFixture(t, db, &campaign)
	campaign.Status = managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String()
	campaign.ScheduledAt = &now
	require.NoError(t, db.Model(&model.Campaign{}).Where("id = ?", campaignID).Updates(map[string]any{
		"status": campaign.Status, "scheduled_at": now,
	}).Error)
	scheduledRun := seedAudienceAuditDeliveryRun(t, db, campaignID, segment.Msg.Id, audienceTestRunScheduled, now)
	require.NotNil(t, scheduledRun)
	_, err = failing.ArchiveSegment(ctx, connect.NewRequest(&managev1.ArchiveSegmentRequest{Id: segment.Msg.Id}))
	require.Error(t, err)
	var stored model.AudienceSegment
	require.NoError(t, db.First(&stored, "id = ?", segment.Msg.Id).Error)
	require.Nil(t, stored.ArchivedAt)
	require.NoError(t, db.Table("content_block_attachment_download_audience_segment").
		Where("block_id = ? AND reference_path = 'file' AND audience_segment_id = ?", blockID, segment.Msg.Id).
		Count(&count).Error)
	require.EqualValues(t, 1, count)
	var campaignAfter model.Campaign
	require.NoError(t, db.First(&campaignAfter, "id = ?", campaignID).Error)
	require.Equal(t, managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String(), campaignAfter.Status)
	require.NotNil(t, campaignAfter.ScheduledAt)
	var runAfter model.CampaignDeliveryRun
	require.NoError(t, db.First(&runAfter, "id = ?", scheduledRun.ID).Error)
	require.Equal(t, audienceTestRunScheduled, runAfter.Status)
	require.Nil(t, runAfter.CompletedAt)
}

func TestAudienceArchiveLocksSegmentBeforeExactPolicyRelationIntegration(t *testing.T) {
	stack := testutil.PrepareOryIntegrationConcurrentTest(t)
	ory := testutil.SetupOryStack(t)
	identityID := uuid.NewString()
	memberID := seedExternalKratosIdentityWithTraits(
		t,
		stack.DB,
		identityID,
		"Audience archive race admin",
	)
	adminSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = ory.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), adminSubject, policyv1.Role.Admin())
	require.NoError(t, err)
	admin := audienceAuditContext(t, identityID, memberID)
	segment, err := newAudienceServiceForTest(stack.DB, ory.SpiceDBClient).CreateSegment(admin, connect.NewRequest(&managev1.CreateSegmentRequest{
		Name: "Audience archive file race " + uuid.NewString(), SegmentType: managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER, Config: &managev1.SegmentConfig{},
	}))
	require.NoError(t, err)
	segmentID := segment.Msg.Id
	fileID := uuid.NewString()
	require.NoError(t, stack.DB.Create(&model.File{
		ID: fileID, FileName: "archive-file-" + fileID, MimeType: "audio/wav",
		FileSize: 1, Extension: "wav", SHA256: make([]byte, 32),
	}).Error)
	postID := uuid.NewString()
	blockID := seedAudiencePostFileAttachment(t, stack.DB, postID, fileID)
	require.NoError(t, stack.DB.Table("content_block_attachment").
		Where("block_id = ? AND reference_path = 'file'", blockID).
		Update("download_audience", "restricted").Error)
	require.NoError(t, stack.DB.Create(&model.ContentBlockAttachmentDownloadAudienceSegment{
		BlockID: blockID, ReferencePath: "file", AudienceSegmentID: segmentID, CreatedAt: time.Now().UTC(),
	}).Error)

	blocker := stack.DB.Begin()
	require.NoError(t, blocker.Error)
	blockerFinished := false
	defer func() {
		if !blockerFinished {
			require.NoError(t, blocker.Rollback().Error)
		}
	}()
	var lockedAttachment struct {
		BlockID string `gorm:"column:block_id"`
	}
	require.NoError(t, blocker.Table("content_block_attachment").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("block_id").
		Where("block_id = ? AND reference_path = 'file'", blockID).
		Take(&lockedAttachment).Error)

	archiveApplication := "geul_audience_file_archive_" + uuid.NewString()
	archiveDB := newAudienceAuditNamedDB(t, stack.PostgresDSN, archiveApplication)

	archiveResult := make(chan error, 1)
	go func() {
		_, archiveErr := newAudienceServiceForTest(archiveDB, ory.SpiceDBClient).ArchiveSegment(admin, connect.NewRequest(&managev1.ArchiveSegmentRequest{Id: segmentID}))
		archiveResult <- archiveErr
	}()
	requireAudienceAuditOperationWaitingOnLock(t, stack.DB, archiveApplication, archiveResult)
	segmentProbe := stack.DB.Begin()
	require.NoError(t, segmentProbe.Error)
	var archiveLockedSegment model.AudienceSegment
	require.Error(t, segmentProbe.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).First(&archiveLockedSegment, "id = ?", segmentID).Error)
	require.NoError(t, segmentProbe.Rollback().Error)
	require.NoError(t, blocker.Commit().Error)
	blockerFinished = true

	select {
	case archiveErr := <-archiveResult:
		require.NoError(t, archiveErr)
	case <-time.After(5 * time.Second):
		t.Fatal("audience archive did not complete after the exact relation lock was released")
	}

	var archived model.AudienceSegment
	require.NoError(t, stack.DB.First(&archived, "id = ?", segmentID).Error)
	require.NotNil(t, archived.ArchivedAt)
	var assignments int64
	require.NoError(t, stack.DB.Table("content_block_attachment_download_audience_segment").
		Where("block_id = ? AND reference_path = 'file' AND audience_segment_id = ?", blockID, segmentID).
		Count(&assignments).Error)
	require.Zero(t, assignments)
}

func newAudienceAuditNamedDB(t *testing.T, dsn, applicationName string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(`SELECT set_config('application_name', ?, false)`, applicationName).Error)
	return db
}

func requireAudienceAuditOperationWaitingOnLock(
	t *testing.T,
	db *gorm.DB,
	applicationName string,
	result <-chan error,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-result:
			require.NoError(t, err)
			t.Fatalf("%s completed before waiting on its expected lock", applicationName)
		default:
		}
		var waiting bool
		err := db.Raw(`SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE application_name = ? AND wait_event_type = 'Lock'
		)`, applicationName).Scan(&waiting).Error
		require.NoError(t, err)
		if waiting {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not wait on its expected lock", applicationName)
}

func requireAudienceAuditRecord(t *testing.T, records []audienceDomainAuditRecord, action, targetType, targetID string, want map[string]any) {
	t.Helper()
	for _, record := range records {
		if record.Action != action || record.TargetType != targetType || record.TargetID != targetID {
			continue
		}
		attributes := map[string]any{}
		require.NoError(t, json.Unmarshal(record.Attributes, &attributes))
		require.Equal(t, want, attributes)
		return
	}
	t.Fatalf("audit %s %s/%s not found", action, targetType, targetID)
}

func requireAudienceAuditRecordWithItem(
	t *testing.T,
	records []audienceDomainAuditRecord,
	action string,
	targetType string,
	targetID string,
	itemID string,
	want map[string]any,
) {
	t.Helper()
	for _, record := range records {
		if record.Action != action || record.TargetType != targetType || record.TargetID != targetID {
			continue
		}
		attributes := map[string]any{}
		require.NoError(t, json.Unmarshal(record.Attributes, &attributes))
		if attributes["item_id"] != itemID {
			continue
		}
		require.Equal(t, want, attributes)
		return
	}
	t.Fatalf("audit %s %s/%s item %s not found", action, targetType, targetID, itemID)
}
