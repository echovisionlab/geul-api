package member

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

const memberNicknameMaxLength = 100

// AuditAppender persists the Member domain audit and personal-data access
// records.
type AuditAppender interface {
	securityaccess.Appender
	domainaudit.Appender
}

// Authorizer is the SpiceDB port of the Member use cases: the checks and
// relationship writes of the pipeline, the global role and resource lookups,
// and the direct role read of registration.
type Authorizer interface {
	mutation.Authorizer
	LookupGlobalSubjects(ctx context.Context, lookup policyv1.SubjectLookup) ([]auth.AccountIdentitySubject, error)
	LookupResources(ctx context.Context, lookup policyv1.ResourceLookup, actor policyv1.Actor) ([]string, error)
	ReadDirectGlobalRole(ctx context.Context, subject auth.AccountIdentitySubject) (policyv1.RoleID, bool, error)
}

// UserAvatarAssetPromoter is the exact File capability required for Member
// avatar projection; File storage remains outside this domain.
type UserAvatarAssetPromoter interface {
	PromoteUserAvatarAsset(ctx context.Context, fileID string) (*Asset, error)
}

// Deps are the collaborators of the Member use cases. Every field except the
// two URLs is required; NewService rejects a missing one.
type Deps struct {
	Units   mutation.UnitOfWork[Store]
	SpiceDB Authorizer
	Fence   identitystate.Fence
	// Identity is the Kratos admin API.
	Identity auth.IdentityGetter
	// AvatarFiles promotes an uploaded avatar source to a public asset.
	AvatarFiles UserAvatarAssetPromoter
	Accounts    Accounts
	Audit       AuditAppender
	// CDNDomain prefixes avatar URLs; empty keeps them relative.
	CDNDomain string
	// SiteOrigin is the origin of the welcome email login link.
	SiteOrigin string
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Identity": d.Identity == nil,
		"AvatarFiles": d.AvatarFiles == nil, "Accounts": d.Accounts == nil, "Audit": d.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("member dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Member use cases. Connect and MCP adapters translate
// their requests into its commands and queries.
type Service struct {
	units              mutation.UnitOfWork[Store]
	spicedb            Authorizer
	identity           auth.IdentityGetter
	avatarFiles        UserAvatarAssetPromoter
	accounts           Accounts
	mutations          *mutation.StoreRunner[Store]
	personalDataAccess *securityaccess.Recorder
	siteOrigin         string
	onboardingMetrics  memberOnboardingMetrics
}

// NewService validates deps and returns the Member use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		units:              deps.Units,
		spicedb:            deps.SpiceDB,
		identity:           deps.Identity,
		avatarFiles:        deps.AvatarFiles,
		accounts:           deps.Accounts,
		mutations:          mutations,
		personalDataAccess: securityaccess.New(deps.Audit),
		siteOrigin:         strings.TrimRight(strings.TrimSpace(deps.SiteOrigin), "/"),
		onboardingMetrics:  newMemberOnboardingMetrics(),
	}, nil
}

type memberOnboardingMetrics struct {
	welcomePublishCounter otelmetric.Int64Counter
}

func newMemberOnboardingMetrics() memberOnboardingMetrics {
	counter, err := otel.Meter(sharedtelemetry.ServiceBackend.Instrumentation("member")).Int64Counter(
		"member_onboarding_welcome_publish_total",
		otelmetric.WithDescription("Counts welcome command outcomes after a committed Member onboarding transition."),
	)
	if err != nil {
		slog.Warn("Failed to create Member onboarding welcome counter", "failure_cause", failurecause.Message(err))
		return memberOnboardingMetrics{}
	}
	return memberOnboardingMetrics{welcomePublishCounter: counter}
}

func (m memberOnboardingMetrics) recordWelcomePublish(ctx context.Context, outcome string) {
	if m.welcomePublishCounter == nil {
		return
	}
	m.welcomePublishCounter.Add(ctx, 1, otelmetric.WithAttributes(attribute.String("outcome", outcome)))
}

// requireAdminMember is the early administrator gate of admin use cases. It
// answers a non-administrator before the use case reveals whether a target
// exists; a mutation still checks fresh authority inside its transaction.
func (s *Service) requireAdminMember(ctx context.Context) (*auth.UserInfo, error) {
	return authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb)
}

// freshAdmin is the in-transaction authority of every administrator mutation.
func freshAdmin() mutation.Access {
	return mutation.FreshAdminCan(policyv1.Platform.IsAdmin())
}

func (s *Service) isGlobalAuthor(ctx context.Context) (bool, error) {
	_, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return false, err
	}
	can, err := policyv1.Platform.IsAuthor()
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, domainerr.InvalidSession().WithCause(err)
	}
	allowed, err := s.spicedb.Can(ctx, decision)
	if err != nil {
		return false, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	return allowed, nil
}

// read runs read in one read-only snapshot of the Member store.
func (s *Service) read(ctx context.Context, read func(Store) error) error {
	return s.units.Read(ctx, read)
}
