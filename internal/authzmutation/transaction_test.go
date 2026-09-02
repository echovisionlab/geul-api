package authzmutation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

type authorizationWriteTestServer struct {
	v1.UnimplementedPermissionsServiceServer

	mu                     sync.Mutex
	requests               []*v1.WriteRelationshipsRequest
	failAt                 int
	failExpectedWrites     bool
	failCompensationWrites bool
	onWrite                func(*v1.WriteRelationshipsRequest)
}

type commitPanicConnPool struct {
	gorm.ConnPool
	transaction *commitPanicTransaction
}

func (pool *commitPanicConnPool) BeginTx(
	ctx context.Context,
	options *sql.TxOptions,
) (gorm.ConnPool, error) {
	beginner, ok := pool.ConnPool.(gorm.TxBeginner)
	if !ok {
		return nil, gorm.ErrInvalidTransaction
	}
	transaction, err := beginner.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	pool.transaction = &commitPanicTransaction{ConnPool: transaction, transaction: transaction}
	return pool.transaction, nil
}

type commitPanicTransaction struct {
	gorm.ConnPool
	transaction   *sql.Tx
	rollbackCalls int
}

type authorizationTrackingConnPool struct {
	gorm.ConnPool
	transaction *authorizationTrackingTransaction
}

func (pool *authorizationTrackingConnPool) BeginTx(
	ctx context.Context,
	options *sql.TxOptions,
) (gorm.ConnPool, error) {
	beginner, ok := pool.ConnPool.(gorm.TxBeginner)
	if !ok {
		return nil, gorm.ErrInvalidTransaction
	}
	transaction, err := beginner.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	pool.transaction = &authorizationTrackingTransaction{ConnPool: transaction, transaction: transaction}
	return pool.transaction, nil
}

type authorizationTrackingTransaction struct {
	gorm.ConnPool
	transaction   *sql.Tx
	rollbackCalls atomic.Int32
}

func (transaction *authorizationTrackingTransaction) Commit() error {
	return transaction.transaction.Commit()
}

func (transaction *authorizationTrackingTransaction) Rollback() error {
	transaction.rollbackCalls.Add(1)
	return transaction.transaction.Rollback()
}

func (*commitPanicTransaction) Commit() error {
	panic("forced commit panic")
}

func (transaction *commitPanicTransaction) Rollback() error {
	transaction.rollbackCalls++
	return transaction.transaction.Rollback()
}

func (server *authorizationWriteTestServer) WriteRelationships(
	_ context.Context,
	request *v1.WriteRelationshipsRequest,
) (*v1.WriteRelationshipsResponse, error) {
	server.mu.Lock()
	server.requests = append(server.requests, request)
	call := len(server.requests)
	isCompensation := len(request.GetUpdates()) != 0 && request.GetUpdates()[0].GetOperation() == v1.RelationshipUpdate_OPERATION_DELETE
	fail := server.failAt == call ||
		(server.failExpectedWrites && len(request.GetOptionalPreconditions()) != 0 && !isCompensation) ||
		(server.failCompensationWrites && isCompensation)
	onWrite := server.onWrite
	server.mu.Unlock()
	if onWrite != nil {
		onWrite(request)
	}
	if fail {
		return nil, status.Error(codes.Unavailable, "forced relationship write failure")
	}
	return &v1.WriteRelationshipsResponse{WrittenAt: &v1.ZedToken{Token: fmt.Sprintf("revision-%d", call)}}, nil
}

func (server *authorizationWriteTestServer) operations() []v1.RelationshipUpdate_Operation {
	server.mu.Lock()
	defer server.mu.Unlock()
	operations := make([]v1.RelationshipUpdate_Operation, 0, len(server.requests))
	for _, request := range server.requests {
		for _, update := range request.GetUpdates() {
			operations = append(operations, update.GetOperation())
		}
	}
	return operations
}

func (server *authorizationWriteTestServer) requestSnapshot() []*v1.WriteRelationshipsRequest {
	server.mu.Lock()
	defer server.mu.Unlock()
	return append([]*v1.WriteRelationshipsRequest(nil), server.requests...)
}

func newAuthorizationBoundaryTestClient(
	t *testing.T,
	server *authorizationWriteTestServer,
) *auth.SpiceDBClient {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcServer := grpc.NewServer()
	v1.RegisterPermissionsServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)

	client, err := auth.NewSpiceDBClient(listener.Addr().String(), "test-token", true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}

func newAuthorizationBoundaryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE authorization_boundary_test (id TEXT PRIMARY KEY)").Error)
	return db
}

func authorizationBoundaryTestMutations(t *testing.T) (policyv1.RelationshipMutation, policyv1.RelationshipMutation) {
	t.Helper()
	resourceID := uuid.NewString()
	apply, err := policyv1.Post.TouchPolicy(resourceID)
	require.NoError(t, err)
	compensate, err := policyv1.Post.DeletePolicy(resourceID)
	require.NoError(t, err)
	return apply, compensate
}

func TestExecuteAuthorizedMutationCommitsDatabaseAndReturnsServerToken(t *testing.T) {
	db := newAuthorizationBoundaryTestDB(t)
	server := &authorizationWriteTestServer{}
	client := newAuthorizationBoundaryTestClient(t, server)
	apply, compensate := authorizationBoundaryTestMutations(t)

	token, err := Execute(t.Context(), db, client, func(tx *gorm.DB, write WriteRelationships) error {
		if err := tx.Exec("INSERT INTO authorization_boundary_test (id) VALUES (?)", "committed").Error; err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{apply}, []policyv1.RelationshipMutation{compensate})
	})
	require.NoError(t, err)
	require.Equal(t, "revision-1", token.String())
	require.Equal(t, []v1.RelationshipUpdate_Operation{v1.RelationshipUpdate_OPERATION_TOUCH}, server.operations())

	var count int64
	require.NoError(t, db.Table("authorization_boundary_test").Where("id = ?", "committed").Count(&count).Error)
	require.EqualValues(t, 1, count)

}

func TestExecuteAuthorizedMutationRollsBackAndCompensatesCallbackError(t *testing.T) {
	db := newAuthorizationBoundaryTestDB(t)
	server := &authorizationWriteTestServer{}
	client := newAuthorizationBoundaryTestClient(t, server)
	apply, compensate := authorizationBoundaryTestMutations(t)
	callbackFailure := errors.New("audit failed")

	token, err := Execute(t.Context(), db, client, func(tx *gorm.DB, write WriteRelationships) error {
		if err := tx.Exec("INSERT INTO authorization_boundary_test (id) VALUES (?)", "rolled-back").Error; err != nil {
			return err
		}
		if err := write([]policyv1.RelationshipMutation{apply}, []policyv1.RelationshipMutation{compensate}); err != nil {
			return err
		}
		return callbackFailure
	})
	require.ErrorIs(t, err, callbackFailure)
	require.Empty(t, token.String())
	require.Equal(t, []v1.RelationshipUpdate_Operation{
		v1.RelationshipUpdate_OPERATION_TOUCH,
		v1.RelationshipUpdate_OPERATION_DELETE,
	}, server.operations())

	var count int64
	require.NoError(t, db.Table("authorization_boundary_test").Where("id = ?", "rolled-back").Count(&count).Error)
	require.Zero(t, count)
}

func TestExecuteAuthorizedMutationPreservesCallbackErrorWithoutCleanupFailure(t *testing.T) {
	db := newAuthorizationBoundaryTestDB(t)
	server := &authorizationWriteTestServer{}
	client := newAuthorizationBoundaryTestClient(t, server)
	callbackErr := connect.NewError(connect.CodeFailedPrecondition, errors.New("stale mutation"))

	_, err := Execute(t.Context(), db, client, func(_ *gorm.DB, _ WriteRelationships) error {
		return callbackErr
	})
	require.Same(t, callbackErr, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Empty(t, server.operations())
}

func TestExecuteAuthorizedMutationCompensatesWhileDatabaseLocksAreHeld(t *testing.T) {
	db := newAuthorizationBoundaryTestDB(t)
	pool := &authorizationTrackingConnPool{ConnPool: db.Statement.ConnPool}
	db.Statement.ConnPool = pool
	db.Config.ConnPool = pool
	var rollbackCallsAtCompensation atomic.Int32
	rollbackCallsAtCompensation.Store(-1)
	server := &authorizationWriteTestServer{onWrite: func(request *v1.WriteRelationshipsRequest) {
		if len(request.GetUpdates()) != 0 && request.GetUpdates()[0].GetOperation() == v1.RelationshipUpdate_OPERATION_DELETE {
			rollbackCallsAtCompensation.Store(pool.transaction.rollbackCalls.Load())
		}
	}}
	client := newAuthorizationBoundaryTestClient(t, server)
	apply, compensate := authorizationBoundaryTestMutations(t)
	callbackFailure := errors.New("audit failed")

	_, err := Execute(t.Context(), db, client, func(_ *gorm.DB, write WriteRelationships) error {
		if err := write([]policyv1.RelationshipMutation{apply}, []policyv1.RelationshipMutation{compensate}); err != nil {
			return err
		}
		return callbackFailure
	})
	require.ErrorIs(t, err, callbackFailure)
	require.EqualValues(t, 0, rollbackCallsAtCompensation.Load())
	require.EqualValues(t, 1, pool.transaction.rollbackCalls.Load())
}

func TestExecuteAuthorizedMutationCompensatesUncertainForwardBeforeRollback(t *testing.T) {
	db := newAuthorizationBoundaryTestDB(t)
	pool := &authorizationTrackingConnPool{ConnPool: db.Statement.ConnPool}
	db.Statement.ConnPool = pool
	db.Config.ConnPool = pool
	var rollbackCallsAtCompensation atomic.Int32
	rollbackCallsAtCompensation.Store(-1)
	server := &authorizationWriteTestServer{
		failExpectedWrites: true,
		onWrite: func(request *v1.WriteRelationshipsRequest) {
			if len(request.GetUpdates()) != 0 && request.GetUpdates()[0].GetOperation() == v1.RelationshipUpdate_OPERATION_DELETE {
				rollbackCallsAtCompensation.Store(pool.transaction.rollbackCalls.Load())
			}
		},
	}
	client := newAuthorizationBoundaryTestClient(t, server)
	apply, compensate := authorizationBoundaryTestMutations(t)

	_, err := Execute(t.Context(), db, client, func(tx *gorm.DB, write WriteRelationships) error {
		if err := tx.Exec("INSERT INTO authorization_boundary_test (id) VALUES (?)", "uncertain-forward").Error; err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{apply}, []policyv1.RelationshipMutation{compensate})
	})
	require.Error(t, err)
	require.True(t, auth.IsRelationshipWriteOutcomeUncertain(err), "error = %v", err)
	require.EqualValues(t, 0, rollbackCallsAtCompensation.Load())
	require.EqualValues(t, 1, pool.transaction.rollbackCalls.Load())
	requireAuthorizationBoundaryRowAbsent(t, db, "uncertain-forward")

	requests := server.requestSnapshot()
	require.GreaterOrEqual(t, len(requests), 2)
	for _, request := range requests[:len(requests)-1] {
		require.NotEmpty(t, request.GetOptionalPreconditions())
		require.Equal(t, v1.RelationshipUpdate_OPERATION_TOUCH, request.GetUpdates()[0].GetOperation())
	}
	require.NotEmpty(t, requests[len(requests)-1].GetOptionalPreconditions())
	require.Equal(t, v1.RelationshipUpdate_OPERATION_DELETE, requests[len(requests)-1].GetUpdates()[0].GetOperation())
}

func TestExecuteAuthorizedMutationRejectsIgnoredSecondWriteAndCompensates(t *testing.T) {
	db := newAuthorizationBoundaryTestDB(t)
	server := &authorizationWriteTestServer{}
	client := newAuthorizationBoundaryTestClient(t, server)
	apply, compensate := authorizationBoundaryTestMutations(t)

	token, err := Execute(t.Context(), db, client, func(tx *gorm.DB, write WriteRelationships) error {
		if err := tx.Exec("INSERT INTO authorization_boundary_test (id) VALUES (?)", "ignored-second-write").Error; err != nil {
			return err
		}
		if err := write([]policyv1.RelationshipMutation{apply}, []policyv1.RelationshipMutation{compensate}); err != nil {
			return err
		}
		_ = write([]policyv1.RelationshipMutation{apply}, []policyv1.RelationshipMutation{compensate})
		return nil
	})
	require.ErrorContains(t, err, "may be written only once")
	require.Empty(t, token.String())
	require.Equal(t, []v1.RelationshipUpdate_Operation{
		v1.RelationshipUpdate_OPERATION_TOUCH,
		v1.RelationshipUpdate_OPERATION_DELETE,
	}, server.operations())
	requireAuthorizationBoundaryRowAbsent(t, db, "ignored-second-write")
}

func TestExecuteAuthorizedMutationJoinsCompensationFailure(t *testing.T) {
	db := newAuthorizationBoundaryTestDB(t)
	server := &authorizationWriteTestServer{failCompensationWrites: true}
	client := newAuthorizationBoundaryTestClient(t, server)
	apply, compensate := authorizationBoundaryTestMutations(t)
	callbackFailure := errors.New("audit failed")

	_, err := Execute(t.Context(), db, client, func(_ *gorm.DB, write WriteRelationships) error {
		if err := write([]policyv1.RelationshipMutation{apply}, []policyv1.RelationshipMutation{compensate}); err != nil {
			return err
		}
		return callbackFailure
	})
	require.ErrorIs(t, err, callbackFailure)
	require.ErrorContains(t, err, "compensate authorization relationships")
	require.GreaterOrEqual(t, len(server.operations()), 2)
}

func TestExecuteAuthorizedMutationCompensatesBeforeJoiningRollbackFailure(t *testing.T) {
	db := newAuthorizationBoundaryTestDB(t)
	server := &authorizationWriteTestServer{}
	client := newAuthorizationBoundaryTestClient(t, server)
	apply, compensate := authorizationBoundaryTestMutations(t)
	callbackFailure := errors.New("audit failed")

	_, err := Execute(t.Context(), db, client, func(tx *gorm.DB, write WriteRelationships) error {
		if err := write([]policyv1.RelationshipMutation{apply}, []policyv1.RelationshipMutation{compensate}); err != nil {
			return err
		}
		if err := tx.Rollback().Error; err != nil {
			return err
		}
		return callbackFailure
	})
	require.ErrorIs(t, err, callbackFailure)
	require.ErrorContains(t, err, "rollback authorized database transaction")
	require.Equal(t, []v1.RelationshipUpdate_Operation{
		v1.RelationshipUpdate_OPERATION_TOUCH,
		v1.RelationshipUpdate_OPERATION_DELETE,
	}, server.operations())
}

func TestExecuteAuthorizedMutationDoesNotCompensateCommitError(t *testing.T) {
	db := newAuthorizationBoundaryTestDB(t)
	require.NoError(t, db.Exec("PRAGMA foreign_keys = ON").Error)
	require.NoError(t, db.Exec("CREATE TABLE authorization_parent (id TEXT PRIMARY KEY)").Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE authorization_child (
			id TEXT PRIMARY KEY,
			parent_id TEXT NOT NULL,
			FOREIGN KEY (parent_id) REFERENCES authorization_parent(id) DEFERRABLE INITIALLY DEFERRED
		)
	`).Error)
	server := &authorizationWriteTestServer{}
	client := newAuthorizationBoundaryTestClient(t, server)
	apply, compensate := authorizationBoundaryTestMutations(t)

	_, err := Execute(t.Context(), db, client, func(tx *gorm.DB, write WriteRelationships) error {
		if err := tx.Exec("INSERT INTO authorization_child (id, parent_id) VALUES (?, ?)", "child", "missing").Error; err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{apply}, []policyv1.RelationshipMutation{compensate})
	})
	require.ErrorContains(t, err, "commit authorized database transaction")
	require.Equal(t, []v1.RelationshipUpdate_Operation{v1.RelationshipUpdate_OPERATION_TOUCH}, server.operations())
}

func TestExecuteAuthorizedMutationDoesNotRollbackOrCompensateCommitPanic(t *testing.T) {
	db := newAuthorizationBoundaryTestDB(t)
	pool := &commitPanicConnPool{ConnPool: db.Statement.ConnPool}
	db.Statement.ConnPool = pool
	db.Config.ConnPool = pool
	server := &authorizationWriteTestServer{}
	client := newAuthorizationBoundaryTestClient(t, server)
	apply, compensate := authorizationBoundaryTestMutations(t)

	panicValue := captureAuthorizationMutationPanic(func() {
		_, _ = Execute(t.Context(), db, client, func(_ *gorm.DB, write WriteRelationships) error {
			return write([]policyv1.RelationshipMutation{apply}, []policyv1.RelationshipMutation{compensate})
		})
	})
	panicErr, ok := panicValue.(error)
	require.True(t, ok, "panic value = %#v", panicValue)
	require.ErrorContains(t, panicErr, "forced commit panic")
	require.ErrorContains(t, panicErr, "commit outcome is uncertain")
	require.NotNil(t, pool.transaction)
	require.Zero(t, pool.transaction.rollbackCalls)
	require.Equal(t, []v1.RelationshipUpdate_Operation{v1.RelationshipUpdate_OPERATION_TOUCH}, server.operations())

	// The helper intentionally leaves the commit-uncertain transaction alone.
	// Test cleanup may release the synthetic transaction after asserting that.
	require.NoError(t, pool.transaction.transaction.Rollback())
}

func TestExecuteAuthorizedMutationPanicBeforeWriteRollsBackWithoutCompensation(t *testing.T) {
	db := newAuthorizationBoundaryTestDB(t)
	server := &authorizationWriteTestServer{}
	client := newAuthorizationBoundaryTestClient(t, server)

	panicValue := captureAuthorizationMutationPanic(func() {
		_, _ = Execute(t.Context(), db, client, func(tx *gorm.DB, _ WriteRelationships) error {
			if err := tx.Exec("INSERT INTO authorization_boundary_test (id) VALUES (?)", "panic-before").Error; err != nil {
				panic(err)
			}
			panic("panic before write")
		})
	})
	require.Equal(t, "panic before write", panicValue)
	require.Empty(t, server.operations())
	requireAuthorizationBoundaryRowAbsent(t, db, "panic-before")
}

func TestExecuteAuthorizedMutationPanicAfterWriteRollsBackAndCompensates(t *testing.T) {
	db := newAuthorizationBoundaryTestDB(t)
	server := &authorizationWriteTestServer{}
	client := newAuthorizationBoundaryTestClient(t, server)
	apply, compensate := authorizationBoundaryTestMutations(t)

	panicValue := captureAuthorizationMutationPanic(func() {
		_, _ = Execute(t.Context(), db, client, func(tx *gorm.DB, write WriteRelationships) error {
			if err := tx.Exec("INSERT INTO authorization_boundary_test (id) VALUES (?)", "panic-after").Error; err != nil {
				panic(err)
			}
			if err := write([]policyv1.RelationshipMutation{apply}, []policyv1.RelationshipMutation{compensate}); err != nil {
				panic(err)
			}
			panic("panic after write")
		})
	})
	require.Equal(t, "panic after write", panicValue)
	require.Equal(t, []v1.RelationshipUpdate_Operation{
		v1.RelationshipUpdate_OPERATION_TOUCH,
		v1.RelationshipUpdate_OPERATION_DELETE,
	}, server.operations())
	requireAuthorizationBoundaryRowAbsent(t, db, "panic-after")
}

func captureAuthorizationMutationPanic(run func()) (panicValue any) {
	defer func() { panicValue = recover() }()
	run()
	return nil
}

func requireAuthorizationBoundaryRowAbsent(t *testing.T, db *gorm.DB, id string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("authorization_boundary_test").Where("id = ?", id).Count(&count).Error)
	require.Zero(t, count)
}
