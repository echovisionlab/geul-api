package audience

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const maxArchiveAttempts = 3

var errArchiveRetry = errors.New("audience archive relationship changed while acquiring locks")

func (s *AudienceService) ArchiveSegment(
	ctx context.Context,
	req *connect.Request[managev1.ArchiveSegmentRequest],
) (*connect.Response[managev1.Segment], error) {
	can, err := policyv1.AudienceSegment.Edit(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	var segment model.AudienceSegment
	for range maxArchiveAttempts {
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return archiveSegmentOnce(ctx, tx, s.spiceDB, s.auditWriter, req.Msg.Id, can, &segment)
		})
		if errors.Is(err, errArchiveRetry) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(toProtoSegment(&segment)), nil
	}
	return nil, connect.NewError(
		connect.CodeAborted,
		fmt.Errorf("audience segment changed while it was being archived; retry"),
	)
}

func (s *AudienceService) RestoreSegment(
	ctx context.Context,
	req *connect.Request[managev1.RestoreSegmentRequest],
) (*connect.Response[managev1.Segment], error) {
	can, err := policyv1.AudienceSegment.Edit(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	var segment model.AudienceSegment
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&segment, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFoundMsg("segment not found")
			}
			return errs.Internal(err)
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if segment.ArchivedAt == nil {
			return errs.FailedPrecondition("audience segment is not archived")
		}
		now := time.Now().UTC()
		result := tx.WithContext(ctx).
			Model(&model.AudienceSegment{}).
			Where("id = ? AND archived_at IS NOT NULL", segment.ID).
			Updates(structured.Fields{
				"archived_at": nil,
				"updated_at":  now,
			})
		if result.Error != nil {
			return errs.Internal(result.Error)
		}
		if result.RowsAffected != 1 {
			return errs.FailedPrecondition("audience segment is not archived")
		}
		if err := domainaudit.AppendOptionalRequest(
			ctx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditAudienceSegmentUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAudienceSegmentLifecycleUpdatedAuditRecord(
					metadata,
					segment.ID,
					sharedtelemetry.AuditStateArchived,
					sharedtelemetry.AuditStateActive,
				)
			},
		); err != nil {
			return err
		}
		return loadSegmentForResponse(ctx, tx, segment.ID, &segment)
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoSegment(&segment)), nil
}

func archiveSegmentOnce(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	auditWriter domainaudit.Appender,
	segmentID string,
	can policyv1.Can,
	result *model.AudienceSegment,
) error {
	var observed model.AudienceSegment
	if err := tx.WithContext(ctx).Select("id", "archived_at").First(&observed, "id = ?", segmentID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.NotFoundMsg("segment not found")
		}
		return errs.Internal(err)
	}
	if observed.ArchivedAt != nil {
		var locked model.AudienceSegment
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "archived_at").
			First(&locked, "id = ?", segmentID).Error; err != nil {
			return errs.Internal(err)
		}
		if locked.ArchivedAt == nil {
			return errArchiveRetry
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, spiceDB, can); err != nil {
			return err
		}
		return loadSegmentForResponse(ctx, tx, segmentID, result)
	}
	// Preserve the campaign -> delivery run -> audience segment lock order used
	// by scheduling. Download-policy mutations lock audience segments before the
	// exact attachment or Track, so policy relations are locked only after the
	// segment below.
	lockedCampaignIDs, err := lockAudienceCampaigns(ctx, tx, segmentID)
	if err != nil {
		return err
	}
	lockedRunIDs, err := lockActiveDeliveryRuns(ctx, tx, segmentID)
	if err != nil {
		return err
	}
	if changed, err := audienceCampaignsChanged(ctx, tx, segmentID, lockedCampaignIDs); err != nil {
		return err
	} else if changed {
		return errArchiveRetry
	}
	if changed, err := audienceActiveRunsChanged(ctx, tx, segmentID, lockedRunIDs); err != nil {
		return err
	} else if changed {
		return errArchiveRetry
	}
	var segment model.AudienceSegment
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&segment, "id = ?", segmentID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.NotFoundMsg("segment not found")
		}
		return errs.Internal(err)
	}
	if segment.ArchivedAt != nil {
		return loadSegmentForResponse(ctx, tx, segmentID, result)
	}
	if changed, err := audienceCampaignsChanged(ctx, tx, segmentID, lockedCampaignIDs); err != nil {
		return err
	} else if changed {
		return errArchiveRetry
	}
	if changed, err := audienceActiveRunsChanged(ctx, tx, segmentID, lockedRunIDs); err != nil {
		return err
	} else if changed {
		return errArchiveRetry
	}
	policyPlans, err := lockDownloadPolicyArchivePlans(ctx, tx, segmentID)
	if err != nil {
		return err
	}
	if err := identitystate.RequireFreshAdminCan(ctx, tx, spiceDB, can); err != nil {
		return err
	}
	scheduledCampaignIDs, err := lockedScheduledCampaignIDs(ctx, tx, segmentID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Where("audience_segment_id = ? AND status = ?", segmentID, deliveryRunScheduled).
		Updates(structured.Fields{
			"status":       deliveryRunCancelled,
			"completed_at": now,
			"updated_at":   now,
		}).Error; err != nil {
		return errs.Internal(err)
	}
	if err := tx.WithContext(ctx).
		Model(&model.Campaign{}).
		Where("segment_id = ? AND status = ?", segmentID, manageCampaignScheduled).
		Updates(structured.Fields{
			"status":       manageCampaignDraft,
			"scheduled_at": nil,
			"updated_at":   now,
		}).Error; err != nil {
		return errs.Internal(err)
	}
	if err := tx.WithContext(ctx).
		Where("audience_segment_id = ?", segmentID).
		Delete(&model.ContentBlockAttachmentDownloadAudienceSegment{}).Error; err != nil {
		return errs.Internal(err)
	}
	if err := tx.WithContext(ctx).
		Where("audience_segment_id = ?", segmentID).
		Delete(&model.TrackDownloadAudienceSegment{}).Error; err != nil {
		return errs.Internal(err)
	}
	archived := tx.WithContext(ctx).
		Model(&model.AudienceSegment{}).
		Where("id = ? AND archived_at IS NULL", segmentID).
		Updates(structured.Fields{
			"archived_at": now,
			"updated_at":  now,
		})
	if archived.Error != nil {
		return errs.Internal(archived.Error)
	}
	if archived.RowsAffected != 1 {
		return errArchiveRetry
	}
	if err := appendArchiveAudits(ctx, tx, auditWriter, segmentID, scheduledCampaignIDs, policyPlans); err != nil {
		return err
	}
	return loadSegmentForResponse(ctx, tx, segmentID, result)
}

type downloadPolicyArchivePlan struct {
	domain             string
	ownerID            string
	itemID             string
	fileID             string
	audience           string
	previousSegmentIDs []string
	segmentIDs         []string
}

type attachmentDownloadPolicyTarget struct {
	Domain        string `gorm:"column:domain"`
	OwnerID       string `gorm:"column:owner_id"`
	BlockID       string `gorm:"column:block_id"`
	ReferencePath string `gorm:"column:reference_path"`
	FileID        string `gorm:"column:file_id"`
	Audience      string `gorm:"column:download_audience"`
}

type trackDownloadPolicyTarget struct {
	ReleaseID string `gorm:"column:release_id"`
	TrackID   string `gorm:"column:track_id"`
	FileID    string `gorm:"column:file_id"`
	Audience  string `gorm:"column:download_audience"`
}

func lockDownloadPolicyArchivePlans(
	ctx context.Context,
	tx *gorm.DB,
	segmentID string,
) ([]downloadPolicyArchivePlan, error) {
	var attachments []attachmentDownloadPolicyTarget
	if err := tx.WithContext(ctx).Raw(`
		SELECT CASE
		         WHEN post.id IS NOT NULL THEN 'post'
		         WHEN page.id IS NOT NULL THEN 'page'
		         WHEN work.id IS NOT NULL THEN 'work'
		         WHEN program_event.id IS NOT NULL THEN 'program_event'
		       END AS domain,
		       COALESCE(post.id, page.id, work.id, program_event.id)::text AS owner_id,
		       attachment.block_id::text AS block_id,
		       attachment.reference_path,
		       attachment.file_id::text AS file_id,
		       attachment.download_audience
		FROM content_block_attachment AS attachment
		JOIN content_block AS block ON block.id = attachment.block_id
		JOIN content_block_attachment_download_audience_segment AS policy_segment
		  ON policy_segment.block_id = attachment.block_id
		 AND policy_segment.reference_path = attachment.reference_path
		LEFT JOIN post ON post.content_document_id = block.document_id
		LEFT JOIN page ON page.content_document_id = block.document_id
		LEFT JOIN work ON work.content_document_id = block.document_id
		LEFT JOIN program_event ON program_event.content_document_id = block.document_id
		WHERE policy_segment.audience_segment_id = ?::uuid
		ORDER BY attachment.block_id, attachment.reference_path
		FOR UPDATE OF attachment
	`, segmentID).Scan(&attachments).Error; err != nil {
		return nil, errs.Internal(err)
	}
	for _, attachment := range attachments {
		if attachment.Domain == "" || attachment.OwnerID == "" || attachment.FileID == "" {
			return nil, errs.Internal(fmt.Errorf("download policy attachment %s/%s has no supported owner or active file", attachment.BlockID, attachment.ReferencePath))
		}
	}
	var tracks []trackDownloadPolicyTarget
	if err := tx.WithContext(ctx).Raw(`
		SELECT track.release_id::text AS release_id,
		       track.id::text AS track_id,
		       track.audio_original_file_id::text AS file_id,
		       track.download_audience
		FROM track
		JOIN track_download_audience_segment AS policy_segment
		  ON policy_segment.track_id = track.id
		WHERE policy_segment.audience_segment_id = ?::uuid
		ORDER BY track.id
		FOR UPDATE OF track
	`, segmentID).Scan(&tracks).Error; err != nil {
		return nil, errs.Internal(err)
	}
	for _, track := range tracks {
		if track.ReleaseID == "" || track.FileID == "" {
			return nil, errs.Internal(fmt.Errorf("track download policy %s has no Release or original File", track.TrackID))
		}
	}

	plans := make([]downloadPolicyArchivePlan, 0, len(attachments)+len(tracks))
	for _, attachment := range attachments {
		segmentIDs, err := attachmentDownloadPolicySegmentIDs(ctx, tx, attachment.BlockID, attachment.ReferencePath)
		if err != nil {
			return nil, err
		}
		plan, ok := newDownloadPolicyArchivePlan(
			attachment.Domain,
			attachment.OwnerID,
			attachment.BlockID,
			attachment.FileID,
			attachment.Audience,
			segmentID,
			segmentIDs,
		)
		if !ok {
			return nil, errArchiveRetry
		}
		plans = append(plans, plan)
	}
	for _, track := range tracks {
		segmentIDs, err := trackDownloadPolicySegmentIDs(ctx, tx, track.TrackID)
		if err != nil {
			return nil, err
		}
		plan, ok := newDownloadPolicyArchivePlan(
			"release",
			track.ReleaseID,
			track.TrackID,
			track.FileID,
			track.Audience,
			segmentID,
			segmentIDs,
		)
		if !ok {
			return nil, errArchiveRetry
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func lockedScheduledCampaignIDs(
	ctx context.Context,
	tx *gorm.DB,
	segmentID string,
) ([]string, error) {
	var rows []struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).
		Table("campaign").
		Select("id").
		Where("segment_id = ? AND status = ?", segmentID, manageCampaignScheduled).
		Order("id ASC").
		Scan(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	ids := make([]string, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	return ids, nil
}

func attachmentDownloadPolicySegmentIDs(
	ctx context.Context,
	tx *gorm.DB,
	blockID string,
	referencePath string,
) ([]string, error) {
	var segmentIDs []string
	if err := tx.WithContext(ctx).
		Table("content_block_attachment_download_audience_segment").
		Where("block_id = ? AND reference_path = ?", blockID, referencePath).
		Order("audience_segment_id ASC").
		Pluck("audience_segment_id", &segmentIDs).Error; err != nil {
		return nil, errs.Internal(err)
	}
	return segmentIDs, nil
}

func trackDownloadPolicySegmentIDs(ctx context.Context, tx *gorm.DB, trackID string) ([]string, error) {
	var segmentIDs []string
	if err := tx.WithContext(ctx).
		Table("track_download_audience_segment").
		Where("track_id = ?", trackID).
		Order("audience_segment_id ASC").
		Pluck("audience_segment_id", &segmentIDs).Error; err != nil {
		return nil, errs.Internal(err)
	}
	return segmentIDs, nil
}

func newDownloadPolicyArchivePlan(
	domain string,
	ownerID string,
	itemID string,
	fileID string,
	audience string,
	archivedSegmentID string,
	previousSegmentIDs []string,
) (downloadPolicyArchivePlan, bool) {
	next := make([]string, 0, len(previousSegmentIDs))
	found := false
	for _, id := range previousSegmentIDs {
		if id == archivedSegmentID {
			found = true
			continue
		}
		next = append(next, id)
	}
	return downloadPolicyArchivePlan{
		domain:             domain,
		ownerID:            ownerID,
		itemID:             itemID,
		fileID:             fileID,
		audience:           audience,
		previousSegmentIDs: append([]string(nil), previousSegmentIDs...),
		segmentIDs:         next,
	}, found
}

func appendArchiveAudits(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	segmentID string,
	campaignIDs []string,
	policyPlans []downloadPolicyArchivePlan,
) error {
	for _, campaignID := range campaignIDs {
		campaignID := campaignID
		if err := domainaudit.AppendOptionalRequest(
			ctx,
			tx,
			writer,
			sharedtelemetry.AuditCampaignUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewCampaignStatusLifecycleAuditRecord(
					metadata,
					campaignID,
					sharedtelemetry.AuditStateScheduled,
					sharedtelemetry.AuditStateDraft,
				)
			},
		); err != nil {
			return err
		}
	}
	for _, plan := range policyPlans {
		plan := plan
		previous := append([]string(nil), plan.previousSegmentIDs...)
		next := append([]string(nil), plan.segmentIDs...)
		action, builder, err := downloadPolicyArchiveAudit(plan, previous, next)
		if err != nil {
			return err
		}
		if err := domainaudit.AppendOptionalRequest(
			ctx,
			tx,
			writer,
			action,
			builder,
		); err != nil {
			return err
		}
	}
	return domainaudit.AppendOptionalRequest(
		ctx,
		tx,
		writer,
		sharedtelemetry.AuditAudienceSegmentUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewAudienceSegmentLifecycleUpdatedAuditRecord(
				metadata,
				segmentID,
				sharedtelemetry.AuditStateActive,
				sharedtelemetry.AuditStateArchived,
			)
		},
	)
}

func downloadPolicyArchiveAudit(
	plan downloadPolicyArchivePlan,
	previousSegmentIDs []string,
	segmentIDs []string,
) (
	sharedtelemetry.AuditAction,
	func(sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error),
	error,
) {
	previousAudience := sharedtelemetry.AuditState(plan.audience)
	newAudience := previousAudience
	switch plan.domain {
	case "post":
		return sharedtelemetry.AuditPostUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostFileBlockDownloadPolicyAuditRecord(metadata, plan.ownerID, plan.itemID, plan.fileID, previousAudience, newAudience, previousSegmentIDs, segmentIDs)
		}, nil
	case "page":
		return sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPageFileBlockDownloadPolicyAuditRecord(metadata, plan.ownerID, plan.itemID, plan.fileID, previousAudience, newAudience, previousSegmentIDs, segmentIDs)
		}, nil
	case "work":
		return sharedtelemetry.AuditWorkUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkFileBlockDownloadPolicyAuditRecord(metadata, plan.ownerID, plan.itemID, plan.fileID, previousAudience, newAudience, previousSegmentIDs, segmentIDs)
		}, nil
	case "program_event":
		return sharedtelemetry.AuditProgramEventUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventFileBlockDownloadPolicyAuditRecord(metadata, plan.ownerID, plan.itemID, plan.fileID, previousAudience, newAudience, previousSegmentIDs, segmentIDs)
		}, nil
	case "release":
		return sharedtelemetry.AuditReleaseUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewReleaseTrackDownloadPolicyAuditRecord(metadata, plan.ownerID, plan.itemID, plan.fileID, previousAudience, newAudience, previousSegmentIDs, segmentIDs)
		}, nil
	default:
		return "", nil, errs.Internal(fmt.Errorf("unsupported download policy owner %q", plan.domain))
	}
}

func lockActiveDeliveryRuns(
	ctx context.Context,
	tx *gorm.DB,
	segmentID string,
) ([]string, error) {
	var rows []struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).
		Table("email_delivery_run").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").
		Where("audience_segment_id = ? AND status IN ?", segmentID, []string{deliveryRunScheduled, deliveryRunSending}).
		Order("id ASC").
		Scan(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	ids := make([]string, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	return ids, nil
}

func lockAudienceCampaigns(
	ctx context.Context,
	tx *gorm.DB,
	segmentID string,
) ([]string, error) {
	var rows []struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).
		Table("campaign").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").
		Where("segment_id = ?", segmentID).
		Order("id ASC").
		Scan(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	ids := make([]string, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	return ids, nil
}

func audienceCampaignsChanged(ctx context.Context, tx *gorm.DB, segmentID string, lockedIDs []string) (bool, error) {
	var rows []struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).
		Table("campaign").
		Select("id").
		Where("segment_id = ?", segmentID).
		Order("id ASC").
		Scan(&rows).Error; err != nil {
		return false, errs.Internal(err)
	}
	if len(rows) != len(lockedIDs) {
		return true, nil
	}
	for i := range rows {
		if rows[i].ID != lockedIDs[i] {
			return true, nil
		}
	}
	return false, nil
}

func audienceActiveRunsChanged(ctx context.Context, tx *gorm.DB, segmentID string, lockedIDs []string) (bool, error) {
	var rows []struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).
		Table("email_delivery_run").
		Select("id").
		Where("audience_segment_id = ? AND status IN ?", segmentID, []string{deliveryRunScheduled, deliveryRunSending}).
		Order("id ASC").
		Scan(&rows).Error; err != nil {
		return false, errs.Internal(err)
	}
	if len(rows) != len(lockedIDs) {
		return true, nil
	}
	for i := range rows {
		if rows[i].ID != lockedIDs[i] {
			return true, nil
		}
	}
	return false, nil
}
