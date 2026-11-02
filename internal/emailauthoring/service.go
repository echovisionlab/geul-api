package emailauthoring

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// TestEmail is an administrator's template test send, queued for the email
// worker.
type TestEmail struct {
	Recipient     string
	TemplateID    string
	Data          map[string]string
	MessageID     string
	ActorMemberID string
	// Locale is the requested locale; nil renders the source locale.
	Locale *string
}

// TestEmailQueue queues an administrator's template test send.
type TestEmailQueue interface {
	QueueTestEmail(context.Context, TestEmail) error
}

// LocaleNormalizer normalizes a requested locale to a supported one, nil
// when none matches.
type LocaleNormalizer interface {
	NormalizeSupportedLocale(string) *string
}

// Deps are the collaborators of the Email Template and Email Layout
// management use cases. Every field except CDNDomain and SiteOrigin is
// required; NewService rejects a missing one.
type Deps struct {
	Units   mutation.UnitOfWork[Store] // emailauthoringpostgres.NewUnits(db, ports)
	SpiceDB mutation.Authorizer
	Fence   identitystate.Fence // identitystate.PostgresFence{}
	Audit   domainaudit.Appender
	// TestEmails queues an administrator's template test send.
	TestEmails TestEmailQueue
	Locales    LocaleNormalizer
	// CDNDomain is the asset host of preview render data; empty keeps
	// asset links relative.
	CDNDomain string
	// SiteOrigin is the site origin of preview render data; empty keeps
	// site links relative.
	SiteOrigin string
}

func (d Deps) validate() error {
	return requireDependencies("email authoring", map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
		"TestEmails": d.TestEmails == nil, "Locales": d.Locales == nil,
	})
}

func requireDependencies(name string, absent map[string]bool) error {
	var missing []string
	for field, isAbsent := range absent {
		if isAbsent {
			missing = append(missing, field)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	slices.Sort(missing)
	return errors.New(name + " dependencies are required: " + strings.Join(missing, ", "))
}

// Service holds the Email Template and Email Layout management use cases.
// The Connect adapters translate their requests into its commands and
// queries.
type Service struct {
	units      mutation.UnitOfWork[Store]
	spiceDB    mutation.Authorizer
	testEmails TestEmailQueue
	locales    LocaleNormalizer
	mutations  *mutation.StoreRunner[Store]
	cdnDomain  string
	siteOrigin string
}

// NewService validates deps and returns the management use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		units: deps.Units, spiceDB: deps.SpiceDB, testEmails: deps.TestEmails, locales: deps.Locales,
		mutations: mutations, cdnDomain: deps.CDNDomain,
		siteOrigin: strings.TrimRight(strings.TrimSpace(deps.SiteOrigin), "/"),
	}, nil
}

// InternalDeps are the collaborators of the collaboration and AI document
// use cases of Email Templates and Email Layouts. Every field is required;
// NewInternalService rejects a missing one.
type InternalDeps struct {
	Units   mutation.UnitOfWork[Store]
	SpiceDB mutation.Authorizer
	Fence   identitystate.Fence
	Audit   domainaudit.Appender
}

func (d InternalDeps) validate() error {
	return requireDependencies("internal email authoring", map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
	})
}

// InternalService holds the collaboration and AI document use cases of
// Email Templates and Email Layouts.
type InternalService struct {
	units     mutation.UnitOfWork[Store]
	spiceDB   mutation.Authorizer
	mutations *mutation.StoreRunner[Store]
}

// NewInternalService validates deps and returns the collaboration and AI
// document use cases.
func NewInternalService(deps InternalDeps) (*InternalService, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &InternalService{units: deps.Units, spiceDB: deps.SpiceDB, mutations: mutations}, nil
}

// requireAdminCan authorizes an administrator read; an ID that cannot name
// the resource is rejected first.
func (s *Service) requireAdminCan(ctx context.Context, can policyv1.Can, canErr error) error {
	if canErr != nil {
		return domainerr.InvalidField("id", canErr.Error())
	}
	return authz.RequireAdminCan(ctx, s.spiceDB, can)
}

// adminAccess is the in-transaction authority of an administrator mutation
// of one resource; an ID that cannot name it is rejected before any lock.
func adminAccess(can policyv1.Can, err error) (mutation.Access, error) {
	if err != nil {
		return mutation.Access{}, domainerr.InvalidField("id", err.Error())
	}
	return mutation.FreshAdminCan(can, nil), nil
}

// renderData adds the site defaults to a preview's render data, read in a
// snapshot of its own because a failed read is logged and ignored.
func (s *Service) renderData(ctx context.Context, requestedLocale string, input map[string]string) map[string]string {
	var data map[string]string
	_ = s.units.Read(ctx, func(store Store) error {
		data = store.EmailRenderData(ctx, s.cdnDomain, s.siteOrigin, requestedLocale, input)
		return nil
	})
	if data == nil {
		data = input
	}
	return data
}
