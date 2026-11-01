//go:build integration

package legal_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

// emailAuthoringDeps is what a legal fixture hands the Email Authoring use
// cases; unset collaborators get stand-ins.
type emailAuthoringDeps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	ContentBlocks *contentblock.Store
	Checkpoints   persistencecheckpoint.ContributorFence
	References    emailauthoringpostgres.DeliveryReferences
	Runtime       *emailauthoringadapter.Runtime
	Audit         domainaudit.Appender
	CDNDomain     string
	SiteOrigin    string
}

func (deps emailAuthoringDeps) units(t *testing.T) (emailauthoring.Deps, emailauthoring.InternalDeps) {
	t.Helper()
	runtime := emailauthoringadapter.NewRuntime()
	if deps.References == nil {
		deps.References = emailauthoringadapter.NewCampaignDeliveryReferences()
	}
	if deps.Checkpoints == nil {
		deps.Checkpoints = unusedCheckpoints{}
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	units, err := emailauthoringpostgres.NewUnits(deps.DB, emailauthoringpostgres.Ports{
		ContentBlocks: deps.ContentBlocks, References: deps.References, Members: memberadapter.ContributorMembers{},
		Checkpoints: deps.Checkpoints, Rendering: runtime,
	})
	require.NoError(t, err)
	return emailauthoring.Deps{
		Units: units, SpiceDB: deps.SpiceDB, Fence: identitystate.PostgresFence{}, Audit: deps.Audit,
		TestEmails: unusedTestEmails{}, Locales: runtime, CDNDomain: deps.CDNDomain, SiteOrigin: deps.SiteOrigin,
	}, emailauthoring.InternalDeps{
		Units: units, SpiceDB: deps.SpiceDB, Fence: identitystate.PostgresFence{}, Audit: deps.Audit,
	}
}

// newEmailAuthoringService builds the Email Authoring management use cases
// that seed fixtures. A test send fails, and an unset audit appender
// records in memory.
func newEmailAuthoringService(t *testing.T, deps emailAuthoringDeps) *emailauthoring.Service {
	t.Helper()
	service, err := emailauthoring.NewService(must(deps.units(t)))
	require.NoError(t, err)
	return service
}

func must(deps emailauthoring.Deps, _ emailauthoring.InternalDeps) emailauthoring.Deps { return deps }

type unusedTestEmails struct{}

func (unusedTestEmails) QueueTestEmail(context.Context, emailauthoring.TestEmail) error {
	return errors.New("test did not configure an email publisher")
}

type unusedCheckpoints struct{}

func (unusedCheckpoints) RequireCurrentContributors(context.Context, *gorm.DB, intrav1.CollaborationResourceType, string, []string) error {
	return errors.New("test did not configure collaboration checkpoints")
}

// newInternalEmailAuthoringService builds the Email Authoring collaboration
// use cases that seed fixtures.
func newInternalEmailAuthoringService(t *testing.T, deps emailAuthoringDeps) *emailauthoring.InternalService {
	t.Helper()
	_, internal := deps.units(t)
	service, err := emailauthoring.NewInternalService(internal)
	require.NoError(t, err)
	return service
}
