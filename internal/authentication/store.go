package authentication

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Store is the authentication state one transaction or read snapshot reads
// and writes: the first-admin bootstrap claim and the auth code issuance
// ledger. authenticationpostgres implements it over PostgreSQL;
// authenticationtest keeps it in memory.
type Store interface {
	// ClaimFirstAdmin takes the first-admin bootstrap lock for the rest of
	// the transaction, claims the bootstrap for identityID when the
	// greenfield allows it (memberID is the onboarded, unbanned Member
	// linked to identityID, and no other account Identity exists), and
	// reports who holds the claim. The claim is one conditional insert made
	// under that lock, so two first logins cannot both claim.
	ClaimFirstAdmin(ctx context.Context, identityID, memberID string) (FirstAdminClaim, error)

	// LockIssuances serializes the issuance budgets until the transaction
	// ends.
	LockIssuances(ctx context.Context) error
	// IssuanceUsage counts the issuances a budget sees.
	IssuanceUsage(ctx context.Context, query IssuanceUsageQuery) (IssuanceUsage, error)
	// InsertIssuance records an accepted issuance.
	InsertIssuance(ctx context.Context, issuance Issuance) error
	// DeleteIssuance removes the issuance token, if it is still recorded.
	DeleteIssuance(ctx context.Context, token string) error
	// LatestIssuance returns the latest issuance to recipientDigest that
	// has not expired at now, and whether there is one.
	LatestIssuance(ctx context.Context, recipientDigest string, now time.Time) (IssuedCode, bool, error)
}

// FirstAdminClaim is the first-admin bootstrap as one login sees it.
type FirstAdminClaim struct {
	// Owner reports whether the login's Identity holds the claim.
	Owner bool
	// ClaimedNow reports whether this login made the claim.
	ClaimedNow bool
}

// Issuance is one accepted auth code issuance. The ledger keeps keyed
// digests only, never the recipient, flow or client address.
type Issuance struct {
	Token           string
	Purpose         email.EventKey
	RecipientDigest string
	SubjectDigest   string
	ClientIPDigest  string
	IssuedAt        time.Time
	ExpiresAt       time.Time
}

// IssuedCode is what the courier needs of the latest issuance to one
// recipient.
type IssuedCode struct {
	Token           string
	Purpose         email.EventKey
	RecipientDigest string
	IssuedAt        time.Time
}

// IssuanceScope names the issuances one budget counts.
type IssuanceScope int

const (
	// EveryIssuance counts all issuances.
	EveryIssuance IssuanceScope = iota
	// ClientIPIssuances counts the issuances of one client IP digest.
	ClientIPIssuances
	// SubjectIssuances counts the issuances of one subject digest.
	SubjectIssuances
)

// IssuanceUsageQuery selects the issuances of Scope (with Digest unless
// Scope is EveryIssuance) issued after After.
type IssuanceUsageQuery struct {
	Scope  IssuanceScope
	Digest string
	After  time.Time
}

// IssuanceUsage is how many issuances a budget counts and when the oldest
// of them was issued; First is zero when Count is.
type IssuanceUsage struct {
	Count int64
	First time.Time
}

// Authorizer is the SpiceDB port of the authentication use cases: the
// pipeline's checks and relationship writes, and the direct global role a
// login converges.
type Authorizer interface {
	mutation.Authorizer
	ReadDirectGlobalRole(ctx context.Context, subject auth.AccountIdentitySubject) (policyv1.RoleID, bool, error)
}

// Sessions reads Kratos sessions for the gateway and the access recorder.
// Each read is one statement outside any transaction: Oathkeeper asks the
// gateway on every request, and neither reader needs a snapshot.
type Sessions interface {
	// Principal resolves the principal of a live session. A session that
	// cannot act answers auth.ErrSessionPrincipalInvalid.
	Principal(ctx context.Context, sessionID string) (*auth.UserInfo, error)
	// FirstSession reports whether sessionID is its Identity's first.
	FirstSession(ctx context.Context, sessionID string) (bool, error)
}
