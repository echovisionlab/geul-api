package authenticationtest

import (
	"context"
	"errors"
	"sync"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

var errDuplicateIssuance = errors.New(`duplicate key value violates unique constraint "auth_code_issuance_pkey"`)

// Authorizer is an authztest.Checker that also answers the direct global
// role of each account Identity. A relationship write is recorded by the
// Checker and does not change the roles a test set.
type Authorizer struct {
	*authztest.Checker

	mu      sync.Mutex
	roles   map[string]policyv1.RoleID
	readErr error
	reads   []string
}

// NewAuthorizer returns an Authorizer over checker in which no Identity has
// a direct role.
func NewAuthorizer(checker *authztest.Checker) *Authorizer {
	return &Authorizer{Checker: checker, roles: map[string]policyv1.RoleID{}}
}

// SetRole gives identityID the direct global role.
func (a *Authorizer) SetRole(identityID string, role policyv1.RoleID) *Authorizer {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roles[identityID] = role
	return a
}

// FailReads makes every later direct role read fail with err.
func (a *Authorizer) FailReads(err error) *Authorizer {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.readErr = err
	return a
}

// Reads returns the Identities whose direct role was read, in order.
func (a *Authorizer) Reads() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.reads...)
}

// ReadDirectGlobalRole implements authentication.Authorizer.
func (a *Authorizer) ReadDirectGlobalRole(_ context.Context, subject auth.AccountIdentitySubject) (policyv1.RoleID, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	identityID := subject.ID.String()
	a.reads = append(a.reads, identityID)
	if a.readErr != nil {
		return policyv1.RoleID{}, false, a.readErr
	}
	role, found := a.roles[identityID]
	return role, found, nil
}

// Sessions answers the principal of each session a test stored.
type Sessions struct {
	mu         sync.Mutex
	principals map[string]*auth.UserInfo
	first      map[string]bool
	err        error
}

// NewSessions returns Sessions that know no session.
func NewSessions() *Sessions {
	return &Sessions{principals: map[string]*auth.UserInfo{}, first: map[string]bool{}}
}

// Store makes sessionID resolve to principal, as its Identity's first
// session when first is set.
func (s *Sessions) Store(sessionID string, principal *auth.UserInfo, first bool) *Sessions {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.principals[sessionID] = principal
	s.first[sessionID] = first
	return s
}

// Fail makes every later read fail with err, as an unreachable database
// does.
func (s *Sessions) Fail(err error) *Sessions {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
	return s
}

// Principal implements authentication.Sessions. An unknown session answers
// auth.ErrSessionPrincipalInvalid.
func (s *Sessions) Principal(_ context.Context, sessionID string) (*auth.UserInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	principal, found := s.principals[sessionID]
	if !found {
		return nil, auth.ErrSessionPrincipalInvalid
	}
	copied := *principal
	return &copied, nil
}

// FirstSession implements authentication.Sessions.
func (s *Sessions) FirstSession(_ context.Context, sessionID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	return s.first[sessionID], nil
}

var (
	_ authentication.Authorizer = (*Authorizer)(nil)
	_ authentication.Sessions   = (*Sessions)(nil)
)
