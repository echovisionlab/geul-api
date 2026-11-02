package authztest

import (
	"context"
	"errors"
	"net"
	"testing"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/echovisionlab/geul-api/internal/auth"
	"google.golang.org/grpc"
)

// Client serves the Checker's rules as a loopback SpiceDB permissions
// service and returns a real client of it, for a use case that still takes
// *auth.SpiceDBClient. Only CheckPermission is served; any other call fails
// as unimplemented, so a test notices when it needs more than checks.
func (c *Checker) Client(t testing.TB) *auth.SpiceDBClient {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("authztest: listen: %v", err)
	}
	server := grpc.NewServer()
	v1.RegisterPermissionsServiceServer(server, permissionServer{checker: c})
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	client, err := auth.NewSpiceDBClient(listener.Addr().String(), "authztest", true)
	if err != nil {
		server.Stop()
		t.Fatalf("authztest: connect: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("authztest: close client: %v", err)
		}
		server.GracefulStop()
		if err := <-served; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("authztest: serve: %v", err)
		}
	})
	return client
}

type permissionServer struct {
	v1.UnimplementedPermissionsServiceServer
	checker *Checker
}

func (s permissionServer) CheckPermission(_ context.Context, request *v1.CheckPermissionRequest) (*v1.CheckPermissionResponse, error) {
	allowed, err := s.checker.answer(Check{
		IdentityID:   request.GetSubject().GetObject().GetObjectId(),
		ResourceType: request.GetResource().GetObjectType(),
		ResourceID:   request.GetResource().GetObjectId(),
		Permission:   request.GetPermission(),
	})
	if err != nil {
		return nil, err
	}
	permissionship := v1.CheckPermissionResponse_PERMISSIONSHIP_NO_PERMISSION
	if allowed {
		permissionship = v1.CheckPermissionResponse_PERMISSIONSHIP_HAS_PERMISSION
	}
	return &v1.CheckPermissionResponse{Permissionship: permissionship}, nil
}
