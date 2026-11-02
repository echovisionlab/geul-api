package form

import (
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
)

// AuditAppender persists the Form domain audit and the personal-data access
// records of submission reads.
type AuditAppender interface {
	securityaccess.Appender
	domainaudit.Appender
}

// Deps are the collaborators of the Form use cases. Every field is required;
// NewService rejects a missing one.
type Deps struct {
	Units    mutation.UnitOfWork[Store] // formpostgres.NewUnits
	SpiceDB  mutation.Authorizer
	Fence    identitystate.Fence
	Audit    AuditAppender
	Password *crypto.PasswordHasher
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil,
		"Audit": d.Audit == nil, "Password": d.Password == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("form dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Form use cases: management, submissions, the
// editor-collab document boundary and the AI document boundary. Adapters
// translate their requests into its commands and queries.
type Service struct {
	units   mutation.UnitOfWork[Store]
	spiceDB mutation.Authorizer
	// authorization decides Form object actions; it is spiceDB.
	authorization      formPermissionChecker
	password           *crypto.PasswordHasher
	personalDataAccess *securityaccess.Recorder
	mutations          *mutation.StoreRunner[Store]
}

// NewService validates deps and returns the Form use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		units: deps.Units, spiceDB: deps.SpiceDB, authorization: authz.NewSpiceDBResourceChecker(deps.SpiceDB, nil, "form"),
		password: deps.Password, personalDataAccess: securityaccess.New(deps.Audit), mutations: mutations,
	}, nil
}
