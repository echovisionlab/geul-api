package public

import (
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"gorm.io/gorm"
)

// Deps are the collaborators of the public Member use cases. Every field
// except CDNDomain is required; NewService rejects a missing one.
type Deps struct {
	DB *gorm.DB
	// SpiceDB is the authority for the extended-profile and author gates.
	SpiceDB *auth.SpiceDBClient
	Audit   domainaudit.Appender
	// TokenSecret verifies newsletter unsubscribe tokens.
	TokenSecret string
	// CDNDomain prefixes avatar URLs; empty keeps them relative.
	CDNDomain string
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Audit": d.Audit == nil,
		"TokenSecret": strings.TrimSpace(d.TokenSecret) == "",
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public member dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the public Member use cases: profile and author reads, and
// the token-authenticated newsletter opt-out.
type Service struct {
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	tokenSecret string
	cdnDomain   string
	mutations   *mutation.Runner
}

// NewService validates deps and returns the public Member use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{db: deps.DB, spiceDB: deps.SpiceDB, tokenSecret: deps.TokenSecret, cdnDomain: deps.CDNDomain, mutations: mutations}, nil
}
