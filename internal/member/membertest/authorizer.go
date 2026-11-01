package membertest

import (
	"context"
	"sync"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/member"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Authorizer is the Member authorization port over an authztest.Checker: the
// checks and relationship writes are the Checker's, the lookups and the
// direct roles are the test's.
type Authorizer struct {
	*authztest.Checker

	mu sync.Mutex
	// Subjects answers LookupGlobalSubjects by the lookup's subject
	// permission: the Identities that hold it.
	Subjects map[string][]string
	// Resources answers LookupResources by resource type.
	Resources map[string][]string
	// Roles answers ReadDirectGlobalRole by Identity.
	Roles map[string]policyv1.RoleID
	// LookupErr fails every lookup.
	LookupErr error
}

var _ member.Authorizer = (*Authorizer)(nil)

// NewAuthorizer returns the port over checker.
func NewAuthorizer(checker *authztest.Checker) *Authorizer {
	return &Authorizer{Checker: checker, Subjects: map[string][]string{}, Resources: map[string][]string{}, Roles: map[string]policyv1.RoleID{}}
}

// Authors is the Subjects key of the global author lookup.
func Authors() string { return lookupKey(policyv1.Platform.LookupAuthorSubjects()) }

// Admins is the Subjects key of the global administrator lookup.
func Admins() string { return lookupKey(policyv1.Platform.LookupAdminSubjects()) }

func lookupKey(lookup policyv1.SubjectLookup) string {
	return lookup.Permission()
}

// LookupGlobalSubjects implements member.Authorizer.
func (a *Authorizer) LookupGlobalSubjects(_ context.Context, lookup policyv1.SubjectLookup) ([]auth.AccountIdentitySubject, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.LookupErr != nil {
		return nil, a.LookupErr
	}
	var subjects []auth.AccountIdentitySubject
	for _, identityID := range a.Subjects[lookupKey(lookup)] {
		subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
		if err != nil {
			return nil, err
		}
		subjects = append(subjects, subject)
	}
	return subjects, nil
}

// LookupResources implements member.Authorizer.
func (a *Authorizer) LookupResources(_ context.Context, lookup policyv1.ResourceLookup, _ policyv1.Actor) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.LookupErr != nil {
		return nil, a.LookupErr
	}
	return a.Resources[lookup.ResourceType()], nil
}

// ReadDirectGlobalRole implements member.Authorizer.
func (a *Authorizer) ReadDirectGlobalRole(_ context.Context, subject auth.AccountIdentitySubject) (policyv1.RoleID, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	role, found := a.Roles[subject.ID.String()]
	return role, found, nil
}
