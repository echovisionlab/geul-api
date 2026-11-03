package postgres

import (
	"slices"

	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	"github.com/echovisionlab/geul-api/internal/model"
)

// CriteriaOf maps a criteria row to the domain criteria. The creation window
// comes from the row's columns; the filter relations from the row's Config,
// which LoadCriteriaConfigs fills.
func CriteriaOf(row model.MemberCriteria) membercriteria.Criteria {
	return membercriteria.Criteria{
		ID:          row.ID,
		Name:        row.Name,
		Description: row.Description,
		Type:        membercriteria.Kind(row.Kind),
		Config: membercriteria.Config{
			MemberTagIDs:     slices.Clone(row.Config.MemberTagIDs),
			AccountRoles:     slices.Clone(row.Config.AccountRoles),
			CreatedAfter:     row.CreatedAfter,
			CreatedBefore:    row.CreatedBefore,
			ExcludeMemberIDs: slices.Clone(row.Config.ExcludeMemberIDs),
		},
		ArchivedAt: row.ArchivedAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

// CriteriaRow maps a domain criteria to its row, with Config filled as
// LoadCriteriaConfigs fills it.
func CriteriaRow(criteria membercriteria.Criteria) model.MemberCriteria {
	return model.MemberCriteria{
		ID:            criteria.ID,
		Name:          criteria.Name,
		Description:   criteria.Description,
		Kind:          model.MemberCriteriaKind(criteria.Type),
		CreatedAfter:  criteria.Config.CreatedAfter,
		CreatedBefore: criteria.Config.CreatedBefore,
		Config: model.MemberCriteriaConfig{
			MemberTagIDs:     slices.Clone(criteria.Config.MemberTagIDs),
			AccountRoles:     slices.Clone(criteria.Config.AccountRoles),
			CreatedAfter:     criteria.Config.CreatedAfter,
			CreatedBefore:    criteria.Config.CreatedBefore,
			ExcludeMemberIDs: slices.Clone(criteria.Config.ExcludeMemberIDs),
		},
		ArchivedAt: criteria.ArchivedAt,
		CreatedAt:  criteria.CreatedAt,
		UpdatedAt:  criteria.UpdatedAt,
	}
}
