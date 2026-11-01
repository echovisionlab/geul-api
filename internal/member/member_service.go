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
	"gorm.io/gorm"
)

const memberNicknameMaxLength = 100

// AuditAppender persists the Member domain audit and personal-data access
// records.
type AuditAppender interface {
	securityaccess.Appender
	domainaudit.Appender
}

// Deps are the collaborators of the Member use cases. Every field except the
// two URLs is required; NewService rejects a missing one.
type Deps struct {
	DB       *gorm.DB
	SpiceDB  *auth.SpiceDBClient
	Identity auth.IdentityGetter
	// AvatarFiles promotes an uploaded avatar source to a public asset.
	AvatarFiles   UserAvatarAssetPromoter
	WelcomeEmails EmailCommandPublisher
	Accounts      AccountSummaryReader
	AccountEmails AccountEmailProjection
	Audit         AuditAppender
	// CDNDomain prefixes avatar URLs; empty keeps them relative.
	CDNDomain string
	// SiteOrigin is the origin of the welcome email login link.
	SiteOrigin string
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Identity": d.Identity == nil,
		"AvatarFiles": d.AvatarFiles == nil, "WelcomeEmails": d.WelcomeEmails == nil,
		"Accounts": d.Accounts == nil, "AccountEmails": d.AccountEmails == nil, "Audit": d.Audit == nil,
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
	db                 *gorm.DB
	spicedb            *auth.SpiceDBClient
	identity           auth.IdentityGetter
	avatarFiles        UserAvatarAssetPromoter
	welcomeEmails      EmailCommandPublisher
	accounts           AccountSummaryReader
	accountEmails      AccountEmailProjection
	mutations          *mutation.Runner
	personalDataAccess *securityaccess.Recorder
	cdnDomain          string
	siteOrigin         string
	onboardingMetrics  memberOnboardingMetrics
}

// NewService validates deps and returns the Member use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db:                 deps.DB,
		spicedb:            deps.SpiceDB,
		identity:           deps.Identity,
		avatarFiles:        deps.AvatarFiles,
		welcomeEmails:      deps.WelcomeEmails,
		accounts:           deps.Accounts,
		accountEmails:      deps.AccountEmails,
		mutations:          mutations,
		personalDataAccess: securityaccess.New(deps.Audit),
		cdnDomain:          deps.CDNDomain,
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
