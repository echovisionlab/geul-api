package audience

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// GrantsAuthenticatedAccess reports whether a download policy may grant
// authenticated access through the segment: it is active and selects Members
// by tag or filter.
func (s Segment) GrantsAuthenticatedAccess() bool {
	return s.ArchivedAt == nil && authenticatedAccessSegmentType(s.Type)
}

func authenticatedAccessSegmentType(segmentType SegmentType) bool {
	switch segmentType.trimmed() {
	case SegmentMemberTags, SegmentMembersByFilter:
		return true
	default:
		return false
	}
}

// RequireAuthenticatedAccessSegments validates the segments a download
// policy names against the ones its caller locked
// (audience/postgres.ValidateAuthenticatedAccessSegmentIDs): every one must
// exist and grant authenticated access.
func RequireAuthenticatedAccessSegments(segmentIDs []string, locked []Segment) error {
	if len(locked) != len(segmentIDs) {
		return invalidAuthenticatedAccessSegments()
	}
	for _, segment := range locked {
		if !segment.GrantsAuthenticatedAccess() {
			return invalidAuthenticatedAccessSegments()
		}
	}
	return nil
}

func invalidAuthenticatedAccessSegments() error {
	return domainerr.InvalidField(
		"audience_segment_ids",
		"one or more audience segments do not exist or cannot grant authenticated access",
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
	can, err := policyv1.AudienceSegment.ListAuthenticatedAccess()
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
