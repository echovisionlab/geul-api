package sharelink_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	"github.com/echovisionlab/geul-api/internal/sharelink/sharelinktest"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const (
	callerIdentityID = "00000000-0000-4000-8000-000000000101"
	callerMemberID   = "00000000-0000-4000-8000-000000000102"
)

// fixture is the Share Link use cases on fakes. A test seeds store and sets
// checker rules before it calls links.
type fixture struct {
	store   *sharelinktest.Store
	units   *mutationtest.UnitOfWork[sharelink.Store]
	checker *authztest.Checker
	fence   *identitystatetest.Fence
	audit   *audittest.Recorder
	links   *sharelink.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store := sharelinktest.NewStore()
	return newFixtureOver(t, store, store)
}

// newFixtureOver runs the use cases over wrapped, a Store that decorates
// store, while the test seeds and inspects store itself.
func newFixtureOver(t *testing.T, store *sharelinktest.Store, wrapped sharelink.Store) *fixture {
	t.Helper()
	f := &fixture{
		store: store, units: mutationtest.New(wrapped), checker: &authztest.Checker{},
		fence: &identitystatetest.Fence{}, audit: &audittest.Recorder{},
	}
	links, err := sharelink.NewService(sharelink.Deps{Units: f.units, SpiceDB: f.checker, Fence: f.fence, Audit: f.audit})
	require.NoError(t, err)
	f.links = links
	return f
}

// target seeds a target of entityType in state and returns its ID.
func (f *fixture) target(entityType sharelink.EntityType, state sharelink.Target) string {
	id := uuid.NewString()
	f.store.SeedTarget(entityType, id, state)
	return id
}

// callerContext is the request of an onboarded Member whose audit records
// name the caller.
func callerContext(t *testing.T) context.Context {
	t.Helper()
	return principalContext(t, &auth.UserInfo{
		IdentityID: callerIdentityID, MemberID: callerMemberID, SessionID: auth.SessionID(uuid.NewString()),
		Authenticated: true, Onboarded: true,
	})
}

func principalContext(t *testing.T, principal *auth.UserInfo) context.Context {
	t.Helper()
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{
		IdentityID: callerIdentityID, MemberID: callerMemberID, SessionID: uuid.NewString(),
	})
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), request), principal)
}

func requireCode(t *testing.T, code connect.Code, err error) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, code, errs.Wrap(err).Code(), err.Error())
}

func requireMessage(t *testing.T, code connect.Code, message string, err error) {
	t.Helper()
	requireCode(t, code, err)
	require.Equal(t, message, errs.Wrap(err).Message())
}

// check is the authorization question the caller asks of can.
func check(action auth.ResourceAction, id string) authztest.Check {
	can, err := action(id)
	return authztest.Of(callerIdentityID, can, err)
}

var committed = []mutation.Step{mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepCommit}
