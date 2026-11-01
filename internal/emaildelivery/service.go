package emaildelivery

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// MailAdapterCache is the process-local provider cache a mail adapter
// change invalidates (email.AdapterLoader).
type MailAdapterCache interface {
	InvalidateCache()
}

// MailAdapterChangePublisher tells other processes that a mail adapter
// changed, so they invalidate their provider caches.
type MailAdapterChangePublisher interface {
	PublishMailAdapterChanged(context.Context, *managev1.MailAdapterChangedEvent) error
}

// Deps are the collaborators of the administrator mail adapter and email
// suppression use cases. Every field is required; NewService rejects a
// missing one.
type Deps struct {
	DB             *gorm.DB
	SpiceDB        *auth.SpiceDBClient
	AdapterCache   MailAdapterCache
	AdapterChanges MailAdapterChangePublisher
	Audit          domainaudit.Appender
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "AdapterCache": d.AdapterCache == nil,
		"AdapterChanges": d.AdapterChanges == nil, "Audit": d.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("email delivery dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the administrator mail adapter and email suppression use
// cases. The Connect adapters and MCP tools translate their requests into
// its commands and queries.
type Service struct {
	db             *gorm.DB
	spiceDB        *auth.SpiceDBClient
	adapterCache   MailAdapterCache
	adapterChanges MailAdapterChangePublisher
	mutations      *mutation.Runner
}

// NewService validates deps and returns the email delivery use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db:             deps.DB,
		spiceDB:        deps.SpiceDB,
		adapterCache:   deps.AdapterCache,
		adapterChanges: deps.AdapterChanges,
		mutations:      mutations,
	}, nil
}

// freshAdmin is the in-transaction authority of an administrator mutation.
func freshAdmin(can policyv1.Can, err error) mutation.Access {
	return mutation.FreshAdminCan(can, err)
}
