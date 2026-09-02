package persistencecheckpoint

import (
	"context"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"gorm.io/gorm"
)

// ContributorFence rechecks the current authority of collaboration
// contributors at the final domain persistence boundary.
type ContributorFence interface {
	RequireCurrentContributors(
		context.Context,
		*gorm.DB,
		intrav1.CollaborationResourceType,
		string,
		[]string,
	) error
}
