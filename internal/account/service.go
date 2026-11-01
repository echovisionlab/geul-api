package account

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

type accountIdentityManager interface {
	auth.IdentityManager
	auth.IdentityCredentialFinder
	auth.IdentityAccountEmailStateWriter
	auth.IdentityCredentialIdentifierDeleter
	auth.IdentityExternalIDWriter
	auth.SessionRevoker
}

// LifecyclePublisher publishes the lifecycle email commands and the durable
// deletion commands of the Account use cases.
type LifecyclePublisher interface {
	EmailCommandPublisher
	UserDeletionDispatchPublisher
	UserDeletionIdentityDispatchPublisher
}

// NewsletterSubscription is the Member-owned newsletter fact an Account
// request changes.
type NewsletterSubscription interface {
	Mutate(context.Context, *gorm.DB, string, bool) (bool, error)
	State(context.Context, *gorm.DB, string) (*managev1.NewsletterSubscriptionState, error)
	AuditStates(bool) (sharedtelemetry.AuditState, sharedtelemetry.AuditState)
}

// Deps are the collaborators of the Account use cases. Every field except
// SiteOrigin is required; NewService rejects a missing one.
type Deps struct {
	DB      *gorm.DB
	SpiceDB *auth.SpiceDBClient
	// Identity is the Kratos admin API.
	Identity       accountIdentityManager
	Publisher      LifecyclePublisher
	Newsletter     NewsletterSubscription
	MemberDeletion MemberDeletionLifecycle
	MemberEmails   MemberEmailProjection
	Audit          domainaudit.Appender
	// SiteOrigin prefixes the links of lifecycle emails; empty keeps them
	// relative.
	SiteOrigin string
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Identity": d.Identity == nil, "Publisher": d.Publisher == nil,
		"Newsletter": d.Newsletter == nil, "MemberDeletion": d.MemberDeletion == nil, "MemberEmails": d.MemberEmails == nil,
		"Audit": d.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("account dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Account use cases: administrator account management, a
// principal's own security settings, and the token-authenticated deletion
// and recovery lifecycle. Connect adapters translate their requests into its
// commands and queries.
type Service struct {
	db             *gorm.DB
	spicedb        *auth.SpiceDBClient
	identity       accountIdentityManager
	publisher      LifecyclePublisher
	newsletter     NewsletterSubscription
	memberDeletion MemberDeletionLifecycle
	memberEmails   MemberEmailProjection
	audit          domainaudit.Appender
	credentials    *AccountCredentialMutationService
	mutations      *mutation.Runner
	baseURL        string
	interval       time.Duration
}

// NewService validates deps and returns the Account use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, spicedb: deps.SpiceDB, identity: deps.Identity, publisher: deps.Publisher,
		newsletter: deps.Newsletter, memberDeletion: deps.MemberDeletion, memberEmails: deps.MemberEmails,
		audit: deps.Audit, credentials: NewAccountCredentialMutationService(deps.DB, deps.Identity, deps.MemberEmails),
		mutations: mutations, baseURL: strings.TrimRight(deps.SiteOrigin, "/"), interval: accountLifecycleReconcileInterval,
	}, nil
}

// freshAdmin is the in-transaction authority of every administrator mutation.
func freshAdmin() mutation.Access {
	return mutation.FreshAdminCan(policyv1.Platform.IsAdmin())
}

// appendAccountUpdatedAudit records a change that an external authority
// (Kratos) already committed under its own Identity fence.
func (s *Service) appendAccountUpdatedAudit(ctx context.Context, build domainaudit.Builder) error {
	return domainaudit.AppendRequestTransaction(ctx, s.db, s.audit, sharedtelemetry.AuditAccountUpdated, build)
}
