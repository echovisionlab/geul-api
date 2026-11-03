package filemediapolicy

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	criteriapostgres "github.com/echovisionlab/geul-api/internal/member/criteria/postgres"
	"gorm.io/gorm"
)

type CriteriaReader struct{}

func (CriteriaReader) ActivePolicyCriteriaSet(ctx context.Context, tx *gorm.DB, ids []string) ([]domain.PolicyCriteria, error) {
	rows, err := criteriapostgres.ActivePolicyCriteriaSet(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]domain.PolicyCriteria, len(rows))
	for i, row := range rows {
		result[i] = domain.PolicyCriteria{ID: row.ID, Name: row.Name, Description: row.Description, Kind: strings.TrimSpace(string(row.Type)), Eligible: row.GrantsAuthenticatedAccess(), ArchivedAt: row.ArchivedAt}
	}
	return result, nil
}
func (CriteriaReader) ValidatePolicyCriteriaSet(ctx context.Context, tx *gorm.DB, ids []string) error {
	return criteriapostgres.ValidateAuthenticatedAccessCriteriaIDs(ctx, tx, ids)
}
