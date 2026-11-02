package mutationtest_test

import (
	"context"
	"errors"
	"testing"
	"time"

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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// notes keeps persisted names and details separately from read observations.
type notes struct {
	names   map[string]bool
	details *noteDetails
	reads   int
	hook    func()
}

type noteDetails struct {
	Labels  map[string][]string
	Title   *string
	Payload proto.Message
}

func (n *notes) Savepoint() mutationtest.Checkpoint {
	names := mutationtest.ClonePersisted(n.names)
	details := mutationtest.ClonePersisted(n.details)
	return mutationtest.Checkpoint{
		State: map[string]any{
			"names":   names,
			"details": details,
		},
		Restore: func() {
			n.names = names
			n.details = details
		},
	}
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

func TestUnitOfWorkReadsHandTheStoreAndKeepTheReadError(t *testing.T) {
	store := &notes{names: map[string]bool{"stored": true}}
	units := mutationtest.New(store)
	readErr := errors.New("read failed")
	for name, read := range map[string]func(context.Context, func(*notes) error) error{
		"snapshot": units.Read, "locked snapshot": units.ReadLocked, "current": units.ReadCurrent,
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, read(t.Context(), func(read *notes) error {
				require.Same(t, store, read)
				return nil
			}))
			require.ErrorIs(t, read(t.Context(), func(*notes) error { return readErr }), readErr)
		})
	}
}

func TestReadRejectsPersistedWritesAndKeepsObservations(t *testing.T) {
	store := &notes{names: map[string]bool{"stored": true}}
	store.hook = func() { store.reads++ }
	units := mutationtest.New(store)
	require.NoError(t, units.Read(t.Context(), func(read *notes) error {
		read.hook()
		return nil
	}))
	require.Equal(t, 1, store.reads)
	err := units.Read(t.Context(), func(read *notes) error {
		read.hook()
		read.names["written"] = true
		return nil
	})
	require.ErrorIs(t, err, mutationtest.ErrReadOnly)
	require.Equal(t, map[string]bool{"stored": true}, store.names)
	require.Equal(t, 2, store.reads)
	require.Empty(t, units.Steps())
}

func TestReadModesAllowWritesOnlyInWritableTransactions(t *testing.T) {
	for _, name := range []string{"snapshot", "locked snapshot", "current"} {
		t.Run(name, func(t *testing.T) {
			store := &notes{names: map[string]bool{}}
			units := mutationtest.New(store)
			read := units.Read
			if name == "locked snapshot" {
				read = units.ReadLocked
			}
			if name == "current" {
				read = units.ReadCurrent
			}
			err := read(t.Context(), func(store *notes) error {
				store.names["written"] = true
				return nil
			})
			if name == "snapshot" {
				require.ErrorIs(t, err, mutationtest.ErrReadOnly)
				require.Empty(t, store.names)
			} else {
				require.NoError(t, err)
				require.True(t, store.names["written"])
			}
		})
	}
}

func TestReadModesRollBackErrorsAndPanics(t *testing.T) {
	for _, name := range []string{"snapshot", "locked snapshot", "current"} {
		t.Run(name, func(t *testing.T) {
			store := &notes{names: map[string]bool{"stored": true}}
			units := mutationtest.New(store)
			read := units.Read
			if name == "locked snapshot" {
				read = units.ReadLocked
			}
			if name == "current" {
				read = units.ReadCurrent
			}
			callbackErr := errors.New("callback failed")
			err := read(t.Context(), func(store *notes) error {
				store.names["failed"] = true
				return callbackErr
			})
			require.ErrorIs(t, err, callbackErr)
			if name == "snapshot" {
				require.ErrorIs(t, err, mutationtest.ErrReadOnly)
			}
			require.Equal(t, map[string]bool{"stored": true}, store.names)
			panicValue := &struct{ message string }{"callback panic"}
			require.PanicsWithValue(t, panicValue, func() {
				_ = read(t.Context(), func(store *notes) error {
					store.names["panicked"] = true
					panic(panicValue)
				})
			})
			require.Equal(t, map[string]bool{"stored": true}, store.names)
			require.NoError(t, units.Read(t.Context(), func(*notes) error { return nil }))
		})
	}
}

func TestReadRestoresNestedValuesChangedThroughAliases(t *testing.T) {
	title := "before"
	store := &notes{names: map[string]bool{}, details: &noteDetails{
		Labels: map[string][]string{"labels": {"before"}}, Title: &title,
		Payload: structpb.NewStringValue("before"),
	}}
	units := mutationtest.New(store)
	require.NoError(t, units.Read(t.Context(), func(read *notes) error {
		_, err := proto.Marshal(read.details.Payload)
		return err
	}))
	err := units.Read(t.Context(), func(read *notes) error {
		read.details.Labels["labels"][0] = "after"
		*read.details.Title = "after"
		read.details.Payload.(*structpb.Value).Kind = &structpb.Value_StringValue{StringValue: "after"}
		return nil
	})
	require.ErrorIs(t, err, mutationtest.ErrReadOnly)
	require.Equal(t, "before", store.details.Labels["labels"][0])
	require.Equal(t, "before", *store.details.Title)
	require.True(t, proto.Equal(structpb.NewStringValue("before"), store.details.Payload))
}

func TestClonePersistedKeepsTimeAndRejectsPrivateMutableFields(t *testing.T) {
	stamp := time.Now()
	require.Equal(t, stamp, mutationtest.ClonePersisted(stamp))
	type privateScalar struct{ value string }
	require.Equal(t, privateScalar{value: "kept"}, mutationtest.ClonePersisted(privateScalar{value: "kept"}))
	type privateSlice struct{ value []byte }
	require.Panics(t, func() { mutationtest.ClonePersisted(privateSlice{value: []byte("mutable")}) })
	require.Panics(t, func() { mutationtest.ClonePersisted(func() {}) })
}

func TestRunnerWithUnitsKeepsAuthorityAndAuditOnTheSelectedStore(t *testing.T) {
	t.Parallel()
	original := mutationtest.New(&notes{names: map[string]bool{}})
	selected := mutationtest.New(&notes{names: map[string]bool{}})
	checker, fence, audit := authztest.AllowAll(), &identitystatetest.Fence{}, &audittest.Recorder{}
	runner, err := mutation.NewStoreRunner(original, checker, fence, audit)
	require.NoError(t, err)
	scoped, err := runner.WithUnits(selected)
	require.NoError(t, err)
	ctx, identity := adminContext(t)
	_, err = mutation.Run(ctx, scoped, noteSpec("selected", nil))
	require.NoError(t, err)
	require.Empty(t, original.Store().names)
	require.True(t, selected.Store().names["selected"])
	require.Equal(t, []string{identity}, fence.Locked())
	require.Len(t, audit.Records(), 1)
	_, err = mutation.Run(ctx, runner, noteSpec("original", nil))
	require.NoError(t, err)
	require.True(t, original.Store().names["original"])
	require.False(t, selected.Store().names["original"])
	queueDown := errors.New("queue down")
	_, err = mutation.Run(ctx, scoped, noteSpec("rolled back", queueDown))
	require.ErrorIs(t, err, queueDown)
	require.False(t, selected.Store().names["rolled back"])
	denied := errors.New("authorization unavailable")
	checker.Fail(denied)
	_, err = mutation.Run(ctx, scoped, noteSpec("denied", nil))
	require.ErrorIs(t, err, denied)
	require.False(t, selected.Store().names["denied"])
	_, err = runner.WithUnits(nil)
	require.ErrorContains(t, err, "unit of work is required")
}
