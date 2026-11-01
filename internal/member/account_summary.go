package member

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Accounts is the Member-owned port for bounded Account projections. Each
// read runs outside the Member store's snapshot.
type Accounts interface {
	// SessionSummary is the lean summary the session view shows.
	SessionSummary(ctx context.Context, memberID string) (*AccountSummary, error)
	Summary(ctx context.Context, memberID string) (*AccountSummary, error)
	Summaries(ctx context.Context, memberIDs []string) (map[string]*AccountSummary, error)
	// AdminDetails projects the providers and email candidates of an
	// Identity read with its email credentials.
	AdminDetails(ctx context.Context, identity *auth.Identity) (*AccountDetails, error)
}

// AccountSummary is the Account state an administrator or session view of a
// Member shows.
type AccountSummary struct {
	MemberID       string
	Role           policyv1.AuthorizationRole
	Status         AdminStatus
	CanonicalEmail *CanonicalEmail
	Banned         bool
	BanDetails     *BanDetails
}

// CanonicalEmail is the canonical account email.
type CanonicalEmail struct {
	Email    string
	Verified bool
}

// BanDetails is why and until when an account is banned.
type BanDetails struct {
	MetadataBanned bool
	IdentityState  string
	InactiveState  bool
	Reason         *string
	ExpiresAt      *time.Time
}

// AccountDetails are the sign-in providers and email candidates of an
// account, as an administrator sees them.
type AccountDetails struct {
	Providers       []AccountProvider
	EmailCandidates []EmailCandidate
}

// AccountProvider is one linked social sign-in.
type AccountProvider struct {
	Provider   string
	Identifier string
}

// EmailCandidate is one address an account can prove.
type EmailCandidate struct {
	Email             string
	NormalizedEmail   string
	Current           bool
	IdentityVerified  bool
	EffectiveTrusted  bool
	UsableForDelivery bool
	Sources           []EmailSource
}

// EmailSourceType is what proves an email candidate.
type EmailSourceType string

// The email source types, as Account stores them.
const (
	EmailSourceIdentityCurrent EmailSourceType = "identity_current"
	EmailSourceEmailCode       EmailSourceType = "email_code"
	EmailSourceOIDCProvider    EmailSourceType = "oidc_provider"
)

// EmailSource is one proof of an email candidate.
type EmailSource struct {
	Type            EmailSourceType
	Provider        *string
	ProviderSubject *string
}
