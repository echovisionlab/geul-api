// Package audience contains composition-only adapters for the Audience domain.
package audience

import (
	"context"

	"gorm.io/gorm"

	audiencedomain "github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/model"
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
	segment *model.AudienceSegment,
) (int64, error) {
	return campaign.CountBulkEmailRecipientsForAudienceSegment(ctx, c.db, c.spiceDB, segment)
}

// MemberReferences adapts the Member-owned durable-reference eligibility
// query to Audience exclusion validation.
type MemberReferences struct{}

var _ audiencedomain.MemberReferences = MemberReferences{}

func (MemberReferences) EligibleIDs(
	ctx context.Context,
	db *gorm.DB,
	memberIDs []string,
) ([]string, error) {
	return authorizationtarget.EligibleMemberIDs(ctx, db, memberIDs)
}
