package filemediapolicy

import (
	"context"
	"strings"

	audiencepostgres "github.com/echovisionlab/geul-api/internal/audience/postgres"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"gorm.io/gorm"
)

type Audiences struct{}

func (Audiences) ActivePolicySegments(ctx context.Context, tx *gorm.DB, ids []string) ([]domain.PolicySegment, error) {
	rows, err := audiencepostgres.ActivePolicySegments(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]domain.PolicySegment, len(rows))
	for i, row := range rows {
		result[i] = domain.PolicySegment{ID: row.ID, Name: row.Name, Description: row.Description, Kind: strings.TrimSpace(string(row.Type)), Eligible: row.GrantsAuthenticatedAccess(), ArchivedAt: row.ArchivedAt}
	}
	return result, nil
}
func (Audiences) ValidatePolicySegments(ctx context.Context, tx *gorm.DB, ids []string) error {
	return audiencepostgres.ValidateAuthenticatedAccessSegmentIDs(ctx, tx, ids)
}
