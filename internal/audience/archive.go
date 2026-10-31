package audience

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const maxArchiveAttempts = 3

var errArchiveRetry = errors.New("audience archive relationship changed while acquiring locks")

// ArchiveSegmentCommand archives a segment.
type ArchiveSegmentCommand struct {
	ID string
}

// ArchiveSegment archives the segment: scheduled campaigns that target it
// return to draft, its scheduled delivery runs are cancelled, and download
// policies stop granting through it. Archiving an archived segment returns
// it unchanged.
func (s *Service) ArchiveSegment(ctx context.Context, command ArchiveSegmentCommand) (*managev1.Segment, error) {
	can, err := policyv1.AudienceSegment.Edit(command.ID)
	if err != nil {
		return nil, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	for range maxArchiveAttempts {
		archive := segmentArchive{service: s, segmentID: command.ID}
		archived, err := mutation.Run(ctx, s.mutations, mutation.Spec[segmentArchive]{
			Lock:   archive.lock,
			Access: freshAdmin(can),
			Change: archive.apply,
			Audit:  segmentArchive.audit,
		})
		if errors.Is(err, errArchiveRetry) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return toProtoSegment(&archived.segment), nil
	}
	return nil, domainerr.Retry("audience segment changed while it was being archived; retry")
}

// segmentArchive is one archive attempt. lock takes the campaign, delivery
// run, segment and exact download-policy locks in the order scheduling and
// policy mutations take them, and apply archives what lock planned.
type segmentArchive struct {
	service         *Service
	segmentID       string
	alreadyArchived bool
	policyPlans     []downloadPolicyArchivePlan
	audits          []mutation.Audit
	segment         model.AudienceSegment
}

func (a *segmentArchive) lock(ctx context.Context, tx *gorm.DB) error {
	var observed model.AudienceSegment
	if err := tx.WithContext(ctx).Select("id", "archived_at").First(&observed, "id = ?", a.segmentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errSegmentNotFound()
		}
		return err
	}
	if observed.ArchivedAt != nil {
		var locked model.AudienceSegment
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Select("id", "archived_at").
			First(&locked, "id = ?", a.segmentID).Error; err != nil {
			return err
		}
		if locked.ArchivedAt == nil {
			return errArchiveRetry
		}
		a.alreadyArchived = true
		return nil
	}
	// Preserve the campaign -> delivery run -> audience segment lock order used
	// by scheduling. Download-policy mutations lock audience segments before the
	// exact attachment or Track, so policy relations are locked only after the
	// segment below.
	lockedCampaignIDs, lockedRunIDs, err := a.service.campaigns.LockSegmentUsage(ctx, tx, a.segmentID)
	if err != nil {
		return err
	}
	if err := a.requireUsageUnchanged(ctx, tx, lockedCampaignIDs, lockedRunIDs); err != nil {
		return err
	}
	var segment model.AudienceSegment
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&segment, "id = ?", a.segmentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errSegmentNotFound()
		}
		return err
	}
	if segment.ArchivedAt != nil {
		a.alreadyArchived = true
		return nil
	}
	if err := a.requireUsageUnchanged(ctx, tx, lockedCampaignIDs, lockedRunIDs); err != nil {
		return err
	}
	a.policyPlans, err = lockDownloadPolicyArchivePlans(ctx, tx, a.segmentID)
	return err
}

// requireUsageUnchanged retries the archive when a campaign or active
// delivery run started or stopped using the segment while the locks were
// taken.
func (a *segmentArchive) requireUsageUnchanged(ctx context.Context, tx *gorm.DB, lockedCampaignIDs, lockedRunIDs []string) error {
	changed, err := a.service.campaigns.SegmentUsageChanged(ctx, tx, a.segmentID, lockedCampaignIDs, lockedRunIDs)
	if err != nil {
		return err
	}
	if changed {
		return errArchiveRetry
	}
	return nil
}

func (a *segmentArchive) apply(ctx context.Context, tx *gorm.DB) (segmentArchive, error) {
	if !a.alreadyArchived {
		if err := a.archive(ctx, tx); err != nil {
			return segmentArchive{}, err
		}
	}
	if err := a.service.loadSegmentForResponse(ctx, tx, a.segmentID, &a.segment); err != nil {
		return segmentArchive{}, err
	}
	return *a, nil
}

func (a *segmentArchive) archive(ctx context.Context, tx *gorm.DB) error {
	now := time.Now().UTC()
	draftedCampaignIDs, err := a.service.campaigns.ReleaseSegmentForArchive(ctx, tx, a.segmentID, now)
	if err != nil {
		return err
	}
	if err := tx.WithContext(ctx).
		Where("audience_segment_id = ?", a.segmentID).
		Delete(&model.ContentBlockAttachmentDownloadAudienceSegment{}).Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).
		Where("audience_segment_id = ?", a.segmentID).
		Delete(&model.TrackDownloadAudienceSegment{}).Error; err != nil {
		return err
	}
	archived := tx.WithContext(ctx).
		Model(&model.AudienceSegment{}).
		Where("id = ? AND archived_at IS NULL", a.segmentID).
		Updates(structured.Fields{
			"archived_at": now,
			"updated_at":  now,
		})
	if archived.Error != nil {
		return archived.Error
	}
	if archived.RowsAffected != 1 {
		return errArchiveRetry
	}
	a.audits, err = archiveAudits(a.segmentID, draftedCampaignIDs, a.policyPlans)
	return err
}

func (a segmentArchive) audit() mutation.Audit {
	if a.alreadyArchived {
		return mutation.Unchanged()
	}
	return mutation.Audits(a.audits...)
}

// RestoreSegmentCommand restores an archived segment.
type RestoreSegmentCommand struct {
	ID string
}

// RestoreSegment makes an archived segment active again.
func (s *Service) RestoreSegment(ctx context.Context, command RestoreSegmentCommand) (*managev1.Segment, error) {
	can, err := policyv1.AudienceSegment.Edit(command.ID)
	if err != nil {
		return nil, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	var segment model.AudienceSegment
	restored, err := mutation.Run(ctx, s.mutations, mutation.Spec[model.AudienceSegment]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return lockSegmentForUpdate(ctx, tx, command.ID, &segment)
		},
		Access: freshAdmin(can),
		Change: func(ctx context.Context, tx *gorm.DB) (model.AudienceSegment, error) {
			if segment.ArchivedAt == nil {
				return segment, domainerr.Precondition("audience segment is not archived")
			}
			result := tx.WithContext(ctx).
				Model(&model.AudienceSegment{}).
				Where("id = ? AND archived_at IS NOT NULL", segment.ID).
				Updates(structured.Fields{
					"archived_at": nil,
					"updated_at":  time.Now().UTC(),
				})
			if result.Error != nil {
				return segment, result.Error
			}
			if result.RowsAffected != 1 {
				return segment, domainerr.Precondition("audience segment is not archived")
			}
			err := s.loadSegmentForResponse(ctx, tx, segment.ID, &segment)
			return segment, err
		},
		Audit: func(segment model.AudienceSegment) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditAudienceSegmentUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAudienceSegmentLifecycleUpdatedAuditRecord(
					metadata, segment.ID, sharedtelemetry.AuditStateArchived, sharedtelemetry.AuditStateActive,
				)
			})
		},
	})
	if err != nil {
		return nil, err
	}
	return toProtoSegment(&restored), nil
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
	attachments, err := lockAttachmentDownloadPolicyTargets(ctx, tx, segmentID)
	if err != nil {
		return nil, err
	}
	tracks, err := lockTrackDownloadPolicyTargets(ctx, tx, segmentID)
	if err != nil {
		return nil, err
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

// lockAttachmentDownloadPolicyTargets locks the File attachments whose
// download policy grants through the segment, with their owning document.
func lockAttachmentDownloadPolicyTargets(ctx context.Context, tx *gorm.DB, segmentID string) ([]attachmentDownloadPolicyTarget, error) {
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
		return nil, err
	}
	for _, attachment := range attachments {
		if attachment.Domain == "" || attachment.OwnerID == "" || attachment.FileID == "" {
			return nil, fmt.Errorf("download policy attachment %s/%s has no supported owner or active file", attachment.BlockID, attachment.ReferencePath)
		}
	}
	return attachments, nil
}

// lockTrackDownloadPolicyTargets locks the Tracks whose download policy
// grants through the segment.
func lockTrackDownloadPolicyTargets(ctx context.Context, tx *gorm.DB, segmentID string) ([]trackDownloadPolicyTarget, error) {
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
		return nil, err
	}
	for _, track := range tracks {
		if track.ReleaseID == "" || track.FileID == "" {
			return nil, fmt.Errorf("track download policy %s has no Release or original File", track.TrackID)
		}
	}
	return tracks, nil
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
		return nil, err
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
		return nil, err
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

// archiveAudits are the records of one archive, in the order the archive
// applied them: campaigns returned to draft, download policies that lost the
// segment, then the segment.
func archiveAudits(
	segmentID string,
	campaignIDs []string,
	policyPlans []downloadPolicyArchivePlan,
) ([]mutation.Audit, error) {
	audits := make([]mutation.Audit, 0, len(campaignIDs)+len(policyPlans)+1)
	for _, campaignID := range campaignIDs {
		audits = append(audits, mutation.RequestAudit(sharedtelemetry.AuditCampaignUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewCampaignStatusLifecycleAuditRecord(
				metadata, campaignID, sharedtelemetry.AuditStateScheduled, sharedtelemetry.AuditStateDraft,
			)
		}))
	}
	for _, plan := range policyPlans {
		previous := append([]string(nil), plan.previousSegmentIDs...)
		next := append([]string(nil), plan.segmentIDs...)
		action, builder, err := downloadPolicyArchiveAudit(plan, previous, next)
		if err != nil {
			return nil, err
		}
		audits = append(audits, mutation.RequestAudit(action, builder))
	}
	return append(audits, mutation.RequestAudit(sharedtelemetry.AuditAudienceSegmentUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewAudienceSegmentLifecycleUpdatedAuditRecord(
			metadata, segmentID, sharedtelemetry.AuditStateActive, sharedtelemetry.AuditStateArchived,
		)
	})), nil
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
		return "", nil, fmt.Errorf("unsupported download policy owner %q", plan.domain)
	}
}
