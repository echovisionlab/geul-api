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
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Identities is the Kratos admin API the Account use cases call.
type Identities interface {
	auth.IdentityGetter
	auth.IdentityCredentialFinder
	auth.IdentityAccountEmailStateWriter
	auth.IdentityCredentialIdentifierDeleter
	auth.IdentityMetadataAdminWriter
	auth.IdentityStateWriter
	auth.IdentitySessionRevoker
	auth.SessionRevoker
}

// Authorizer is the SpiceDB port of the Account use cases: the pipeline's
// checks and relationship writes, the global role reads and lookups, and
// the removal of a deleted Identity's relationships.
type Authorizer interface {
	mutation.Authorizer
	CheckActorCan(ctx context.Context, actor policyv1.Actor, can policyv1.Can) (bool, error)
	ReadDirectGlobalRole(ctx context.Context, subject auth.AccountIdentitySubject) (policyv1.RoleID, bool, error)
	LookupGlobalSubjects(ctx context.Context, lookup policyv1.SubjectLookup) ([]auth.AccountIdentitySubject, error)
	DeleteAllAccountIdentityRelationships(ctx context.Context, subject auth.AccountIdentitySubject) (auth.ZedToken, error)
}

// MemberAuthorizations is the Member side of an account deletion that runs
// outside any Account transaction (member.DeletionLifecycle): revoking the
// Member resource relationships of a hard-delete target, and cleaning up a
// deleted Member's avatar, whose media steps commit on their own.
type MemberAuthorizations interface {
	RevokeUnonboardedAuthorization(ctx context.Context, target MemberUnonboardedDeletionTarget) error
	CleanupAvatar(ctx context.Context, memberID, avatarAssetID string) error
}

// Deps are the collaborators of the Account use cases. Every field except
// SiteOrigin is required; NewService rejects a missing one.
type Deps struct {
	Units   Units
	SpiceDB Authorizer
	Fence   identitystate.Fence
	// Identity is the Kratos admin API.
	Identity Identities
	// Emails publishes the lifecycle and security email commands.
	Emails  EmailPublisher
	Members MemberAuthorizations
	Audit   domainaudit.Appender
	// SiteOrigin prefixes the links of lifecycle emails; empty keeps them
	// relative.
	SiteOrigin string
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Identity": d.Identity == nil,
		"Emails": d.Emails == nil, "Members": d.Members == nil, "Audit": d.Audit == nil,
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
// principal's own security settings, the token-authenticated deletion and
// recovery lifecycle, and the principal-less flows the Kratos hooks, the
// reconcilers and the worker run. Connect adapters translate their requests
// into its commands and queries.
type Service struct {
	units     Units
	spicedb   Authorizer
	identity  Identities
	emails    EmailPublisher
	members   MemberAuthorizations
	audit     domainaudit.Appender
	mutations *mutation.StoreRunner[Store]
	baseURL   string
	interval  time.Duration
	// emailChangeInterval paces the email change reconciler.
	emailChangeInterval time.Duration
	now                 func() time.Time
}

// NewService validates deps and returns the Account use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner[Store](deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		units: deps.Units, spicedb: deps.SpiceDB, identity: deps.Identity, emails: deps.Emails,
		members: deps.Members, audit: deps.Audit, mutations: mutations,
		baseURL: strings.TrimRight(deps.SiteOrigin, "/"), interval: accountLifecycleReconcileInterval,
		emailChangeInterval: accountEmailChangeReconcileInterval, now: time.Now,
	}, nil
}

// freshAdmin is the in-transaction authority of every administrator mutation.
func freshAdmin() mutation.Access {
	return mutation.FreshAdminCan(policyv1.Platform.IsAdmin())
}

// read runs read in one read-only snapshot.
func (s *Service) read(ctx context.Context, read func(Store) error) error {
	return s.units.Read(ctx, read)
}

// transact runs work in one read-committed transaction of its own, for an
// explicit step outside the mutation pipeline.
func (s *Service) transact(ctx context.Context, work func(Store) error) error {
	return s.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error { return work(tx.Store()) })
}

// appendAccountUpdatedAudit records, in a transaction of its own, a change
// that an external authority (Kratos) already committed under its own
// Identity fence.
func (s *Service) appendAccountUpdatedAudit(ctx context.Context, build domainaudit.Builder) error {
	return s.transact(ctx, func(store Store) error {
		return store.AppendRequestAudit(ctx, s.audit, sharedtelemetry.AuditAccountUpdated, build)
	})
}
