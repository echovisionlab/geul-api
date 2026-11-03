package oauthapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Decision answers whether the signed-in Member may sign in through a client.
type Decision int

const (
	// DecisionNotSignInApp is a client that is not a sign-in app; the MCP
	// connection rules apply to it.
	DecisionNotSignInApp Decision = iota + 1
	DecisionAllowed
	DecisionDenied
)

// AuthorizeOAuthSignIn decides for the signed-in Member alone whether the
// sign-in app clientID admits them. The login app asks it on every sign-in.
func (s *Service) AuthorizeOAuthSignIn(ctx context.Context, clientID string) (Decision, error) {
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return 0, err
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return 0, domainerr.Required("client_id")
	}
	var record Record
	err := s.units.Read(ctx, func(store Store) (err error) {
		record, err = store.App(ctx, clientID)
		return err
	})
	if errors.Is(err, ErrAppNotFound) {
		return DecisionNotSignInApp, nil
	}
	if err != nil {
		return 0, err
	}
	allowed, err := s.admits(ctx, auth.GetUser(ctx), record)
	if err != nil || !allowed {
		return DecisionDenied, err
	}
	return DecisionAllowed, nil
}

func (s *Service) admits(ctx context.Context, user *auth.UserInfo, record Record) (bool, error) {
	if record.Access == AccessNone || user.Banned {
		return false, nil
	}
	active, err := s.eligibility.ActiveMember(ctx, user)
	if err != nil || !active {
		return false, err
	}
	switch record.Access {
	case AccessAdmins:
		return s.isAdmin(ctx)
	case AccessMembers:
		return true, nil
	case AccessMemberCriteria:
		return s.eligibility.MatchesAny(ctx, user, record.MemberCriteriaIDs)
	default:
		return false, nil
	}
}

func (s *Service) isAdmin(ctx context.Context) (bool, error) {
	can, err := policyv1.Platform.IsAdmin()
	if err != nil {
		return false, fmt.Errorf("build authorization check: %w", err)
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := s.spiceDB.Can(ctx, decision)
	if err != nil {
		return false, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	return allowed, nil
}
