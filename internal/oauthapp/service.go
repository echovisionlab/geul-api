package oauthapp

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// Deps are the collaborators of the OAuth app use cases. Secrets defaults to
// crypto/rand; every other field is required.
type Deps struct {
	Units       mutation.UnitOfWork[Store] // oauthapppostgres.NewUnits(db)
	SpiceDB     mutation.Authorizer
	Fence       identitystate.Fence // identitystate.PostgresFence{}
	Audit       domainaudit.Appender
	Clients     Clients
	Eligibility Eligibility
	// MCPResourceURL is the resource connected AI tools must target; a tool
	// that targets another one is stale.
	MCPResourceURL string
	Secrets        io.Reader
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
		"Clients": d.Clients == nil, "Eligibility": d.Eligibility == nil, "MCPResourceURL": strings.TrimSpace(d.MCPResourceURL) == "",
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("OAuth app dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the OAuth app use cases. Connect and MCP adapters translate
// their requests into its commands and queries.
type Service struct {
	units          mutation.UnitOfWork[Store]
	spiceDB        mutation.Authorizer
	mutations      *mutation.StoreRunner[Store]
	clients        Clients
	eligibility    Eligibility
	mcpResourceURL string
	secrets        io.Reader
}

// NewService validates deps and returns the OAuth app use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	secrets := deps.Secrets
	if secrets == nil {
		secrets = rand.Reader
	}
	return &Service{
		units: deps.Units, spiceDB: deps.SpiceDB, mutations: mutations, clients: deps.Clients,
		eligibility: deps.Eligibility, mcpResourceURL: strings.TrimSpace(deps.MCPResourceURL), secrets: secrets,
	}, nil
}

// newSecret returns a random client secret of 256 bits.
func (s *Service) newSecret() (string, error) {
	buffer := make([]byte, 32)
	if _, err := io.ReadFull(s.secrets, buffer); err != nil {
		return "", fmt.Errorf("generate client secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
