package account

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type unchangedRolePermissionsServer struct {
	v1.UnimplementedPermissionsServiceServer
}

func (unchangedRolePermissionsServer) CheckPermission(context.Context, *v1.CheckPermissionRequest) (*v1.CheckPermissionResponse, error) {
	return &v1.CheckPermissionResponse{Permissionship: v1.CheckPermissionResponse_PERMISSIONSHIP_HAS_PERMISSION}, nil
}
func (unchangedRolePermissionsServer) ReadRelationships(req *v1.ReadRelationshipsRequest, stream grpc.ServerStreamingServer[v1.ReadRelationshipsResponse]) error {
	filter := req.GetRelationshipFilter()
	return stream.Send(&v1.ReadRelationshipsResponse{Relationship: &v1.Relationship{
		Resource: &v1.ObjectReference{ObjectType: filter.GetResourceType(), ObjectId: policyv1.Role.User().ID()},
		Relation: filter.GetOptionalRelation(), Subject: &v1.SubjectReference{Object: &v1.ObjectReference{ObjectType: filter.GetOptionalSubjectFilter().GetSubjectType(), ObjectId: filter.GetOptionalSubjectFilter().GetOptionalSubjectId()}},
	}})
}

func newUnchangedRoleTestClient(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	v1.RegisterPermissionsServiceServer(server, unchangedRolePermissionsServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	client, err := auth.NewSpiceDBClient(listener.Addr().String(), "test-token", true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}

func TestUnchangedAccountRoleDoesNotHideRollbackFailure(t *testing.T) {
	for _, rollbackFails := range []bool{false, true} {
		name := "successful rollback"
		if rollbackFails {
			name = "failed rollback"
		}
		t.Run(name, func(t *testing.T) {
			db, mock := newAccountErrorTestDB(t)
			actorMemberID := "11111111-1111-4111-8111-111111111111"
			actorIdentityID := "22222222-2222-4222-8222-222222222222"
			target := authorizationtarget.Target{MemberID: "33333333-3333-4333-8333-333333333333", IdentityID: "44444444-4444-4444-8444-444444444444"}
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock\(`).WillReturnResult(sqlmock.NewResult(0, 1))
			for range 2 {
				mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended`).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectExec(`SELECT pg_advisory_xact_lock\(`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(`SELECT member.id::text AS member_id`).WillReturnRows(sqlmock.NewRows([]string{"member_id", "identity_id"}).AddRow(actorMemberID, actorIdentityID))
			mock.ExpectQuery(`SELECT member.id::text AS member_id`).WillReturnRows(sqlmock.NewRows([]string{"member_id", "identity_id"}).AddRow(target.MemberID, target.IdentityID))
			rollbackErr := errors.New("database rollback failed")
			rollback := mock.ExpectRollback()
			if rollbackFails {
				rollback.WillReturnError(rollbackErr)
			}
			ctx := auth.WithUser(t.Context(), &auth.UserInfo{IdentityID: auth.IdentityID(actorIdentityID), MemberID: auth.MemberID(actorMemberID), SessionID: "55555555-5555-4555-8555-555555555555", Authenticated: true})
			service := newTestService(t, Deps{DB: db, SpiceDB: newUnchangedRoleTestClient(t)})
			err := service.replaceAccountRole(ctx, auth.GetUser(ctx), target, policyv1.Role.User())
			if rollbackFails {
				require.ErrorIs(t, err, rollbackErr)
				require.ErrorIs(t, err, errAccountRoleUnchanged)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
