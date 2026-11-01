package referencecatalog_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/referencecatalog/referencecatalogtest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// fixture is the Reference Catalog use cases over the in-memory store.
type fixture struct {
	store   *referencecatalogtest.Store
	units   *mutationtest.UnitOfWork[referencecatalog.Store]
	checker *authztest.Checker
	fence   *identitystatetest.Fence
	audit   *audittest.Recorder
	service *referencecatalog.Service
}

// newFixture builds the use cases with checker answering SpiceDB and audit
// recording; nil takes a checker that allows everything and a recorder.
func newFixture(t *testing.T, checker *authztest.Checker, audit *audittest.Recorder) *fixture {
	t.Helper()
	if checker == nil {
		checker = authztest.AllowAll()
	}
	if audit == nil {
		audit = &audittest.Recorder{}
	}
	store, units := referencecatalogtest.New()
	fence := &identitystatetest.Fence{}
	service, err := referencecatalog.NewService(referencecatalog.Deps{Units: units, SpiceDB: checker, Fence: fence, Audit: audit})
	require.NoError(t, err)
	return &fixture{store: store, units: units, checker: checker, fence: fence, audit: audit, service: service}
}

// principal is an authenticated, onboarded Member with a request context,
// as the Connect and MCP gateways hand it to a use case.
type principal struct {
	identityID string
	memberID   string
	ctx        context.Context
}

func newPrincipal(t *testing.T) principal {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.71")
	require.NoError(t, err)
	identityID, memberID := uuid.NewString(), uuid.NewString()
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
	return principal{identityID: identityID, memberID: memberID, ctx: ctx}
}

func codeOf(err error) connect.Code {
	return errs.Wrap(err).Code()
}

// messageOf is the client message of err, empty when there is none.
func messageOf(err error) string {
	if err == nil {
		return ""
	}
	return errs.Wrap(err).Message()
}

// lastStep is the end of the most recent transaction.
func (f *fixture) lastStep() mutation.Step {
	steps := f.units.Steps()
	if len(steps) == 0 {
		return ""
	}
	return steps[len(steps)-1]
}

// requireAudited asserts the records appended so far by action and target,
// each attributed to the principal's request.
func (f *fixture) requireAudited(t *testing.T, p principal, want ...sharedtelemetry.AuditRecord) {
	t.Helper()
	records := f.audit.Records()
	require.Len(t, records, len(want))
	for i, record := range records {
		require.Equal(t, want[i].Action, record.Action, i)
		require.Equal(t, want[i].TargetType, record.TargetType, i)
		require.Equal(t, want[i].TargetID, record.TargetID, i)
		require.Equal(t, want[i].ChangedFields, record.ChangedFields, i)
		require.Equal(t, want[i].AssetSlot, record.AssetSlot, i)
		require.Equal(t, want[i].CollectionOperation, record.CollectionOperation, i)
		require.Equal(t, want[i].FileID, record.FileID, i)
		require.Equal(t, p.memberID, record.MemberID, i)
		require.Equal(t, sharedtelemetry.RequestIDFromContext(p.ctx), record.RequestID, i)
	}
}

func audited(action sharedtelemetry.AuditAction, targetType, targetID string, changed ...string) sharedtelemetry.AuditRecord {
	return sharedtelemetry.AuditRecord{Action: action, TargetType: targetType, TargetID: targetID, ChangedFields: changed}
}

// relationship is the policy relationship write of a created (touch) or
// deleted catalog row.
func relationship(t *testing.T, mutate func(string) (policyv1.RelationshipMutation, error), id string) []policyv1.RelationshipMutation {
	t.Helper()
	written, err := mutate(id)
	require.NoError(t, err)
	return []policyv1.RelationshipMutation{written}
}

func TestNewServiceNamesEveryMissingDependency(t *testing.T) {
	_, err := referencecatalog.NewService(referencecatalog.Deps{})
	require.EqualError(t, err, "reference catalog dependencies are required: Audit, Fence, SpiceDB, Units")
}
