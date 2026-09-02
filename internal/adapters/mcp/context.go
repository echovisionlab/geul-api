package mcp

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// WithAuthenticatedContext adds the normal Geul UserInfo and telemetry Member
// actor for an already authenticated MCP principal. It leaves SessionID and
// AuthenticatedAt empty because an OAuth access token is not a browser Session.
func WithAuthenticatedContext(
	ctx context.Context,
	principal mcpserver.Principal,
) (context.Context, error) {
	if strings.TrimSpace(principal.IdentityID) == "" ||
		strings.TrimSpace(principal.MemberID) == "" ||
		strings.TrimSpace(principal.DelegationID) == "" ||
		strings.TrimSpace(principal.DelegationName) == "" ||
		principal.DelegationMethod != mcpserver.DelegationMethodMCPOAuth {
		return ctx, ErrInvalidPrincipal
	}

	authenticated := auth.WithUser(ctx, &auth.UserInfo{
		IdentityID:    auth.IdentityID(principal.IdentityID),
		MemberID:      auth.MemberID(principal.MemberID),
		Authenticated: true,
		Onboarded:     true,
	})
	delegation, err := policyv1.MCPOAuth(principal.DelegationID, principal.DelegationName)
	if err != nil {
		return ctx, ErrInvalidPrincipal
	}
	authenticated, err = auth.WithAuthorizationDelegation(authenticated, delegation)
	if err != nil {
		return ctx, ErrInvalidPrincipal
	}
	return authenticated, nil
}

// WrapDispatcher guarantees that owning-domain dispatch runs with the same
// auth.UserInfo and telemetry Member actor used by normal domain checks.
func WrapDispatcher(next mcpserver.ToolDispatcher) (mcpserver.ToolDispatcher, error) {
	if interfaceValueIsNil(next) {
		return nil, ErrInvalidDependency
	}
	return authenticatedDispatcher{next: next}, nil
}

// WrapRegistry guarantees that Member-visible tool discovery uses the same
// authenticated domain and telemetry context as tool dispatch. A non-nil
// registry may return an empty per-Member snapshot.
func WrapRegistry(next mcpserver.ToolRegistry) (mcpserver.ToolRegistry, error) {
	if interfaceValueIsNil(next) {
		return nil, ErrInvalidDependency
	}
	return authenticatedRegistry{next: next}, nil
}

type authenticatedRegistry struct {
	next mcpserver.ToolRegistry
}

func (registry authenticatedRegistry) ListTools(
	ctx context.Context,
	principal mcpserver.Principal,
) ([]mcpserver.Tool, error) {
	authenticated, err := WithAuthenticatedContext(ctx, principal)
	if err != nil {
		return nil, err
	}
	return registry.next.ListTools(authenticated, principal)
}

type authenticatedDispatcher struct {
	next mcpserver.ToolDispatcher
}

func (dispatcher authenticatedDispatcher) CallTool(
	ctx context.Context,
	principal mcpserver.Principal,
	name string,
	arguments mcpserver.ToolArguments,
) (mcpserver.ToolResult, error) {
	authenticated, err := WithAuthenticatedContext(ctx, principal)
	if err != nil {
		return mcpserver.ToolResult{}, err
	}
	return dispatcher.next.CallTool(authenticated, principal, name, arguments)
}
