package emaildelivery

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// MailAdapterCache is the process-local provider cache a mail adapter
// change invalidates (email.AdapterLoader).
type MailAdapterCache interface {
	InvalidateCache()
}

// MailAdapterChangeType names what happened to a mail adapter. The values
// are the API enum names the change event carries.
type MailAdapterChangeType string

const (
	MailAdapterCreated MailAdapterChangeType = "MAIL_ADAPTER_CHANGE_TYPE_CREATED"
	MailAdapterUpdated MailAdapterChangeType = "MAIL_ADAPTER_CHANGE_TYPE_UPDATED"
	MailAdapterDeleted MailAdapterChangeType = "MAIL_ADAPTER_CHANGE_TYPE_DELETED"
)

// MailAdapterChange is a committed mail adapter change.
type MailAdapterChange struct {
	AdapterID string
	Type      MailAdapterChangeType
	At        time.Time
}

// MailAdapterChangePublisher tells other processes that a mail adapter
// changed, so they invalidate their provider caches.
type MailAdapterChangePublisher interface {
	PublishMailAdapterChanged(context.Context, MailAdapterChange) error
}

// MailProviders opens the provider client of an adapter configuration for
// one verification or test send.
type MailProviders interface {
	Open(MailAdapter) (MailProvider, error)
}

// MailProvider is a provider client opened for one send. Close releases the
// connections it keeps pooled.
type MailProvider interface {
	email.Sender
	Close() error
}

// Deps are the collaborators of the administrator mail adapter and email
// suppression use cases. Every field but Clock is required; NewService
// rejects a missing one.
type Deps struct {
	Units          mutation.UnitOfWork[Store] // emaildeliverypostgres.NewUnits(db)
	SpiceDB        mutation.Authorizer
	Fence          identitystate.Fence
	Audit          domainaudit.Appender
	Providers      MailProviders
	AdapterCache   MailAdapterCache
	AdapterChanges MailAdapterChangePublisher
	Clock          func() time.Time // optional: time.Now
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
		"Providers": d.Providers == nil, "AdapterCache": d.AdapterCache == nil, "AdapterChanges": d.AdapterChanges == nil,
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
	units          mutation.UnitOfWork[Store]
	spiceDB        mutation.Authorizer
	providers      MailProviders
	adapterCache   MailAdapterCache
	adapterChanges MailAdapterChangePublisher
	mutations      *mutation.StoreRunner[Store]
	now            func() time.Time
}

// NewService validates deps and returns the email delivery use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	now := deps.Clock
	if now == nil {
		now = time.Now
	}
	return &Service{
		units:          deps.Units,
		spiceDB:        deps.SpiceDB,
		providers:      deps.Providers,
		adapterCache:   deps.AdapterCache,
		adapterChanges: deps.AdapterChanges,
		mutations:      mutations,
		now:            now,
	}, nil
}

// freshAdmin is the in-transaction authority of an administrator mutation.
func freshAdmin(can policyv1.Can, err error) mutation.Access {
	return mutation.FreshAdminCan(can, err)
}
