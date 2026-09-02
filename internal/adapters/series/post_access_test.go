package series

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/echovisionlab/geul-api/internal/auth"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const postAccessTestDriverName = "series-post-access-test"

var (
	registerPostAccessTestDriver sync.Once
	postAccessTestScenarios      sync.Map
)

type postAccessTestScenario struct {
	status     string
	missing    bool
	err        error
	identityID string
	memberID   string

	mu      sync.Mutex
	queries int
}

type postAccessTestDriver struct{}

func (postAccessTestDriver) Open(name string) (driver.Conn, error) {
	scenario, ok := postAccessTestScenarios.Load(name)
	if !ok {
		return nil, errors.New("unknown Post access test scenario")
	}
	return &postAccessTestConn{scenario: scenario.(*postAccessTestScenario)}, nil
}

type postAccessTestConn struct{ scenario *postAccessTestScenario }

func (*postAccessTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not supported")
}

func (*postAccessTestConn) Close() error { return nil }

func (*postAccessTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (connection *postAccessTestConn) ExecContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Result, error) {
	if !strings.Contains(query, "pg_advisory_xact_lock") {
		return nil, errors.New("unexpected Post access test exec")
	}
	return driver.RowsAffected(1), nil
}

func (connection *postAccessTestConn) QueryContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	switch {
	case strings.Contains(query, `FROM "post"`):
		connection.scenario.mu.Lock()
		connection.scenario.queries++
		connection.scenario.mu.Unlock()
		if connection.scenario.err != nil {
			return nil, connection.scenario.err
		}
		if connection.scenario.missing {
			return &postAccessTestRows{columns: []string{"status"}}, nil
		}
		return &postAccessTestRows{
			columns: []string{"status"},
			values:  [][]driver.Value{{connection.scenario.status}},
		}, nil
	case strings.Contains(query, `FROM "member"`):
		return &postAccessTestRows{
			columns: []string{"id", "onboarded", "active"},
			values:  [][]driver.Value{{connection.scenario.memberID, true, true}},
		}, nil
	case strings.Contains(query, "kratos") && strings.Contains(query, "identities"):
		return &postAccessTestRows{
			columns: []string{"id", "state", "banned"},
			values:  [][]driver.Value{{connection.scenario.identityID, auth.KratosStateActive, false}},
		}, nil
	default:
		return nil, errors.New("unexpected Post access test query")
	}
}

type postAccessTestRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (rows *postAccessTestRows) Columns() []string { return rows.columns }
func (*postAccessTestRows) Close() error           { return nil }

func (rows *postAccessTestRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}

func newPostAccessTestDB(t *testing.T, scenario *postAccessTestScenario) *gorm.DB {
	t.Helper()
	registerPostAccessTestDriver.Do(func() {
		sql.Register(postAccessTestDriverName, postAccessTestDriver{})
	})
	dsn := uuid.NewString()
	postAccessTestScenarios.Store(dsn, scenario)
	sqlDB, err := sql.Open(postAccessTestDriverName, dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sqlDB.Close())
		postAccessTestScenarios.Delete(dsn)
	})
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableAutomaticPing: true,
	})
	require.NoError(t, err)
	return db
}

func (scenario *postAccessTestScenario) queryCount() int {
	scenario.mu.Lock()
	defer scenario.mu.Unlock()
	return scenario.queries
}

type postAccessPermissionServer struct {
	v1.UnimplementedPermissionsServiceServer

	mu       sync.Mutex
	requests []*v1.CheckPermissionRequest
	allowed  bool
	err      error
}

func (server *postAccessPermissionServer) CheckPermission(
	_ context.Context,
	request *v1.CheckPermissionRequest,
) (*v1.CheckPermissionResponse, error) {
	server.mu.Lock()
	server.requests = append(server.requests, request)
	server.mu.Unlock()
	if server.err != nil {
		return nil, server.err
	}
	permissionship := v1.CheckPermissionResponse_PERMISSIONSHIP_NO_PERMISSION
	if server.allowed {
		permissionship = v1.CheckPermissionResponse_PERMISSIONSHIP_HAS_PERMISSION
	}
	return &v1.CheckPermissionResponse{Permissionship: permissionship}, nil
}

func (server *postAccessPermissionServer) requestSnapshot() []*v1.CheckPermissionRequest {
	server.mu.Lock()
	defer server.mu.Unlock()
	return append([]*v1.CheckPermissionRequest(nil), server.requests...)
}

func newPostAccessTestSpiceDB(
	t *testing.T,
	server *postAccessPermissionServer,
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

func postAccessTestContext(scenario *postAccessTestScenario) (context.Context, string) {
	identityID := uuid.NewString()
	memberID := uuid.NewString()
	scenario.identityID = identityID
	scenario.memberID = memberID
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		MemberID:      auth.MemberID(memberID),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
		Onboarded:     true,
	}), identityID
}

func TestPostAccessRequireLockedEditChecksExactPostActionOnce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		postStatus         string
		expectedPermission string
	}{
		{
			name:               "ordinary Post uses edit",
			postStatus:         managev1.PostStatus_POST_STATUS_DRAFT.String(),
			expectedPermission: "edit",
		},
		{
			name:               "archived Post uses edit_archived",
			postStatus:         managev1.PostStatus_POST_STATUS_ARCHIVED.String(),
			expectedPermission: "edit_archived",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			scenario := &postAccessTestScenario{status: test.postStatus}
			server := &postAccessPermissionServer{allowed: true}
			ctx, identityID := postAccessTestContext(scenario)
			postID := uuid.NewString()

			err := (PostAccess{}).RequireLockedEdit(
				ctx,
				newPostAccessTestDB(t, scenario),
				newPostAccessTestSpiceDB(t, server),
				postID,
			)
			require.NoError(t, err)
			require.Equal(t, 1, scenario.queryCount())
			requests := server.requestSnapshot()
			require.Len(t, requests, 1)
			require.Equal(t, "post", requests[0].GetResource().GetObjectType())
			require.Equal(t, postID, requests[0].GetResource().GetObjectId())
			require.Equal(t, test.expectedPermission, requests[0].GetPermission())
			require.Equal(t, "account_identity", requests[0].GetSubject().GetObject().GetObjectType())
			require.Equal(t, identityID, requests[0].GetSubject().GetObject().GetObjectId())
			require.NotNil(t, requests[0].GetConsistency().GetFullyConsistent())
		})
	}
}

func TestPostAccessRequireLockedEditMasksPrivateDenialAndMissing(t *testing.T) {
	t.Parallel()

	t.Run("denied", func(t *testing.T) {
		t.Parallel()
		scenario := &postAccessTestScenario{status: managev1.PostStatus_POST_STATUS_DRAFT.String()}
		server := &postAccessPermissionServer{}
		ctx, _ := postAccessTestContext(scenario)
		err := (PostAccess{}).RequireLockedEdit(
			ctx,
			newPostAccessTestDB(t, scenario),
			newPostAccessTestSpiceDB(t, server),
			uuid.NewString(),
		)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
		require.Len(t, server.requestSnapshot(), 1)
	})

	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		scenario := &postAccessTestScenario{missing: true}
		server := &postAccessPermissionServer{allowed: true}
		ctx, _ := postAccessTestContext(scenario)
		err := (PostAccess{}).RequireLockedEdit(
			ctx,
			newPostAccessTestDB(t, scenario),
			newPostAccessTestSpiceDB(t, server),
			uuid.NewString(),
		)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
		require.Empty(t, server.requestSnapshot())
	})

	t.Run("anonymous", func(t *testing.T) {
		t.Parallel()
		scenario := &postAccessTestScenario{status: managev1.PostStatus_POST_STATUS_DRAFT.String()}
		server := &postAccessPermissionServer{allowed: true}
		err := (PostAccess{}).RequireLockedEdit(
			context.Background(),
			newPostAccessTestDB(t, scenario),
			newPostAccessTestSpiceDB(t, server),
			uuid.NewString(),
		)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
		require.Empty(t, server.requestSnapshot())
	})
}

func TestPostAccessRequireLockedEditPreservesDependencyFailure(t *testing.T) {
	t.Parallel()
	scenario := &postAccessTestScenario{status: managev1.PostStatus_POST_STATUS_ARCHIVED.String()}
	server := &postAccessPermissionServer{err: status.Error(codes.Unavailable, "SpiceDB unavailable")}
	ctx, _ := postAccessTestContext(scenario)
	err := (PostAccess{}).RequireLockedEdit(
		ctx,
		newPostAccessTestDB(t, scenario),
		newPostAccessTestSpiceDB(t, server),
		uuid.NewString(),
	)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.Len(t, server.requestSnapshot(), 1)
}
