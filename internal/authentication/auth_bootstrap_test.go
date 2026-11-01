package authentication

import (
	"errors"
	"net"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/grpc"
	"gorm.io/driver/postgres"
	"gorm.io/gorm/logger"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNewServiceRejectsMissingRequiredDependencies(t *testing.T) {
	_, err := NewService(Deps{SpiceDB: &auth.SpiceDBClient{}})
	require.EqualError(t, err, "authentication dependencies are required: Audit, DB, Roles")
	_, err = NewService(Deps{DB: &gorm.DB{}})
	require.EqualError(t, err, "authentication dependencies are required: Audit, Roles, SpiceDB")
}

type unchangedLoginRoleServer struct {
	v1.UnimplementedPermissionsServiceServer
}

func (unchangedLoginRoleServer) ReadRelationships(req *v1.ReadRelationshipsRequest, stream grpc.ServerStreamingServer[v1.ReadRelationshipsResponse]) error {
	filter := req.GetRelationshipFilter()
	return stream.Send(&v1.ReadRelationshipsResponse{Relationship: &v1.Relationship{
		Resource: &v1.ObjectReference{ObjectType: filter.GetResourceType(), ObjectId: policyv1.Role.User().ID()},
		Relation: filter.GetOptionalRelation(), Subject: &v1.SubjectReference{Object: &v1.ObjectReference{ObjectType: filter.GetOptionalSubjectFilter().GetSubjectType(), ObjectId: filter.GetOptionalSubjectFilter().GetOptionalSubjectId()}},
	}})
}

func TestUnchangedLoginRoleDoesNotHideRollbackFailure(t *testing.T) {
	for _, rollbackFails := range []bool{false, true} {
		name := "successful rollback"
		if rollbackFails {
			name = "failed rollback"
		}
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			server := grpc.NewServer()
			v1.RegisterPermissionsServiceServer(server, unchangedLoginRoleServer{})
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(server.Stop)
			spiceDB, err := auth.NewSpiceDBClient(listener.Addr().String(), "test-token", true)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, spiceDB.Close()) })
			sqlDB, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, sqlDB.Close()) })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock\(`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtext`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`INSERT INTO auth_bootstrap_state`).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery(`SELECT COALESCE\(identity_id::text, ''\) FROM auth_bootstrap_state`).WillReturnRows(sqlmock.NewRows([]string{"identity_id"}).AddRow("another-identity"))
			rollbackErr := errors.New("database rollback failed")
			rollback := mock.ExpectRollback()
			if rollbackFails {
				rollback.WillReturnError(rollbackErr)
			}
			service := &Service{db: db, spiceDB: spiceDB}
			result, err := service.SyncLoginRole(t.Context(), "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
			if rollbackFails {
				require.ErrorIs(t, err, rollbackErr)
				require.ErrorIs(t, err, errLoginRoleUnchanged)
				require.Empty(t, result)
			} else {
				require.NoError(t, err)
				require.Equal(t, policyv1.Role.User(), result.Role)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
