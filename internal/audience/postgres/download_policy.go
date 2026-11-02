package postgres

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/model"
)

func (s store) GrantsDownloadAccess(_ context.Context, id string) (bool, error) {
	var attachmentReferenceCount int64
	if err := s.tx.Model(&model.ContentBlockAttachmentDownloadAudienceSegment{}).
		Where("audience_segment_id = ?", id).
		Count(&attachmentReferenceCount).Error; err != nil {
		return false, err
	}
	var trackReferenceCount int64
	if err := s.tx.Model(&model.TrackDownloadAudienceSegment{}).
		Where("audience_segment_id = ?", id).
		Count(&trackReferenceCount).Error; err != nil {
		return false, err
	}
	return attachmentReferenceCount != 0 || trackReferenceCount != 0, nil
}

func (s store) DetachDownloadPolicies(ctx context.Context, id string) error {
	if err := s.tx.WithContext(ctx).
		Where("audience_segment_id = ?", id).
		Delete(&model.ContentBlockAttachmentDownloadAudienceSegment{}).Error; err != nil {
		return err
	}
	return s.tx.WithContext(ctx).
		Where("audience_segment_id = ?", id).
		Delete(&model.TrackDownloadAudienceSegment{}).Error
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

func (s store) LockDownloadPolicies(ctx context.Context, id string) ([]audience.DownloadPolicy, error) {
	attachments, err := lockAttachmentDownloadPolicyTargets(ctx, s.tx, id)
	if err != nil {
		return nil, err
	}
	tracks, err := lockTrackDownloadPolicyTargets(ctx, s.tx, id)
	if err != nil {
		return nil, err
	}
	policies := make([]audience.DownloadPolicy, 0, len(attachments)+len(tracks))
	for _, attachment := range attachments {
		segmentIDs, err := attachmentDownloadPolicySegmentIDs(ctx, s.tx, attachment.BlockID, attachment.ReferencePath)
		if err != nil {
			return nil, err
		}
		policies = append(policies, audience.DownloadPolicy{
			Owner: attachment.Domain, OwnerID: attachment.OwnerID, ItemID: attachment.BlockID,
			FileID: attachment.FileID, Audience: attachment.Audience, SegmentIDs: segmentIDs,
		})
	}
	for _, track := range tracks {
		segmentIDs, err := trackDownloadPolicySegmentIDs(ctx, s.tx, track.TrackID)
		if err != nil {
			return nil, err
		}
		policies = append(policies, audience.DownloadPolicy{
			Owner: "release", OwnerID: track.ReleaseID, ItemID: track.TrackID,
			FileID: track.FileID, Audience: track.Audience, SegmentIDs: segmentIDs,
		})
	}
	return policies, nil
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
