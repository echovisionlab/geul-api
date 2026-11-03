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
type PolicyCriteriaRelations interface {
	InsertTrack(context.Context, *gorm.DB, string, []string, time.Time) error
	InsertAttachment(context.Context, *gorm.DB, string, string, []string, time.Time) error
}

func (s *store) ResolveDownloadPolicyRelation(ctx context.Context, selector domain.PolicySelector, lock bool) (domain.PolicyRelation, error) {
	var row struct {
		FileID                   string
		Access                   mediaasset.FileDownloadAccess `gorm:"column:download_access"`
		ResourceType, ResourceID string
	}
	query := s.tx.WithContext(ctx)
	switch selector.OwnerKind {
	case "post", "page", "work", "program_event":
		query = query.Table("content_block_attachment AS attachment").Select("attachment.file_id, attachment.download_access, ? AS resource_type, owner.id AS resource_id", selector.OwnerKind).Joins("JOIN content_block AS block ON block.id = attachment.block_id").Joins("JOIN "+selector.OwnerKind+" AS owner ON owner.content_document_id = block.document_id").Joins("JOIN file ON file.id = attachment.file_id AND file.delete_requested_at IS NULL").Where("owner.id = ?", selector.EntityID).Where("block.id = ? AND block.kind = 'file'", *selector.BlockID).Where("attachment.reference_path = ? AND attachment.selector_kind = 'active'", *selector.ReferencePath)
		if lock {
			query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Table: clause.Table{Name: "attachment"}})
		}
	case "track":
		query = query.Table("track").Select("track.audio_original_file_id AS file_id, track.download_access, 'release' AS resource_type, track.release_id AS resource_id").Joins("JOIN file ON file.id = track.audio_original_file_id AND file.delete_requested_at IS NULL").Where("track.id = ?", selector.EntityID)
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
	return domain.PolicyRelation{Selector: selector, FileID: row.FileID, Access: row.Access, ResourceType: row.ResourceType, ResourceID: row.ResourceID}, err
}
func (s *store) DownloadPolicyCriteriaIDs(ctx context.Context, selector domain.PolicySelector) ([]string, error) {
	var ids []string
	query := s.tx.WithContext(ctx)
	if selector.OwnerKind == "track" {
		query = query.Table("track_download_member_criteria").Where("track_id = ?", selector.EntityID)
	} else {
		query = query.Table("content_block_attachment_download_member_criteria").Where("block_id = ? AND reference_path = ?", *selector.BlockID, *selector.ReferencePath)
	}
	err := query.Order("member_criteria_id ASC").Pluck("member_criteria_id", &ids).Error
	return ids, err
}
func (s *store) SetDownloadPolicy(ctx context.Context, selector domain.PolicySelector, access mediaasset.FileDownloadAccess, ids []string, now time.Time) error {
	if len(ids) > 0 && s.ports.PolicyCriteriaRelations == nil {
		return fmt.Errorf("File download policy criteria writers are required")
	}
	if selector.OwnerKind == "track" {
		if err := s.tx.WithContext(ctx).Model(&model.Track{}).Where("id = ?", selector.EntityID).Update("download_access", string(access)).Error; err != nil {
			return err
		}
		if err := s.tx.WithContext(ctx).Where("track_id = ?", selector.EntityID).Delete(&model.TrackDownloadMemberCriteria{}).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		return s.ports.PolicyCriteriaRelations.InsertTrack(ctx, s.tx, selector.EntityID, ids, now)
	}
	if err := s.tx.WithContext(ctx).Table("content_block_attachment").Where("block_id = ? AND reference_path = ?", *selector.BlockID, *selector.ReferencePath).Update("download_access", string(access)).Error; err != nil {
		return err
	}
	if err := s.tx.WithContext(ctx).Where("block_id = ? AND reference_path = ?", *selector.BlockID, *selector.ReferencePath).Delete(&model.ContentBlockAttachmentDownloadMemberCriteria{}).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	return s.ports.PolicyCriteriaRelations.InsertAttachment(ctx, s.tx, *selector.BlockID, *selector.ReferencePath, ids, now)
}
func (s *store) ActivePolicyCriteriaSet(ctx context.Context, ids []string) ([]domain.PolicyCriteria, error) {
	if s.ports.PolicyCriteriaReader == nil {
		return nil, fmt.Errorf("File policy access access is required")
	}
	return s.ports.PolicyCriteriaReader.ActivePolicyCriteriaSet(ctx, s.tx, ids)
}
func (s *store) ValidatePolicyCriteriaSet(ctx context.Context, ids []string) error {
	if s.ports.PolicyCriteriaReader == nil {
		return fmt.Errorf("File policy access access is required")
	}
	return s.ports.PolicyCriteriaReader.ValidatePolicyCriteriaSet(ctx, s.tx, ids)
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

type PolicyCriteriaReader interface {
	ActivePolicyCriteriaSet(context.Context, *gorm.DB, []string) ([]domain.PolicyCriteria, error)
	ValidatePolicyCriteriaSet(context.Context, *gorm.DB, []string) error
}
