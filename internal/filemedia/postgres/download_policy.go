package filemediapostgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PolicyOwners interface {
	RequirePolicyView(context.Context, *gorm.DB, string, string) error
	RequirePolicyEdit(context.Context, *gorm.DB, string, string) error
}
type PolicySignals interface {
	PublishPolicyChange(context.Context, *gorm.DB, domain.PolicyChangeSignal) error
}

func (s *store) ResolveDownloadPolicyRelation(ctx context.Context, selector domain.PolicySelector, lock bool) (domain.PolicyRelation, error) {
	var row struct {
		FileID                   string
		Audience                 mediaasset.FileDownloadAudience `gorm:"column:download_audience"`
		ResourceType, ResourceID string
	}
	query := s.tx.WithContext(ctx)
	switch selector.OwnerKind {
	case "post", "page", "work", "program_event":
		query = query.Table("content_block_attachment AS attachment").Select("attachment.file_id, attachment.download_audience, ? AS resource_type, owner.id AS resource_id", selector.OwnerKind).Joins("JOIN content_block AS block ON block.id = attachment.block_id").Joins("JOIN "+selector.OwnerKind+" AS owner ON owner.content_document_id = block.document_id").Joins("JOIN file ON file.id = attachment.file_id AND file.delete_requested_at IS NULL").Where("owner.id = ?", selector.EntityID).Where("block.id = ? AND block.kind = 'file'", *selector.BlockID).Where("attachment.reference_path = ? AND attachment.selector_kind = 'active'", *selector.ReferencePath)
		if lock {
			query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Table: clause.Table{Name: "attachment"}})
		}
	case "track":
		query = query.Table("track").Select("track.audio_original_file_id AS file_id, track.download_audience, 'release' AS resource_type, track.release_id AS resource_id").Joins("JOIN file ON file.id = track.audio_original_file_id AND file.delete_requested_at IS NULL").Where("track.id = ?", selector.EntityID)
		if lock {
			query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Table: clause.Table{Name: "track"}})
		}
	default:
		return domain.PolicyRelation{}, fmt.Errorf("unsupported File policy owner %s", selector.OwnerKind)
	}
	err := query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = storeError{err, domain.ErrPolicyRelationNotFound}
	}
	return domain.PolicyRelation{Selector: selector, FileID: row.FileID, Audience: row.Audience, ResourceType: row.ResourceType, ResourceID: row.ResourceID}, err
}
func (s *store) DownloadPolicySegmentIDs(ctx context.Context, selector domain.PolicySelector) ([]string, error) {
	var ids []string
	query := s.tx.WithContext(ctx)
	if selector.OwnerKind == "track" {
		query = query.Table("track_download_audience_segment").Where("track_id = ?", selector.EntityID)
	} else {
		query = query.Table("content_block_attachment_download_audience_segment").Where("block_id = ? AND reference_path = ?", *selector.BlockID, *selector.ReferencePath)
	}
	err := query.Order("audience_segment_id ASC").Pluck("audience_segment_id", &ids).Error
	return ids, err
}
func (s *store) SetDownloadPolicy(ctx context.Context, selector domain.PolicySelector, audience mediaasset.FileDownloadAudience, ids []string, now time.Time) error {
	if selector.OwnerKind == "track" {
		if err := s.tx.WithContext(ctx).Model(&model.Track{}).Where("id = ?", selector.EntityID).Update("download_audience", string(audience)).Error; err != nil {
			return err
		}
		if err := s.tx.WithContext(ctx).Where("track_id = ?", selector.EntityID).Delete(&model.TrackDownloadAudienceSegment{}).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		rows := make([]model.TrackDownloadAudienceSegment, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, model.TrackDownloadAudienceSegment{TrackID: selector.EntityID, AudienceSegmentID: id, CreatedAt: now})
		}
		return s.tx.WithContext(ctx).Create(&rows).Error
	}
	if err := s.tx.WithContext(ctx).Table("content_block_attachment").Where("block_id = ? AND reference_path = ?", *selector.BlockID, *selector.ReferencePath).Update("download_audience", string(audience)).Error; err != nil {
		return err
	}
	if err := s.tx.WithContext(ctx).Where("block_id = ? AND reference_path = ?", *selector.BlockID, *selector.ReferencePath).Delete(&model.ContentBlockAttachmentDownloadAudienceSegment{}).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	rows := make([]model.ContentBlockAttachmentDownloadAudienceSegment, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, model.ContentBlockAttachmentDownloadAudienceSegment{BlockID: *selector.BlockID, ReferencePath: *selector.ReferencePath, AudienceSegmentID: id, CreatedAt: now})
	}
	return s.tx.WithContext(ctx).Create(&rows).Error
}
func (s *store) ActivePolicySegments(ctx context.Context, ids []string) ([]domain.PolicySegment, error) {
	if s.ports.PolicyAudiences == nil {
		return nil, fmt.Errorf("File policy audience access is required")
	}
	return s.ports.PolicyAudiences.ActivePolicySegments(ctx, s.tx, ids)
}
func (s *store) ValidatePolicySegments(ctx context.Context, ids []string) error {
	if s.ports.PolicyAudiences == nil {
		return fmt.Errorf("File policy audience access is required")
	}
	return s.ports.PolicyAudiences.ValidatePolicySegments(ctx, s.tx, ids)
}
func (s *store) RequirePolicyOwner(ctx context.Context, kind, id string, edit bool) error {
	if s.ports.PolicyOwners == nil {
		return fmt.Errorf("File policy owner access is required")
	}
	if edit {
		return s.ports.PolicyOwners.RequirePolicyEdit(ctx, s.tx, kind, id)
	}
	return s.ports.PolicyOwners.RequirePolicyView(ctx, s.tx, kind, id)
}
func (s *store) PublishPolicyChange(ctx context.Context, signal domain.PolicyChangeSignal) error {
	if s.ports.PolicySignals == nil {
		return fmt.Errorf("File policy signal publisher is required")
	}
	return s.ports.PolicySignals.PublishPolicyChange(ctx, s.tx, signal)
}

type PolicyAudiences interface {
	ActivePolicySegments(context.Context, *gorm.DB, []string) ([]domain.PolicySegment, error)
	ValidatePolicySegments(context.Context, *gorm.DB, []string) error
}
