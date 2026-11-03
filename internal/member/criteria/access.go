package criteria

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// GrantsAuthenticatedAccess reports whether a download policy may grant
// authenticated access through the criteria: it is active and selects Members
// by tag or filter.
func (s Criteria) GrantsAuthenticatedAccess() bool {
	return s.ArchivedAt == nil && authenticatedAccessKind(s.Type)
}

func authenticatedAccessKind(kind Kind) bool {
	switch kind.trimmed() {
	case KindMemberTags, KindFilter:
		return true
	default:
		return false
	}
}

// RequireAuthenticatedAccessCriteriaSet validates the Member criteria a download
// policy names against the ones its caller locked
// (criteria/postgres.ValidateAuthenticatedAccessCriteriaIDs): every one must
// exist and grant authenticated access.
func RequireAuthenticatedAccessCriteriaSet(criteriaIDs []string, locked []Criteria) error {
	if len(locked) != len(criteriaIDs) {
		return invalidAuthenticatedAccessCriteriaSet()
	}
	for _, criteria := range locked {
		if !criteria.GrantsAuthenticatedAccess() {
			return invalidAuthenticatedAccessCriteriaSet()
		}
	}
	return nil
}

func invalidAuthenticatedAccessCriteriaSet() error {
	return domainerr.InvalidField(
		"member_criteria_ids",
		"one or more Member criteria do not exist or cannot grant authenticated access",
	)
}

func requireDownloadPolicyAuthor(ctx context.Context, spiceDB authz.AuthorizationDecisionChecker) error {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || strings.TrimSpace(user.MemberID.String()) == "" {
		return domainerr.New(domainerr.Unauthenticated, "", "authentication required")
	}
	if user.Banned {
		return domainerr.Denied("account is banned")
	}
	can, err := policyv1.MemberCriteria.ListAuthenticatedAccess()
	if err != nil {
		return err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return domainerr.New(domainerr.Unauthenticated, "", "authentication required").WithCause(err)
	}
	allowed, err := spiceDB.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return domainerr.Denied("author access required")
	}
	return nil
}
