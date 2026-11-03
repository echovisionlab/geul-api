// Package criteria contains composition-only adapters for the Criteria domain.
package criteria

import (
	"context"

	campaignpostgres "github.com/echovisionlab/geul-api/internal/campaign/postgres"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	criteriapostgres "github.com/echovisionlab/geul-api/internal/member/criteria/postgres"
)

// RecipientCounter adapts the delivery-owned recipient selection query to the
// estimate required by the Criteria domain.
type RecipientCounter struct {
	db      *gorm.DB
	spiceDB *auth.SpiceDBClient
}

var _ membercriteria.RecipientCounter = RecipientCounter{}

func NewRecipientCounter(db *gorm.DB, spiceDB *auth.SpiceDBClient) RecipientCounter {
	return RecipientCounter{db: db, spiceDB: spiceDB}
}

func (c RecipientCounter) Count(
	ctx context.Context,
	criteria membercriteria.Criteria,
) (int64, error) {
	row := criteriapostgres.CriteriaRow(criteria)
	return campaignpostgres.CountBulkEmailRecipientsForMemberCriteria(ctx, c.db, c.spiceDB, &row)
}
