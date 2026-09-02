package accountadapter

import (
	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	authentication "github.com/echovisionlab/geul-api/internal/authentication"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// AccountDirectRoleTransition delegates account-owned relationship semantics
// while leaving the authentication transaction and compensation boundary with
// its consumer.
type AccountDirectRoleTransition struct{}

var _ authentication.DirectRoleTransition = AccountDirectRoleTransition{}

func (AccountDirectRoleTransition) Transition(
	subject auth.AccountIdentitySubject,
	desired policyv1.RoleID,
	previous policyv1.RoleID,
	previousFound bool,
) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	apply, err := account.RoleReplacementMutations(subject, desired)
	if err != nil {
		return nil, nil, err
	}
	compensate, err := account.RoleRestoreMutations(subject, previous, previousFound)
	if err != nil {
		return nil, nil, err
	}
	return apply, compensate, nil
}
