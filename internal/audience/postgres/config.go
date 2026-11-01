package postgres

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// LoadSegmentConfigs loads the normalized relation-backed configuration for
// each non-nil segment. Campaign delivery and download-policy matching call
// it in their own transactions.
func LoadSegmentConfigs(ctx context.Context, db *gorm.DB, segments []*model.AudienceSegment) error {
	byID := make(map[string]*model.AudienceSegment, len(segments))
	ids := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == nil {
			continue
		}
		segment.Config = model.AudienceSegmentConfig{
			CreatedAfter:  segment.CreatedAfter,
			CreatedBefore: segment.CreatedBefore,
		}
		byID[segment.ID] = segment
		ids = append(ids, segment.ID)
	}
	if len(ids) == 0 {
		return nil
	}

	var userTags []model.AudienceSegmentUserTag
	if err := db.WithContext(ctx).Where("audience_segment_id IN ?", ids).Find(&userTags).Error; err != nil {
		return err
	}
	for _, association := range userTags {
		if segment := byID[association.AudienceSegmentID]; segment != nil {
			segment.Config.MemberTagIDs = append(segment.Config.MemberTagIDs, association.UserTagID)
		}
	}

	var userRoles []model.AudienceSegmentUserRole
	if err := db.WithContext(ctx).Where("audience_segment_id IN ?", ids).Find(&userRoles).Error; err != nil {
		return err
	}
	for _, association := range userRoles {
		if segment := byID[association.AudienceSegmentID]; segment != nil {
			segment.Config.AccountRoles = append(segment.Config.AccountRoles, association.Role)
		}
	}

	var excludedMembers []model.AudienceSegmentExcludedMember
	if err := db.WithContext(ctx).Where("audience_segment_id IN ?", ids).Find(&excludedMembers).Error; err != nil {
		return err
	}
	for _, association := range excludedMembers {
		if segment := byID[association.AudienceSegmentID]; segment != nil {
			segment.Config.ExcludeMemberIDs = append(segment.Config.ExcludeMemberIDs, association.MemberID)
		}
	}
	for _, segment := range segments {
		if segment != nil {
			sortSegmentConfig(&segment.Config)
		}
	}
	return nil
}

// LoadSegmentConfig loads the normalized relation-backed configuration for a
// segment.
func LoadSegmentConfig(ctx context.Context, db *gorm.DB, segment *model.AudienceSegment) error {
	return LoadSegmentConfigs(ctx, db, []*model.AudienceSegment{segment})
}

func sortSegmentConfig(config *model.AudienceSegmentConfig) {
	config.MemberTagIDs = sliceutil.SortedUniqueTrimmed(config.MemberTagIDs)
	config.AccountRoles = sliceutil.SortedUniqueTrimmed(config.AccountRoles)
	config.ExcludeMemberIDs = sliceutil.SortedUniqueTrimmed(config.ExcludeMemberIDs)
}

// unknownReferenceError is a foreign-key violation of a filter relation,
// kept in the chain behind audience.ErrUnknownReference.
type unknownReferenceError struct{ error }

func (err unknownReferenceError) Is(target error) bool { return target == audience.ErrUnknownReference }
func (err unknownReferenceError) Unwrap() error        { return err.error }

func (s store) ReplaceConfig(_ context.Context, segmentID string, config audience.Config) error {
	err := replaceSegmentRelations(s.tx, segmentID, config)
	if dberrors.IsForeignKeyViolation(err) {
		return unknownReferenceError{err}
	}
	return err
}

func replaceSegmentRelations(tx *gorm.DB, segmentID string, config audience.Config) error {
	for _, association := range (structured.Values{
		&model.AudienceSegmentUserTag{},
		&model.AudienceSegmentUserRole{},
		&model.AudienceSegmentExcludedMember{},
	}) {
		if err := tx.Where("audience_segment_id = ?", segmentID).Delete(association).Error; err != nil {
			return err
		}
	}
	if len(config.MemberTagIDs) > 0 {
		rows := make([]model.AudienceSegmentUserTag, 0, len(config.MemberTagIDs))
		for _, tagID := range config.MemberTagIDs {
			rows = append(rows, model.AudienceSegmentUserTag{AudienceSegmentID: segmentID, UserTagID: tagID})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
	}
	if len(config.AccountRoles) > 0 {
		rows := make([]model.AudienceSegmentUserRole, 0, len(config.AccountRoles))
		for _, role := range config.AccountRoles {
			rows = append(rows, model.AudienceSegmentUserRole{AudienceSegmentID: segmentID, Role: role})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
	}
	if len(config.ExcludeMemberIDs) > 0 {
		rows := make([]model.AudienceSegmentExcludedMember, 0, len(config.ExcludeMemberIDs))
		for _, memberID := range config.ExcludeMemberIDs {
			rows = append(rows, model.AudienceSegmentExcludedMember{AudienceSegmentID: segmentID, MemberID: memberID})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
	}
	return nil
}
