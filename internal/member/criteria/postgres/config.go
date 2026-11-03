package postgres

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// LoadCriteriaConfigs loads the normalized relation-backed configuration for
// each non-nil criteria. Campaign delivery and download-policy matching call
// it in their own transactions.
func LoadCriteriaConfigs(ctx context.Context, db *gorm.DB, criteriaSet []*model.MemberCriteria) error {
	byID := make(map[string]*model.MemberCriteria, len(criteriaSet))
	ids := make([]string, 0, len(criteriaSet))
	for _, criteria := range criteriaSet {
		if criteria == nil {
			continue
		}
		criteria.Config = model.MemberCriteriaConfig{
			CreatedAfter:  criteria.CreatedAfter,
			CreatedBefore: criteria.CreatedBefore,
		}
		byID[criteria.ID] = criteria
		ids = append(ids, criteria.ID)
	}
	if len(ids) == 0 {
		return nil
	}

	var userTags []model.MemberCriteriaTag
	if err := db.WithContext(ctx).Where("member_criteria_id IN ?", ids).Find(&userTags).Error; err != nil {
		return err
	}
	for _, association := range userTags {
		if criteria := byID[association.MemberCriteriaID]; criteria != nil {
			criteria.Config.MemberTagIDs = append(criteria.Config.MemberTagIDs, association.UserTagID)
		}
	}

	var userRoles []model.MemberCriteriaRole
	if err := db.WithContext(ctx).Where("member_criteria_id IN ?", ids).Find(&userRoles).Error; err != nil {
		return err
	}
	for _, association := range userRoles {
		if criteria := byID[association.MemberCriteriaID]; criteria != nil {
			criteria.Config.AccountRoles = append(criteria.Config.AccountRoles, association.Role)
		}
	}

	var excludedMembers []model.MemberCriteriaExcludedMember
	if err := db.WithContext(ctx).Where("member_criteria_id IN ?", ids).Find(&excludedMembers).Error; err != nil {
		return err
	}
	for _, association := range excludedMembers {
		if criteria := byID[association.MemberCriteriaID]; criteria != nil {
			criteria.Config.ExcludeMemberIDs = append(criteria.Config.ExcludeMemberIDs, association.MemberID)
		}
	}
	for _, criteria := range criteriaSet {
		if criteria != nil {
			sortCriteriaConfig(&criteria.Config)
		}
	}
	return nil
}

// LoadCriteriaConfig loads the normalized relation-backed configuration for a
// criteria.
func LoadCriteriaConfig(ctx context.Context, db *gorm.DB, criteria *model.MemberCriteria) error {
	return LoadCriteriaConfigs(ctx, db, []*model.MemberCriteria{criteria})
}

func sortCriteriaConfig(config *model.MemberCriteriaConfig) {
	config.MemberTagIDs = sliceutil.SortedUniqueTrimmed(config.MemberTagIDs)
	config.AccountRoles = sliceutil.SortedUniqueTrimmed(config.AccountRoles)
	config.ExcludeMemberIDs = sliceutil.SortedUniqueTrimmed(config.ExcludeMemberIDs)
}

// unknownReferenceError is a foreign-key violation of a filter relation,
// kept in the chain behind membercriteria.ErrUnknownReference.
type unknownReferenceError struct{ error }

func (err unknownReferenceError) Is(target error) bool {
	return target == membercriteria.ErrUnknownReference
}
func (err unknownReferenceError) Unwrap() error { return err.error }

func (s store) ReplaceConfig(_ context.Context, criteriaID string, config membercriteria.Config) error {
	err := replaceCriteriaRelations(s.tx, criteriaID, config)
	if dberrors.IsForeignKeyViolation(err) {
		return unknownReferenceError{err}
	}
	return err
}

func replaceCriteriaRelations(tx *gorm.DB, criteriaID string, config membercriteria.Config) error {
	for _, association := range (structured.Values{
		&model.MemberCriteriaTag{},
		&model.MemberCriteriaRole{},
		&model.MemberCriteriaExcludedMember{},
	}) {
		if err := tx.Where("member_criteria_id = ?", criteriaID).Delete(association).Error; err != nil {
			return err
		}
	}
	if len(config.MemberTagIDs) > 0 {
		rows := make([]model.MemberCriteriaTag, 0, len(config.MemberTagIDs))
		for _, tagID := range config.MemberTagIDs {
			rows = append(rows, model.MemberCriteriaTag{MemberCriteriaID: criteriaID, UserTagID: tagID})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
	}
	if len(config.AccountRoles) > 0 {
		rows := make([]model.MemberCriteriaRole, 0, len(config.AccountRoles))
		for _, role := range config.AccountRoles {
			rows = append(rows, model.MemberCriteriaRole{MemberCriteriaID: criteriaID, Role: role})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
	}
	if len(config.ExcludeMemberIDs) > 0 {
		rows := make([]model.MemberCriteriaExcludedMember, 0, len(config.ExcludeMemberIDs))
		for _, memberID := range config.ExcludeMemberIDs {
			rows = append(rows, model.MemberCriteriaExcludedMember{MemberCriteriaID: criteriaID, MemberID: memberID})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
	}
	return nil
}
