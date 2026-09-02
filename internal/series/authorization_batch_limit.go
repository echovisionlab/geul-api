package series

import (
	"fmt"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const maxSpiceDBAtomicRelationshipMutations = 1000

func validateResourceDeletionAuthorizationBatchSize(resourceName string, deleted, restored []policyv1.RelationshipMutation) error {
	if len(deleted) <= maxSpiceDBAtomicRelationshipMutations && len(restored) <= maxSpiceDBAtomicRelationshipMutations {
		return nil
	}
	return errs.FailedPrecondition(fmt.Sprintf(
		"%s has too many authorization relationships to delete atomically; remove participant relationships or reparent dependent resources first",
		resourceName,
	))
}
