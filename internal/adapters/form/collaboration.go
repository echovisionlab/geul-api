package form

import (
	"context"

	"gorm.io/gorm"

	collaborationadapter "github.com/echovisionlab/geul-api/internal/adapters/collaboration"
	"github.com/echovisionlab/geul-api/internal/auth"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

// Collaboration authorizes a Form collaboration save by its origin Member's
// current collaboration permission.
type Collaboration struct {
	spiceDB *auth.SpiceDBClient
}

func NewCollaboration(spiceDB *auth.SpiceDBClient) *Collaboration {
	return &Collaboration{spiceDB: spiceDB}
}

func (c *Collaboration) RequireCurrentContributor(ctx context.Context, tx *gorm.DB, formID, memberID string) error {
	return collaborationadapter.NewCheckpointFence(tx, c.spiceDB).RequireCurrentContributors(
		ctx, tx, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_FORM, formID, []string{memberID},
	)
}

var _ formdomain.Collaboration = (*Collaboration)(nil)
