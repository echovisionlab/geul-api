package sharelink

import (
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// Deps are the collaborators of the Share Link use cases. Every field is
// required; NewService rejects a missing one.
type Deps struct {
	Units   mutation.UnitOfWork[Store] // sharelinkpostgres.NewUnits(db)
	SpiceDB mutation.Authorizer
	Fence   identitystate.Fence // identitystate.PostgresFence{}
	Audit   domainaudit.Appender
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("share link dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Share Link management use cases.
type Service struct {
	units     mutation.UnitOfWork[Store]
	spiceDB   mutation.Authorizer
	mutations *mutation.StoreRunner[Store]
}

// NewService validates deps and returns the Share Link use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{units: deps.Units, spiceDB: deps.SpiceDB, mutations: mutations}, nil
}
