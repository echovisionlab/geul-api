package authentication

import (
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
)

// Deps are the collaborators of the gateway authorization and login role
// use cases. Every field is required; NewService rejects a missing one.
type Deps struct {
	DB      *gorm.DB
	SpiceDB *auth.SpiceDBClient
	Audit   domainaudit.Appender
	Roles   DirectRoleTransition
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Audit": d.Audit == nil, "Roles": d.Roles == nil,
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
	db      *gorm.DB
	spiceDB *auth.SpiceDBClient
	audit   domainaudit.Appender
	roles   DirectRoleTransition
}

// NewService validates deps and returns the authentication use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{db: deps.DB, spiceDB: deps.SpiceDB, audit: deps.Audit, roles: deps.Roles}, nil
}
