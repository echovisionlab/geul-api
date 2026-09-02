package authentication

import (
	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// DirectRoleTransition is the transaction-aware account-role boundary used by
// authentication flows. The caller supplies the observed role so one
// authzmutation batch can preserve its exact compensation ordering.
type DirectRoleTransition interface {
	Transition(
		auth.AccountIdentitySubject,
		policyv1.RoleID,
		policyv1.RoleID,
		bool,
	) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error)
}
