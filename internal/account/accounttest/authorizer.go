package accounttest

import (
	"context"
	"slices"
	"sync"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Authorizer is the Account authorization port over an authztest.Checker:
// the checks and relationship writes are the Checker's, the global role
// reads, the lookups and the relationship removal of a deleted Identity are
// the test's.
type Authorizer struct {
	*authztest.Checker

	mu sync.Mutex
	// Subjects answers LookupGlobalSubjects by the lookup's subject
	// permission: the Identities that hold it.
	Subjects map[string][]string
	// Roles answers ReadDirectGlobalRole by Identity.
	Roles map[string]policyv1.RoleID
	// LookupErr fails every lookup; DeleteErr fails every relationship
	// removal.
	LookupErr, DeleteErr error
	deleted              []string
}

var (
	_ account.Authorizer         = (*Authorizer)(nil)
	_ account.DeletionAuthorizer = (*Authorizer)(nil)
	_ account.GlobalRoles        = (*Authorizer)(nil)
)

// NewAuthorizer returns the port over checker.
func NewAuthorizer(checker *authztest.Checker) *Authorizer {
	return &Authorizer{Checker: checker, Subjects: map[string][]string{}, Roles: map[string]policyv1.RoleID{}}
}

// Admins is the Subjects key of the global administrator lookup.
func Admins() string { return policyv1.Platform.LookupAdminSubjects().Permission() }

// LookupGlobalSubjects implements account.Authorizer.
func (a *Authorizer) LookupGlobalSubjects(_ context.Context, lookup policyv1.SubjectLookup) ([]auth.AccountIdentitySubject, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.LookupErr != nil {
		return nil, a.LookupErr
	}
	var subjects []auth.AccountIdentitySubject
	for _, identityID := range a.Subjects[lookup.Permission()] {
		subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
		if err != nil {
			return nil, err
		}
		subjects = append(subjects, subject)
	}
	return subjects, nil
}

// ReadDirectGlobalRole implements account.Authorizer.
func (a *Authorizer) ReadDirectGlobalRole(_ context.Context, subject auth.AccountIdentitySubject) (policyv1.RoleID, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	role, found := a.Roles[subject.ID.String()]
	return role, found, nil
}

// DeleteAllAccountIdentityRelationships implements account.Authorizer.
func (a *Authorizer) DeleteAllAccountIdentityRelationships(_ context.Context, subject auth.AccountIdentitySubject) (auth.ZedToken, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.DeleteErr != nil {
		return auth.ZedToken{}, a.DeleteErr
	}
	a.deleted = append(a.deleted, subject.ID.String())
	return auth.ZedToken{}, nil
}

// Deleted returns the Identities whose relationships were removed, in order.
func (a *Authorizer) Deleted() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.deleted)
}
