package emailauthoring

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// ContributorMembers locks the Members an AI document or collaboration write
// credits, so a concurrent Member deletion waits for the write. Member owns
// those rows (memberadapter.ContributorMembers).
type ContributorMembers interface {
	// LockMembers takes a KEY SHARE lock on each existing Member of
	// memberIDs and returns the IDs it locked.
	LockMembers(ctx context.Context, tx *gorm.DB, memberIDs []string) ([]string, error)
}

// Deps are the collaborators of the Email Template and Email Layout
// management use cases. Every field except CDNDomain and SiteOrigin is
// required; NewService rejects a missing one.
type Deps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	ContentBlocks *contentblock.Store
	// Publisher queues an administrator's template test send.
	Publisher EmailPublisher
	// Runtime localizes templates and layouts for previews and test sends.
	Runtime EmailTemplateRuntime
	// RenderData adds the site defaults to preview render data.
	RenderData EmailRenderDataBuilder
	References CampaignDeliveryReferences
	Audit      domainaudit.Appender
	// CDNDomain is the asset host of preview render data; empty keeps
	// asset links relative.
	CDNDomain string
	// SiteOrigin is the site origin of preview render data; empty keeps
	// site links relative.
	SiteOrigin string
}

func (d Deps) validate() error {
	return requireDependencies("email authoring", map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "ContentBlocks": d.ContentBlocks == nil,
		"Publisher": d.Publisher == nil, "Runtime": d.Runtime == nil, "RenderData": d.RenderData == nil,
		"References": d.References == nil, "Audit": d.Audit == nil,
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
	db            *gorm.DB
	spiceDB       *auth.SpiceDBClient
	contentBlocks *contentblock.Store
	publisher     EmailPublisher
	runtime       EmailTemplateRuntime
	renderData    EmailRenderDataBuilder
	references    CampaignDeliveryReferences
	mutations     *mutation.Runner
	cdnDomain     string
	siteOrigin    string
}

// NewService validates deps and returns the management use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, contentBlocks: deps.ContentBlocks,
		publisher: deps.Publisher, runtime: deps.Runtime, renderData: deps.RenderData,
		references: deps.References, mutations: mutations,
		cdnDomain: deps.CDNDomain, siteOrigin: strings.TrimRight(strings.TrimSpace(deps.SiteOrigin), "/"),
	}, nil
}

// InternalDeps are the collaborators of the collaboration and AI document
// use cases of Email Templates and Email Layouts. Every field is required;
// NewInternalService rejects a missing one.
type InternalDeps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	ContentBlocks *contentblock.Store
	Checkpoints   persistencecheckpoint.ContributorFence
	References    CampaignDeliveryReferences
	Members       ContributorMembers
	Audit         domainaudit.Appender
}

func (d InternalDeps) validate() error {
	return requireDependencies("internal email authoring", map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "ContentBlocks": d.ContentBlocks == nil,
		"Checkpoints": d.Checkpoints == nil, "References": d.References == nil,
		"Members": d.Members == nil, "Audit": d.Audit == nil,
	})
}

// InternalService holds the collaboration and AI document use cases of
// Email Templates and Email Layouts.
type InternalService struct {
	db            *gorm.DB
	spiceDB       *auth.SpiceDBClient
	contentBlocks *contentblock.Store
	checkpoints   persistencecheckpoint.ContributorFence
	references    CampaignDeliveryReferences
	members       ContributorMembers
	audit         domainaudit.Appender
	mutations     *mutation.Runner
}

// NewInternalService validates deps and returns the collaboration and AI
// document use cases.
func NewInternalService(deps InternalDeps) (*InternalService, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &InternalService{
		db: deps.DB, spiceDB: deps.SpiceDB, contentBlocks: deps.ContentBlocks,
		checkpoints: deps.Checkpoints, references: deps.References, members: deps.Members,
		audit: deps.Audit, mutations: mutations,
	}, nil
}

// adminAccess is the in-transaction authority of an administrator mutation
// of one resource; an ID that cannot name it is rejected before any lock.
func adminAccess(can policyv1.Can, err error) (mutation.Access, error) {
	if err != nil {
		return mutation.Access{}, domainerr.InvalidField("id", err.Error())
	}
	return mutation.FreshAdminCan(can, nil), nil
}
