package referencecatalog

import (
	stderrors "errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// Deps are the collaborators of the Reference Catalog use cases; all are
// required.
type Deps struct {
	Units   mutation.UnitOfWork[Store] // referencecatalogpostgres.NewUnits(db, ports)
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
		return stderrors.New("reference catalog dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the administrator use cases of the Category, Tag, Genre,
// Style, Format, Client and Map Place catalogs. Connect and MCP adapters
// translate their requests into its commands and queries.
type Service struct {
	units     mutation.UnitOfWork[Store]
	spiceDB   mutation.Authorizer
	mutations *mutation.StoreRunner[Store]
}

// NewService validates deps and returns the Reference Catalog use cases.
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

// Page is one page of a catalog list.
type Page[T any] struct {
	Items      []T
	Pagination Pagination
}

// Pagination is where a page sits among every match, as the API reports it.
type Pagination struct {
	Total   int32
	Limit   int32
	Offset  int32
	HasMore bool
}

// paginationOf is the position of the page at limit and offset among total
// matches.
func paginationOf(limit, offset int32, total int64) Pagination {
	return Pagination{
		Total: int32(total), Limit: limit, Offset: offset,
		HasMore: int64(offset)+int64(limit) < total,
	}
}
