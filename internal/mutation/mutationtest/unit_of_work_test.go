package mutationtest_test

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// notes is the smallest fake store: a set of names it can roll back.
type notes struct{ names map[string]bool }

func (n *notes) Savepoint() func() {
	saved := maps.Clone(n.names)
	return func() { n.names = saved }
}

var _ mutation.UnitOfWork[*notes] = (*mutationtest.UnitOfWork[*notes])(nil)

func adminContext(t *testing.T) (context.Context, string) {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.10")
	require.NoError(t, err)
	identityID := uuid.NewString()
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	}), identityID
}

func noteSpec(name string, outbox error) mutation.StoreSpec[string, *notes] {
	return mutation.StoreSpec[string, *notes]{
		Lock:   func(context.Context, *notes) error { return nil },
		Access: mutation.FreshAdminCan(policyv1.Platform.IsAdmin()),
		Change: func(_ context.Context, store *notes) (string, error) {
			store.names[name] = true
			return name, nil
		},
		Audit: func(name string) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMemberTagCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberTagCreatedAuditRecord(metadata, uuid.NewString(), name)
			})
		},
		Outbox: func(context.Context, *notes, string) error { return outbox },
	}
}

func TestUnitOfWorkRecordsTheStepsAndRollsBackTheStore(t *testing.T) {
	units := mutationtest.New(&notes{names: map[string]bool{}})
	runner, err := mutation.NewStoreRunner(units, authztest.AllowAll(), &identitystatetest.Fence{}, &audittest.Recorder{})
	require.NoError(t, err)
	ctx, _ := adminContext(t)

	_, err = mutation.Run(ctx, runner, noteSpec("kept", nil))
	require.NoError(t, err)
	queueDown := errors.New("queue unavailable")
	_, err = mutation.Run(ctx, runner, noteSpec("rolled back", queueDown))
	require.ErrorIs(t, err, queueDown)

	require.Equal(t, map[string]bool{"kept": true}, units.Store().names)
	require.Equal(t, []mutation.Step{
		mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepOutbox, mutation.StepCommit,
		mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepOutbox, mutation.StepRollback,
	}, units.Steps())
}

func TestUnitOfWorkLocksAuthorizationAndWritesRelationshipsLast(t *testing.T) {
	units := mutationtest.New(&notes{names: map[string]bool{}})
	checker := authztest.AllowAll()
	runner, err := mutation.NewStoreRunner(units, checker, &identitystatetest.Fence{}, &audittest.Recorder{})
	require.NoError(t, err)
	ctx, identityID := adminContext(t)
	touch, err := policyv1.SiteSetting.TouchPolicy("1")
	require.NoError(t, err)
	remove, err := policyv1.SiteSetting.DeletePolicy("1")
	require.NoError(t, err)
	spec := noteSpec("related", nil)
	spec.Subjects = []string{identityID}
	written := false
	spec.Relationships = func(string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
		if written {
			return nil, nil, errors.New("relationship plan failed")
		}
		written = true
		return []policyv1.RelationshipMutation{touch}, []policyv1.RelationshipMutation{remove}, nil
	}

	_, err = mutation.Run(ctx, runner, spec)
	require.NoError(t, err)
	_, err = mutation.Run(ctx, runner, spec)
	require.ErrorContains(t, err, "relationship plan failed")

	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, checker.Applied())
	require.Empty(t, checker.Compensated(), "a batch that was never written is not compensated")
	require.Equal(t, mutation.StepAuthorizationLock, units.Steps()[0])
	require.Equal(t, mutation.StepSubjects, units.Steps()[1])
	require.Contains(t, units.Steps(), mutation.StepRelationships)

	auditDown := errors.New("audit unavailable")
	failing, err := mutation.NewStoreRunner(units, checker, &identitystatetest.Fence{}, &audittest.Recorder{Err: auditDown})
	require.NoError(t, err)
	_, err = mutation.Run(ctx, failing, noteSpec("audited", nil))
	require.ErrorIs(t, err, auditDown)
	require.False(t, units.Store().names["audited"])
}
