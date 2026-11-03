package criteria

import (
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Deps are the collaborators of the Criteria use cases. Every field is
// required; NewService rejects a missing one.
type Deps struct {
	// Units opens the transactions and read snapshots of the Store
	// (criteria/postgres.NewUnits).
	Units   mutation.UnitOfWork[Store]
	SpiceDB mutation.Authorizer
	// Fence is the identity fence (identitystate.PostgresFence).
	Fence identitystate.Fence
	Audit domainaudit.Appender
	// RecipientCounter estimates the recipients of a criteria.
	RecipientCounter RecipientCounter
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil,
		"Audit": d.Audit == nil, "RecipientCounter": d.RecipientCounter == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("criteria dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Criteria use cases. The Connect adapter translates its
// requests into their commands and queries.
type Service struct {
	units            mutation.UnitOfWork[Store]
	spiceDB          mutation.Authorizer
	recipientCounter RecipientCounter
	mutations        *mutation.StoreRunner[Store]
}

// NewService validates deps and returns the Criteria use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		units:            deps.Units,
		spiceDB:          deps.SpiceDB,
		recipientCounter: deps.RecipientCounter,
		mutations:        mutations,
	}, nil
}

// freshAdmin is the in-transaction authority of every criteria mutation.
func freshAdmin(can policyv1.Can) mutation.Access {
	return mutation.FreshAdminCan(can, nil)
}
