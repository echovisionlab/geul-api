package referencecatalog

import (
	"context"
	stderrors "errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// ClientWorks reads the Work-owned relations that reference Clients.
type ClientWorks interface {
	// CountByClient returns how many Works reference each Client; a Client
	// without Works is absent.
	CountByClient(ctx context.Context, db *gorm.DB, clientIDs []string) (map[string]int32, error)
}

// Deps are the collaborators of the Reference Catalog use cases; all are
// required.
type Deps struct {
	DB          *gorm.DB
	SpiceDB     *auth.SpiceDBClient
	Audit       domainaudit.Appender
	Assets      Assets
	Members     MemberSummaries
	MenuTargets MenuTargets
	ClientWorks ClientWorks
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Audit": d.Audit == nil, "Assets": d.Assets == nil,
		"Members": d.Members == nil, "MenuTargets": d.MenuTargets == nil, "ClientWorks": d.ClientWorks == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return stderrors.New("reference catalog dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the administrator use cases of the Category, Tag, Genre,
// Style, Format, Client and Map Place catalogs. Connect and MCP adapters
// translate their requests into its commands and queries.
type Service struct {
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	assets      Assets
	members     MemberSummaries
	menuTargets MenuTargets
	clientWorks ClientWorks
	mutations   *mutation.Runner
}

// NewService validates deps and returns the Reference Catalog use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, assets: deps.Assets, members: deps.Members,
		menuTargets: deps.MenuTargets, clientWorks: deps.ClientWorks, mutations: mutations,
	}, nil
}

// Page is one page of a catalog list.
type Page[T any] struct {
	Items      []T
	Pagination *commonv1.PaginationResponse
}
