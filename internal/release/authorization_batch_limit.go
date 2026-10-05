package release

import errs "github.com/echovisionlab/geul-api/internal/errors"

// SpiceDB v1.56 accepts at most 1,000 updates and 1,000 preconditions in one
// atomic relationship write.
const maxSpiceDBAtomicRelationshipMutations = 1000

func validateAtomicAuthorizationRelationshipBatchSize(updateCount, preconditionCount int, message string) error {
	if updateCount <= maxSpiceDBAtomicRelationshipMutations && preconditionCount <= maxSpiceDBAtomicRelationshipMutations {
		return nil
	}
	return errs.FailedPrecondition(message)
}
