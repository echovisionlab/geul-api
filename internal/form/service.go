package form

import (
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
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
	DB       *gorm.DB
	SpiceDB  *auth.SpiceDBClient
	Password *crypto.PasswordHasher
	// ContentBlocks advances the Form Content Document revision.
	ContentBlocks *contentblock.Store
	Assets        Assets
	OG            OG
	Routes        Routes
	// Collaboration fences a collaboration save to its origin Member.
	Collaboration Collaboration
	Members       Members
	ShareLinks    ShareLinks
	Signals       contentsignal.Publisher
	Audit         AuditAppender
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Password": d.Password == nil,
		"ContentBlocks": d.ContentBlocks == nil, "Assets": d.Assets == nil, "OG": d.OG == nil,
		"Routes": d.Routes == nil, "Collaboration": d.Collaboration == nil, "Members": d.Members == nil,
		"ShareLinks": d.ShareLinks == nil, "Signals": d.Signals == nil, "Audit": d.Audit == nil,
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
	db      *gorm.DB
	spiceDB *auth.SpiceDBClient
	// authorization decides Form object actions; it is spiceDB.
	authorization      formPermissionChecker
	password           *crypto.PasswordHasher
	contentBlocks      *contentblock.Store
	assets             Assets
	og                 OG
	routes             Routes
	collaboration      Collaboration
	members            Members
	shareLinks         ShareLinks
	signals            contentsignal.Publisher
	audit              domainaudit.Appender
	personalDataAccess *securityaccess.Recorder
	mutations          *mutation.Runner
}

// NewService validates deps and returns the Form use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, authorization: authz.NewSpiceDBResourceChecker(deps.SpiceDB, deps.DB, "form"),
		password: deps.Password, contentBlocks: deps.ContentBlocks, assets: deps.Assets, og: deps.OG, routes: deps.Routes,
		collaboration: deps.Collaboration, members: deps.Members, shareLinks: deps.ShareLinks, signals: deps.Signals,
		audit: deps.Audit, personalDataAccess: securityaccess.New(deps.Audit), mutations: mutations,
	}, nil
}
