// Package audience contains composition-only adapters for the Audience domain.
package audience

import (
	"context"

	"gorm.io/gorm"

	audiencedomain "github.com/echovisionlab/geul-api/internal/audience"
	audiencepostgres "github.com/echovisionlab/geul-api/internal/audience/postgres"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/campaign"
)

// RecipientCounter adapts the delivery-owned recipient selection query to the
// estimate required by the Audience domain.
type RecipientCounter struct {
	db      *gorm.DB
	spiceDB *auth.SpiceDBClient
}

var _ audiencedomain.RecipientCounter = RecipientCounter{}

func NewRecipientCounter(db *gorm.DB, spiceDB *auth.SpiceDBClient) RecipientCounter {
	return RecipientCounter{db: db, spiceDB: spiceDB}
}

func (c RecipientCounter) Count(
	ctx context.Context,
	segment audiencedomain.Segment,
) (int64, error) {
	row := audiencepostgres.SegmentRow(segment)
	return campaign.CountBulkEmailRecipientsForAudienceSegment(ctx, c.db, c.spiceDB, &row)
}
