package public

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/mutation"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Store is the public Member persistence of one transaction or read
// snapshot: onboarded Member profiles, their ready avatars, the post author
// counts, and the newsletter subscription a signed token opts out of.
type Store interface {
	member.NewsletterStore

	// PublicMember reads an onboarded Member, or reports
	// member.ErrMemberNotFound.
	PublicMember(ctx context.Context, memberID string) (member.Member, error)
	// PublicMembers reads the onboarded Members in memberIDs.
	PublicMembers(ctx context.Context, memberIDs []string) ([]member.Member, error)
	// PublicAvatars reads the ready, inline avatar of each Member that has
	// one.
	PublicAvatars(ctx context.Context, memberIDs []string) (map[string]*memberref.Asset, error)
	// TopAuthors reads the onboarded Members with the most published or
	// archived posts, most first.
	TopAuthors(ctx context.Context, limit int32) ([]AuthorRow, error)
	// SelectedAuthors reads the post counts of the onboarded live linked
	// Members in memberIDs.
	SelectedAuthors(ctx context.Context, memberIDs []string) ([]AuthorRow, error)
	// NewsletterMemberID reads the live Member linked to identityID, or
	// reports member.ErrMemberNotFound.
	NewsletterMemberID(ctx context.Context, identityID string) (string, error)
}

// AuthorRow is one Member's published or archived post count.
type AuthorRow struct {
	MemberID          string
	AccountIdentityID *string
	PostCount         int64
	Bio               *string
}

// Authorizer is the SpiceDB port of the public Member use cases: the
// extended-profile and author gates, and the pipeline's checks.
type Authorizer interface {
	mutation.Authorizer
	CheckActorCan(ctx context.Context, actor policyv1.Actor, can policyv1.Can) (bool, error)
	LookupGlobalSubjects(ctx context.Context, lookup policyv1.SubjectLookup) ([]auth.AccountIdentitySubject, error)
}

// Deps are the collaborators of the public Member use cases. Every field is
// required; NewService rejects a missing one.
type Deps struct {
	Units mutation.UnitOfWork[Store]
	// SpiceDB is the authority for the extended-profile and author gates.
	SpiceDB Authorizer
	Fence   identitystate.Fence
	Audit   domainaudit.Appender
	// TokenSecret verifies newsletter unsubscribe tokens.
	TokenSecret string
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
		"TokenSecret": strings.TrimSpace(d.TokenSecret) == "",
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public member dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the public Member use cases: profile and author reads, and
// the token-authenticated newsletter opt-out.
type Service struct {
	units       mutation.UnitOfWork[Store]
	spiceDB     Authorizer
	tokenSecret string
	mutations   *mutation.StoreRunner[Store]
}

// NewService validates deps and returns the public Member use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{units: deps.Units, spiceDB: deps.SpiceDB, tokenSecret: deps.TokenSecret, mutations: mutations}, nil
}
