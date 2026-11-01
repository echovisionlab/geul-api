package authentication

import (
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// Deps are the collaborators of the gateway authorization and login role
// use cases. Every field is required; NewService rejects a missing one.
type Deps struct {
	Units    mutation.UnitOfWork[Store] // authenticationpostgres.NewUnits(db)
	SpiceDB  Authorizer
	Fence    identitystate.Fence // identitystate.PostgresFence{}
	Audit    domainaudit.Appender
	Roles    DirectRoleTransition
	Sessions Sessions // authenticationpostgres.NewSessions(db)
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil,
		"Audit": d.Audit == nil, "Roles": d.Roles == nil, "Sessions": d.Sessions == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("authentication dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the authentication use cases that read the database and
// SpiceDB: the gateway's coarse authorization and the login role
// bootstrap. Kratos remains AuthN; SpiceDB is the sole role authority.
type Service struct {
	mutations *mutation.StoreRunner[Store]
	spiceDB   Authorizer
	roles     DirectRoleTransition
	sessions  Sessions
}

// NewService validates deps and returns the authentication use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{mutations: mutations, spiceDB: deps.SpiceDB, roles: deps.Roles, sessions: deps.Sessions}, nil
}
