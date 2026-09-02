package mcp

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestWithAuthenticatedContextCreatesSessionlessDomainAndTelemetryPrincipal(t *testing.T) {
	t.Parallel()

	requestContext, err := sharedtelemetry.NewPublicRequestContext("127.0.0.1")
	if err != nil {
		t.Fatalf("NewPublicRequestContext() error = %v", err)
	}
	ctx := sharedtelemetry.WithRequestContext(t.Context(), requestContext)
	principal := validMCPPrincipal()

	authenticated, err := WithAuthenticatedContext(ctx, principal)
	if err != nil {
		t.Fatalf("WithAuthenticatedContext() error = %v", err)
	}
	user := auth.GetUser(authenticated)
	if user == nil || user.IdentityID.String() != principal.IdentityID || user.MemberID.String() != principal.MemberID ||
		!user.Authenticated || !user.Onboarded || user.Banned {
		t.Fatalf("UserInfo = %+v", user)
	}
	if user.SessionID != "" || !user.AuthenticatedAt.IsZero() {
		t.Fatalf("OAuth principal fabricated browser Session state: %+v", user)
	}
	resolved, ok := sharedtelemetry.RequestContextFrom(authenticated)
	if !ok {
		t.Fatal("telemetry request context disappeared")
	}
	wantActor := sharedtelemetry.MemberActor{IdentityID: principal.IdentityID, MemberID: principal.MemberID}
	if resolved.Actor != wantActor {
		t.Fatalf("telemetry actor = %+v, want %+v", resolved.Actor, wantActor)
	}
}

func TestWithAuthenticatedContextRejectsIncompletePrincipal(t *testing.T) {
	t.Parallel()

	valid := validMCPPrincipal()
	tests := []mcpserver.Principal{
		{},
		{IdentityID: valid.IdentityID, MemberID: valid.MemberID, DelegationID: valid.DelegationID, DelegationName: valid.DelegationName},
		{MemberID: valid.MemberID, DelegationID: valid.DelegationID, DelegationName: valid.DelegationName, DelegationMethod: valid.DelegationMethod},
		{IdentityID: valid.IdentityID, DelegationID: valid.DelegationID, DelegationName: valid.DelegationName, DelegationMethod: valid.DelegationMethod},
		{IdentityID: valid.IdentityID, MemberID: valid.MemberID, DelegationName: valid.DelegationName, DelegationMethod: valid.DelegationMethod},
		{IdentityID: valid.IdentityID, MemberID: valid.MemberID, DelegationID: valid.DelegationID, DelegationMethod: valid.DelegationMethod},
		{IdentityID: valid.IdentityID, MemberID: valid.MemberID, DelegationID: valid.DelegationID, DelegationName: valid.DelegationName, DelegationMethod: "unknown"},
	}
	for _, principal := range tests {
		if ctx, err := WithAuthenticatedContext(t.Context(), principal); !errors.Is(err, ErrInvalidPrincipal) || auth.GetUser(ctx) != nil {
			t.Fatalf("WithAuthenticatedContext(%+v) = %v, %v", principal, ctx, err)
		}
	}
}

func TestWithAuthenticatedContextPreservesOAuthDelegation(t *testing.T) {
	t.Parallel()
	principal := validMCPPrincipal()
	ctx, err := WithAuthenticatedContext(t.Context(), principal)
	if err != nil {
		t.Fatal(err)
	}
	can, err := policyv1.Platform.IsAuthor()
	if err != nil {
		t.Fatal(err)
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delegation().Kind() != policyv1.DelegationMCPOAuth ||
		decision.Delegation().DelegationID() != principal.DelegationID ||
		decision.Delegation().DelegationDisplayName() != principal.DelegationName {
		t.Fatalf("delegation = %+v", decision.Delegation())
	}
}

type dispatcherFunc func(context.Context, mcpserver.Principal, string, mcpserver.ToolArguments) (mcpserver.ToolResult, error)

type registryFunc func(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error)

func (function registryFunc) ListTools(
	ctx context.Context,
	principal mcpserver.Principal,
) ([]mcpserver.Tool, error) {
	return function(ctx, principal)
}

func (function dispatcherFunc) CallTool(
	ctx context.Context,
	principal mcpserver.Principal,
	name string,
	arguments mcpserver.ToolArguments,
) (mcpserver.ToolResult, error) {
	return function(ctx, principal, name, arguments)
}

func TestWrapDispatcherInjectsNormalAuthContext(t *testing.T) {
	t.Parallel()

	principal := validMCPPrincipal()
	arguments := mcpserver.ToolArguments{"document": []byte(`"post:1"`)}
	next := dispatcherFunc(func(ctx context.Context, got mcpserver.Principal, name string, gotArguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
		if !reflect.DeepEqual(got, principal) || name != "document_read" || string(gotArguments["document"]) != `"post:1"` {
			t.Fatalf("dispatch inputs = principal:%+v name:%q arguments:%v", got, name, gotArguments)
		}
		user := auth.GetUser(ctx)
		if user == nil || user.IdentityID.String() != principal.IdentityID || user.MemberID.String() != principal.MemberID {
			t.Fatalf("dispatcher UserInfo = %+v", user)
		}
		if user.SessionID != "" {
			t.Fatalf("dispatcher received fake Session %q", user.SessionID)
		}
		return mcpserver.ToolResult{Content: []mcpserver.ContentBlock{mcpserver.TextContent("ok")}}, nil
	})
	wrapper, err := WrapDispatcher(next)
	if err != nil {
		t.Fatalf("WrapDispatcher() error = %v", err)
	}
	result, err := wrapper.CallTool(t.Context(), principal, "document_read", arguments)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := WrapDispatcher(nil); !errors.Is(err, ErrInvalidDependency) {
		t.Fatalf("WrapDispatcher(nil) error = %v", err)
	}
}

func TestWrapRegistryInjectsNormalAuthContextAndAllowsEmptySnapshot(t *testing.T) {
	t.Parallel()

	principal := validMCPPrincipal()
	next := registryFunc(func(ctx context.Context, got mcpserver.Principal) ([]mcpserver.Tool, error) {
		if !reflect.DeepEqual(got, principal) {
			t.Fatalf("registry principal = %+v", got)
		}
		user := auth.GetUser(ctx)
		if user == nil || user.IdentityID.String() != principal.IdentityID || user.MemberID.String() != principal.MemberID {
			t.Fatalf("registry UserInfo = %+v", user)
		}
		if user.SessionID != "" {
			t.Fatalf("registry received fake Session %q", user.SessionID)
		}
		return nil, nil
	})
	wrapper, err := WrapRegistry(next)
	if err != nil {
		t.Fatalf("WrapRegistry() error = %v", err)
	}
	tools, err := wrapper.ListTools(t.Context(), principal)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if tools != nil {
		t.Fatalf("empty registry snapshot changed before core normalization: %+v", tools)
	}
	if _, err := WrapRegistry(nil); !errors.Is(err, ErrInvalidDependency) {
		t.Fatalf("WrapRegistry(nil) error = %v", err)
	}
}

func validMCPPrincipal() mcpserver.Principal {
	return mcpserver.Principal{
		IdentityID:       testIdentity,
		MemberID:         testMember,
		DelegationID:     "https://client.example/mcp.json",
		DelegationName:   "Example Member · Example Client",
		DelegationMethod: mcpserver.DelegationMethodMCPOAuth,
	}
}
