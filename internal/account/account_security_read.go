package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
)

// Security is a principal's security settings.
type Security struct {
	Providers          []Provider
	EmailCodeAvailable bool
	CanonicalEmail     string
	PasskeyCount       int32
	EmailCandidates    []AccountEmailProjection
	Sessions           []SecuritySession
}

// SecuritySession is one live session of the principal.
type SecuritySession struct {
	ID              string
	Current         bool
	Active          bool
	AuthenticatedAt time.Time
	ExpiresAt       time.Time
}

func (s *Service) deliveryPrimaryEmailForIdentity(ctx context.Context, memberID, identityID string) (string, error) {
	var email string
	err := s.read(ctx, func(store Store) error {
		var err error
		email, err = store.MemberPrimaryEmail(ctx, memberID, identityID)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrMemberLinkNotFound) {
			return "", domainerr.ResourceNotFound("member", memberID)
		}
		return "", err
	}
	return strings.TrimSpace(email), nil
}

func (s *Service) securityForIdentity(
	ctx context.Context,
	memberID,
	identityID,
	currentSessionID string,
) (*Security, error) {
	if _, err := uuidutil.ParseCanonical(currentSessionID, "session_id"); err != nil {
		return nil, domainerr.InvalidSession()
	}
	identity, err := s.identity.GetIdentityWithIncludeCredential(ctx, identityID, "oidc")
	if err != nil || identity == nil {
		if err == nil {
			err = errors.New("Kratos identity response is empty")
		}
		return nil, fmt.Errorf("read account identity: %w", err)
	}
	if identity.ID != identityID || identity.ExternalID != memberID || identity.State != auth.KratosStateActive {
		return nil, domainerr.InvalidSession()
	}
	passkeyIdentity, err := s.identity.GetIdentityWithIncludeCredential(ctx, identityID, "passkey")
	if err != nil || passkeyIdentity == nil {
		if err == nil {
			err = errors.New("Kratos identity response is empty")
		}
		return nil, fmt.Errorf("read account passkeys: %w", err)
	}
	deliveryPrimaryEmail, err := s.deliveryPrimaryEmailForIdentity(ctx, memberID, identityID)
	if err != nil {
		return nil, err
	}
	canonicalEmail := emailutil.NormalizeAddressForDelivery(identity.CurrentEmail())
	if canonicalEmail == "" || emailutil.NormalizeAddressForDelivery(deliveryPrimaryEmail) != canonicalEmail {
		return nil, errors.New("member primary email is not synchronized with the canonical account email")
	}
	codeIdentity, err := s.identity.GetIdentityWithIncludeCredential(ctx, identityID, "code")
	if err != nil || codeIdentity == nil {
		if err == nil {
			err = errors.New("Kratos identity response is empty")
		}
		return nil, fmt.Errorf("read account email-code credential: %w", err)
	}
	if identity.Credentials == nil {
		identity.Credentials = map[string]auth.Credential{}
	}
	if codeCredential, ok := codeIdentity.Credentials["code"]; ok {
		identity.Credentials["code"] = codeCredential
	}
	providerCandidates := ResolveAccountEmailProviderCandidates(ctx, identity.Credentials)
	rows := projectedAccountEmailRows(identity, providerCandidates)
	security := &Security{
		Providers:          Providers(identity),
		EmailCodeAvailable: auth.CodeCredentialHasAddress(inventoryCredential(identity.Credentials, "code"), canonicalEmail),
		CanonicalEmail:     canonicalEmail,
		EmailCandidates:    rows,
	}
	if passkeyCredential, ok := passkeyIdentity.Credentials["passkey"]; ok {
		security.PasskeyCount = int32(auth.UsablePasskeyCredentialCount(passkeyCredential))
	}
	var sessions []Session
	if err := s.read(ctx, func(store Store) error {
		var err error
		sessions, err = store.LiveSessions(ctx, identityID)
		return err
	}); err != nil {
		return nil, err
	}
	currentFound := false
	for _, row := range sessions {
		current := row.ID == currentSessionID
		currentFound = currentFound || current
		security.Sessions = append(security.Sessions, SecuritySession{
			ID: row.ID, Current: current, Active: row.Active, AuthenticatedAt: row.AuthenticatedAt, ExpiresAt: row.ExpiresAt,
		})
	}
	if !currentFound {
		return nil, domainerr.InvalidSession()
	}
	return security, nil
}

func (s *Service) requireFreshSecuritySession(ctx context.Context, identityID, sessionID string) error {
	// Kratos exposes authenticated_at for the active session. The interceptor
	// carries that verified timestamp into UserInfo. PostgreSQL is not an
	// authentication freshness authority.
	user := auth.GetUser(ctx)
	if user == nil || user.IdentityID.String() != identityID || user.SessionID.String() != sessionID ||
		!auth.IsFreshForSecurityMutation(user, time.Now().UTC()) {
		return domainerr.Precondition("reauthenticate before changing account security settings")
	}
	return nil
}

// GetMySecurity reads the principal's security settings: providers, email
// candidates, passkeys and active sessions.
func (s *Service) GetMySecurity(ctx context.Context) (*Security, error) {
	p, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	return s.securityForIdentity(ctx, p.MemberID.String(), p.IdentityID.String(), p.SessionID.String())
}

// setIdentityCanonicalEmail is the canonical account-email mutation used by the
// explicit administrative account operation. Member primary_email follows it
// as a deletion-safe projection.
func (s *Service) setIdentityCanonicalEmail(ctx context.Context, identityID, email string) (string, string, bool, error) {
	normalized, ok := normalizeAccountEmailInput(email)
	if !ok {
		return "", "", false, domainerr.InvalidField("email", "enter a valid email address")
	}
	before, err := s.identity.GetIdentity(ctx, identityID)
	if err != nil {
		return "", "", false, accountCanonicalEmailMutationError(err, identityID)
	}
	if before == nil {
		return "", "", false, domainerr.ResourceNotFound("identity", identityID)
	}
	previous := emailutil.NormalizeAddressForDelivery(before.CurrentEmail())
	err = NewAccountEmailChangeLifecycle(s.units, s.identity, s.emails).
		ApplyProvenCandidate(ctx, identityID, normalized)
	if err := accountCanonicalEmailMutationError(err, identityID); err != nil {
		return "", "", false, err
	}
	after, err := s.identity.GetIdentity(ctx, identityID)
	if err != nil {
		return "", "", false, fmt.Errorf("confirm canonical account email: %w", err)
	}
	if after == nil {
		return "", "", false, errors.New("confirm canonical account email: identity is missing")
	}
	current := emailutil.NormalizeAddressForDelivery(after.CurrentEmail())
	if current != normalized {
		return "", "", false, errors.New("canonical account email did not converge")
	}
	return previous, current, previous != current, nil
}

func accountCanonicalEmailMutationError(err error, identityID string) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrAccountEmailCandidateNotProven):
		return domainerr.Precondition("email must be proven by Kratos or a connected trusted provider before it can become the canonical email").WithCause(err)
	case errors.Is(err, ErrAccountEmailChangeInFlight):
		return domainerr.Precondition(err.Error())
	case errors.Is(err, ErrAccountEmailChangeConflict):
		return domainerr.Precondition("email is already used by another account").WithCause(err)
	// Each "record not found" the lifecycle answers: the Identity, its Member
	// link, or a request a concurrent change removed.
	case errors.Is(err, ErrIdentityNotFound), errors.Is(err, ErrMemberLinkNotFound), errors.Is(err, ErrEmailChangeRequestNotFound):
		return domainerr.ResourceNotFound("identity", identityID).WithCause(err)
	default:
		return err
	}
}
