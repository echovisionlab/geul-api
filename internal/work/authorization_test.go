package work

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	authzedv1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type workListPermissionServer struct {
	authzedv1.UnimplementedPermissionsServiceServer
}

func (*workListPermissionServer) CheckPermission(context.Context, *authzedv1.CheckPermissionRequest) (*authzedv1.CheckPermissionResponse, error) {
	return &authzedv1.CheckPermissionResponse{Permissionship: authzedv1.CheckPermissionResponse_PERMISSIONSHIP_HAS_PERMISSION}, nil
}

func TestListWorksAdminPagesKeepTiesUniqueAndRequestedDirection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	authzedv1.RegisterPermissionsServiceServer(server, &workListPermissionServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	spiceDB, err := auth.NewSpiceDBClient(listener.Addr().String(), "test-token", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = spiceDB.Close() })
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Exec(`CREATE TABLE work (id TEXT PRIMARY KEY, updated_at DATETIME NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	const a = "11111111-1111-4111-8111-111111111111"
	const b = "22222222-2222-4222-8222-222222222222"
	const c = "33333333-3333-4333-8333-333333333333"
	const old = "44444444-4444-4444-8444-444444444444"
	const newest = "55555555-5555-4555-8555-555555555555"
	for _, row := range []struct {
		id        string
		updatedAt time.Time
	}{
		{c, base}, {a, base}, {b, base}, {old, base.Add(-time.Hour)}, {newest, base.Add(time.Hour)},
	} {
		if err := db.Exec(`INSERT INTO work (id, updated_at) VALUES (?, ?)`, row.id, row.updatedAt).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Observe the actual service SELECT after SQLite executes it. Stop before
	// unrelated content/asset projections, which require the full domain fixture.
	stop := errors.New("work list data query observed")
	var selected []model.Work
	var statement string
	if err := db.Callback().Query().After("gorm:query").Register("test:observe_work_list", func(query *gorm.DB) {
		if rows, ok := query.Statement.Dest.(*[]model.Work); ok && query.Error == nil {
			selected = append([]model.Work(nil), (*rows)...)
			statement = query.Statement.SQL.String()
			query.AddError(stop)
		}
	}); err != nil {
		t.Fatal(err)
	}
	service := &WorkService{db: db, spiceDB: spiceDB}
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString()), SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true})
	for _, test := range []struct {
		name  string
		order commonv1.SortOrder
		want  []string
	}{
		{"ascending", commonv1.SortOrder_SORT_ORDER_ASC, []string{old, a, b, c, newest}},
		{"descending", commonv1.SortOrder_SORT_ORDER_DESC, []string{newest, a, b, c, old}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var ids []string
			for offset := range test.want {
				selected, statement = nil, ""
				_, err := service.ListWorksAdmin(ctx, connect.NewRequest(&managev1.ListWorksAdminRequest{
					Pagination: &commonv1.PaginationRequest{Limit: 1, Offset: int32(offset)},
					Sorts:      []*commonv1.SortSpec{{Field: "updated_at", Order: test.order}},
				}))
				if err == nil || !strings.Contains(err.Error(), stop.Error()) || len(selected) != 1 {
					t.Fatalf("list did not reach data query: rows=%+v err=%v", selected, err)
				}
				ids = append(ids, selected[0].ID)
			}
			if !reflect.DeepEqual(ids, test.want) {
				t.Fatalf("one-row pages = %v, want %v; last query=%s", ids, test.want, statement)
			}
		})
	}
}

func TestGetWorkClientsDistinguishesEmptyResultFromQueryFailure(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, statement := range []string{
		`CREATE TABLE client (id TEXT PRIMARY KEY, name TEXT, website TEXT, logo_light_file_id TEXT, logo_dark_file_id TEXT)`,
		`CREATE TABLE work_client (work_id TEXT, client_id TEXT, sort_order INTEGER)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	const workID = "22222222-2222-4222-8222-222222222222"
	const clientID = "11111111-1111-4111-8111-111111111111"
	service := &WorkService{db: db}
	clients, err := service.getWorkClients(t.Context(), workID)
	if err != nil || len(clients) != 0 {
		t.Fatalf("empty client read = %+v, err=%v", clients, err)
	}
	if err := db.Exec(`INSERT INTO client (id, name) VALUES (?, ?)`, clientID, "Existing client").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO work_client (work_id, client_id, sort_order) VALUES (?, ?, 0)`, workID, clientID).Error; err != nil {
		t.Fatal(err)
	}
	clients, err = service.getWorkClients(t.Context(), workID)
	if err != nil || len(clients) != 1 || clients[0].Id != clientID {
		t.Fatalf("populated client read = %+v, err=%v", clients, err)
	}
	queryFailure := errors.New("client query unavailable")
	failures := 0
	// Inject failure only into the real client JOIN, after verifying its success.
	if err := db.Callback().Row().Before("gorm:row").Register("test:fail_work_client_read", func(query *gorm.DB) {
		if query.Statement.TableExpr != nil && query.Statement.TableExpr.SQL == "work_client wc" {
			failures++
			query.AddError(queryFailure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	clients, err = service.getWorkClients(t.Context(), workID)
	if failures != 1 || clients != nil || connect.CodeOf(err) != connect.CodeInternal || !errors.Is(err, queryFailure) {
		t.Fatalf("failed client read = %+v, err=%v, injected failures=%d", clients, err, failures)
	}
}

type workAuthorizationRecordingChecker struct {
	calls    int
	decision policyv1.AuthorizationDecision
}

func (c *workAuthorizationRecordingChecker) Can(
	_ context.Context,
	decision policyv1.AuthorizationDecision,
) (bool, error) {
	c.calls++
	c.decision = decision
	return true, nil
}

func (c *workAuthorizationRecordingChecker) CheckActorCan(
	_ context.Context,
	_ policyv1.Actor,
	_ policyv1.Can,
) (bool, error) {
	return true, nil
}

func TestWorkLifecyclePermissionSelectsExactSinglePermission(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status string
		normal workAction
		use    workAuthorizationUse
		want   workAction
	}{
		{name: "draft publish", status: managev1.WorkStatus_WORK_STATUS_DRAFT.String(), normal: policyv1.Work.Publish, use: workAuthorizationMutation, want: policyv1.Work.Publish},
		{name: "archived editor read", status: managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(), normal: policyv1.Work.Edit, use: workAuthorizationRead, want: policyv1.Work.ViewArchived},
		{name: "archived publish mutation", status: managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(), normal: policyv1.Work.Publish, use: workAuthorizationMutation, want: policyv1.Work.EditArchived},
		{name: "archived manage mutation", status: managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(), normal: policyv1.Work.Manage, use: workAuthorizationMutation, want: policyv1.Work.EditArchived},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			const workID = "33333333-3333-4333-8333-333333333333"
			got, err := workLifecycleAction(test.status, test.normal, test.use)(workID)
			if err != nil {
				t.Fatal(err)
			}
			want, err := test.want(workID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Action().Permission() != want.Action().Permission() {
				t.Fatalf("permission = %q, want %q", got.Action().Permission(), want.Action().Permission())
			}
		})
	}
}

func TestRequireWorkPermissionForSubjectUsesOneExactObjectDecision(t *testing.T) {
	t.Parallel()
	workID := "33333333-3333-4333-8333-333333333333"
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID("44444444-4444-4444-8444-444444444444"),
		SessionID:     auth.SessionID("55555555-5555-4555-8555-555555555555"),
		Authenticated: true,
	})
	checker := &workAuthorizationRecordingChecker{}
	want, err := policyv1.Work.EditArchived(workID)
	if err != nil {
		t.Fatal(err)
	}

	if err := requireWorkPermissionForCurrentActor(ctx, checker, workID, policyv1.Work.EditArchived); err != nil {
		t.Fatal(err)
	}
	if checker.calls != 1 {
		t.Fatalf("permission checks = %d, want exactly 1", checker.calls)
	}
	if checker.decision.Resource() != want.Resource() {
		t.Fatalf("resource = %s:%s, want %s:%s", checker.decision.Resource().Type(), checker.decision.Resource().ID(), want.Resource().Type(), want.Resource().ID())
	}
	if checker.decision.Action().Permission() != want.Action().Permission() {
		t.Fatalf("permission = %q, want %q", checker.decision.Action().Permission(), want.Action().Permission())
	}
}

func TestRequireWorkGlobalActionsUseDomainCatalog(t *testing.T) {
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID:    auth.IdentityID("44444444-4444-4444-8444-444444444444"),
		SessionID:     auth.SessionID("55555555-5555-4555-8555-555555555555"),
		Authenticated: true,
	})
	for _, test := range []struct {
		name    string
		require func(context.Context, CollaborationPermissionChecker) error
		want    func() (policyv1.Can, error)
	}{
		{name: "create", require: requireWorkCreate, want: policyv1.Work.Create},
		{name: "list", require: requireWorkList, want: policyv1.Work.List},
	} {
		t.Run(test.name, func(t *testing.T) {
			checker := &workAuthorizationRecordingChecker{}
			want, err := test.want()
			if err != nil {
				t.Fatal(err)
			}
			if err := test.require(ctx, checker); err != nil {
				t.Fatal(err)
			}
			if checker.calls != 1 {
				t.Fatalf("permission checks = %d, want exactly 1", checker.calls)
			}
			if checker.decision.Resource() != want.Resource() {
				t.Fatalf("resource = %s:%s, want %s:%s", checker.decision.Resource().Type(), checker.decision.Resource().ID(), want.Resource().Type(), want.Resource().ID())
			}
			if checker.decision.Action().Name() != want.Action().Name() {
				t.Fatalf("action = %q, want %q", checker.decision.Action().Name(), want.Action().Name())
			}
			if checker.decision.Action().Permission() != want.Action().Permission() {
				t.Fatalf("permission = %q, want %q", checker.decision.Action().Permission(), want.Action().Permission())
			}
		})
	}
}
