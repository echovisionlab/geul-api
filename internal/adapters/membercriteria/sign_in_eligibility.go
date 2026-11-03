package criteria

import (
	"context"

	"gorm.io/gorm"

	mediaassetadapter "github.com/echovisionlab/geul-api/internal/adapters/mediaasset"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/criteriamatch"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	criteriapostgres "github.com/echovisionlab/geul-api/internal/member/criteria/postgres"
	"github.com/echovisionlab/geul-api/internal/oauthapp"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// SignInEligibility decides who a sign-in app admits from the Member facts
// restricted downloads read and the Member criteria rule they share
// (criteriamatch), so a criteria selects the same Members for both.
type SignInEligibility struct {
	db      *gorm.DB
	spiceDB authz.AuthorizationDecisionChecker
	facts   mediaasset.DownloadStore
}

var _ oauthapp.Eligibility = SignInEligibility{}

// NewSignInEligibility returns the sign-in eligibility of db and spiceDB.
func NewSignInEligibility(db *gorm.DB, spiceDB authz.AuthorizationDecisionChecker) SignInEligibility {
	return SignInEligibility{
		db: db, spiceDB: spiceDB,
		facts: mediaassetpostgres.NewDownloadStore(db, mediaassetadapter.NewCriteriaConfigs()),
	}
}

func (e SignInEligibility) CriteriaSummaries(ctx context.Context, ids []string) ([]oauthapp.CriteriaSummary, error) {
	criteriaSet, err := criteriapostgres.CriteriaSetByID(ctx, e.db, ids)
	if err != nil {
		return nil, err
	}
	summaries := make([]oauthapp.CriteriaSummary, len(criteriaSet))
	for i, criteria := range criteriaSet {
		summaries[i] = oauthapp.CriteriaSummary{
			ID: criteria.ID, Name: criteria.Name, Description: criteria.Description, Kind: string(criteria.Type),
			Eligible: criteria.GrantsAuthenticatedAccess(),
		}
	}
	return summaries, nil
}

func (e SignInEligibility) ActiveMember(ctx context.Context, user *auth.UserInfo) (bool, error) {
	_, found, err := e.member(ctx, user)
	return found, err
}

func (e SignInEligibility) MatchesAny(ctx context.Context, user *auth.UserInfo, ids []string) (bool, error) {
	member, found, err := e.member(ctx, user)
	if err != nil || !found || len(ids) == 0 {
		return false, err
	}
	stored, err := e.facts.CriteriaSet(ctx, ids, true)
	if err != nil {
		return false, err
	}
	criteriaSet := make([]criteriamatch.Criteria, 0, len(stored))
	for _, criteria := range stored {
		if eligible := (membercriteria.Criteria{Type: membercriteria.Kind(criteria.Kind)}).GrantsAuthenticatedAccess(); eligible {
			criteriaSet = append(criteriaSet, criteriamatch.Criteria{ID: criteria.ID, Kind: criteria.Kind, Config: criteria.Config})
		}
	}
	facts, err := e.matchFacts(ctx, user, member.MemberID, criteriaSet)
	if err != nil {
		return false, err
	}
	matchMember := criteriamatch.Member{ID: member.MemberID, CreatedAt: member.CreatedAt}
	for _, criteria := range criteriaSet {
		if criteriamatch.Matches(matchMember, criteria, facts) {
			return true, nil
		}
	}
	return false, nil
}

// member reads the live, onboarded, unbanned Member of user.
func (e SignInEligibility) member(ctx context.Context, user *auth.UserInfo) (mediaasset.DownloadMemberFacts, bool, error) {
	if user == nil || !user.Authenticated || user.Banned {
		return mediaasset.DownloadMemberFacts{}, false, nil
	}
	return e.facts.MemberFacts(ctx, user.MemberID.String(), user.IdentityID.String())
}

func (e SignInEligibility) matchFacts(ctx context.Context, user *auth.UserInfo, memberID string, criteriaSet []criteriamatch.Criteria) (criteriamatch.Facts, error) {
	facts := criteriamatch.Facts{
		ExistingTagIDs: map[string]struct{}{}, MemberTagIDs: map[string]struct{}{}, Roles: map[policyv1.RoleID]bool{},
	}
	if tagIDs := criteriamatch.ReferencedTagIDs(criteriaSet); len(tagIDs) != 0 {
		rows, err := e.facts.TagFacts(ctx, memberID, tagIDs)
		if err != nil {
			return facts, err
		}
		for _, row := range rows {
			facts.ExistingTagIDs[row.TagID] = struct{}{}
			if row.MappedMemberID != nil {
				facts.MemberTagIDs[row.TagID] = struct{}{}
			}
		}
	}
	roles, err := criteriamatch.RequestedRoles(criteriaSet)
	if err != nil {
		return facts, err
	}
	for _, role := range roles {
		can, err := criteriamatch.PlatformCan(role)
		if err != nil {
			return facts, err
		}
		decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, user), can)
		if err != nil {
			return facts, domainerr.AuthenticationRequired().WithCause(err)
		}
		allowed, err := e.spiceDB.Can(ctx, decision)
		if err != nil {
			return facts, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
		}
		facts.Roles[role] = allowed
	}
	return facts, nil
}
