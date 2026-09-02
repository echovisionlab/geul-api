package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

func (s *AccountService) deliveryPrimaryEmailForIdentity(ctx context.Context, memberID, identityID string) (string, error) {
	if s.memberEmails == nil {
		return "", errs.Internal(fmt.Errorf("member email projection is required"))
	}
	email, err := s.memberEmails.PrimaryEmail(ctx, s.db, memberID, identityID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errs.NotFound("member", memberID)
		}
		return "", errs.Internal(err)
	}
	return strings.TrimSpace(email), nil
}

func (s *AccountService) securityForIdentity(
	ctx context.Context,
	memberID,
	identityID,
	currentSessionID string,
) (*managev1.AccountSecurity, error) {
	if _, err := uuidutil.ParseCanonical(currentSessionID, "session_id"); err != nil {
		return nil, errs.InvalidSession()
	}
	identity, err := s.identity.GetIdentityWithIncludeCredential(ctx, identityID, "oidc")
	if err != nil || identity == nil {
		return nil, errs.Internal(fmt.Errorf("read account identity: %w", err))
	}
	if identity.ID != identityID || identity.ExternalID != memberID || identity.State != auth.KratosStateActive {
		return nil, errs.InvalidSession()
	}
	passkeyIdentity, err := s.identity.GetIdentityWithIncludeCredential(ctx, identityID, "passkey")
	if err != nil || passkeyIdentity == nil {
		return nil, errs.Internal(fmt.Errorf("read account passkeys: %w", err))
	}
	deliveryPrimaryEmail, err := s.deliveryPrimaryEmailForIdentity(ctx, memberID, identityID)
	if err != nil {
		return nil, err
	}
	canonicalEmail := emailutil.NormalizeAddressForDelivery(identity.CurrentEmail())
	if canonicalEmail == "" || emailutil.NormalizeAddressForDelivery(deliveryPrimaryEmail) != canonicalEmail {
		return nil, errs.Internal(fmt.Errorf("member primary email is not synchronized with the canonical account email"))
	}
	codeIdentity, err := s.identity.GetIdentityWithIncludeCredential(ctx, identityID, "code")
	if err != nil || codeIdentity == nil {
		return nil, errs.Internal(fmt.Errorf("read account email-code credential: %w", err))
	}
	if identity.Credentials == nil {
		identity.Credentials = map[string]auth.Credential{}
	}
	if codeCredential, ok := codeIdentity.Credentials["code"]; ok {
		identity.Credentials["code"] = codeCredential
	}
	providerCandidates := ResolveAccountEmailProviderCandidates(ctx, identity.Credentials)
	rows := projectedAccountEmailRows(identity, providerCandidates)
	security := &managev1.AccountSecurity{
		Providers:          providerProto(identity),
		EmailCodeAvailable: auth.CodeCredentialHasAddress(inventoryCredential(identity.Credentials, "code"), canonicalEmail),
		CanonicalEmail:     canonicalEmail,
	}
	if passkeyCredential, ok := passkeyIdentity.Credentials["passkey"]; ok {
		security.PasskeyCount = int32(auth.UsablePasskeyCredentialCount(passkeyCredential))
	}
	for _, row := range rows {
		candidate := &managev1.AccountEmailCandidate{Email: row.DisplayEmail, NormalizedEmail: row.NormalizedEmail, Current: row.IsCurrentEmail, IdentityVerified: row.IdentityVerified, EffectiveTrusted: row.EffectiveTrusted, UsableForDelivery: row.UsableForDelivery}
		for _, source := range row.Sources {
			candidate.Sources = append(candidate.Sources, &managev1.AccountEmailSource{SourceType: sourceTypeProto(string(source.SourceType)), Provider: source.Provider, ProviderSubject: source.ProviderSubject})
		}
		security.EmailCandidates = append(security.EmailCandidates, candidate)
	}
	type sessionRow struct {
		ID              string
		Active          bool
		AuthenticatedAt time.Time
		ExpiresAt       time.Time
	}
	var sessions []sessionRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT CAST(id AS text) AS id, active, authenticated_at, expires_at
		FROM kratos.sessions
		WHERE identity_id = ?
		  AND active = TRUE
		  AND expires_at > CURRENT_TIMESTAMP
		ORDER BY authenticated_at DESC, id DESC
	`, identityID).Scan(&sessions).Error; err != nil {
		return nil, errs.Internal(err)
	}
	currentFound := false
	for _, row := range sessions {
		current := row.ID == currentSessionID
		currentFound = currentFound || current
		security.Sessions = append(security.Sessions, &managev1.AccountSession{
			Id:              row.ID,
			Current:         current,
			Active:          row.Active,
			AuthenticatedAt: timestamppb.New(row.AuthenticatedAt),
			ExpiresAt:       timestamppb.New(row.ExpiresAt),
		})
	}
	if !currentFound {
		return nil, errs.InvalidSession()
	}
	return security, nil
}

func (s *AccountService) requireFreshSecuritySession(ctx context.Context, identityID, sessionID string) error {
	// Kratos exposes authenticated_at for the active session. The interceptor
	// carries that verified timestamp into UserInfo. PostgreSQL is not an
	// authentication freshness authority.
	user := auth.GetUser(ctx)
	if user == nil || user.IdentityID.String() != identityID || user.SessionID.String() != sessionID ||
		!auth.IsFreshForSecurityMutation(user, time.Now().UTC()) {
		return errs.FailedPrecondition("reauthenticate before changing account security settings")
	}
	return nil
}

func (s *AccountService) GetMySecurity(ctx context.Context, _ *connect.Request[managev1.GetMySecurityRequest]) (*connect.Response[managev1.GetMySecurityResponse], error) {
	p, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	security, err := s.securityForIdentity(ctx, p.MemberID.String(), p.IdentityID.String(), p.SessionID.String())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.GetMySecurityResponse{Security: security}), nil
}

// setIdentityCanonicalEmail is the canonical account-email mutation used by the
// explicit administrative account operation. Member primary_email follows it
// as a deletion-safe projection.
func (s *AccountService) setIdentityCanonicalEmail(ctx context.Context, identityID, email string) (string, string, bool, error) {
	normalized, ok := normalizeAccountEmailInput(email)
	if !ok {
		return "", "", false, errs.InvalidArgument("email", "enter a valid email address")
	}
	before, err := s.identity.GetIdentity(ctx, identityID)
	if err != nil {
		return "", "", false, accountCanonicalEmailMutationError(err, identityID)
	}
	if before == nil {
		return "", "", false, errs.NotFound("identity", identityID)
	}
	previous := emailutil.NormalizeAddressForDelivery(before.CurrentEmail())
	err = NewAccountEmailChangeLifecycle(s.db, s.identity, s.publisher, s.memberEmails).
		ApplyProvenCandidate(ctx, identityID, normalized)
	if err := accountCanonicalEmailMutationError(err, identityID); err != nil {
		return "", "", false, err
	}
	after, err := s.identity.GetIdentity(ctx, identityID)
	if err != nil {
		return "", "", false, errs.Internal(fmt.Errorf("confirm canonical account email: %w", err))
	}
	if after == nil {
		return "", "", false, errs.Internal(fmt.Errorf("confirm canonical account email: identity is missing"))
	}
	current := emailutil.NormalizeAddressForDelivery(after.CurrentEmail())
	if current != normalized {
		return "", "", false, errs.Internal(fmt.Errorf("canonical account email did not converge"))
	}
	return previous, current, previous != current, nil
}

func accountCanonicalEmailMutationError(err error, identityID string) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrAccountEmailCandidateNotProven):
		return errs.FailedPrecondition("email must be proven by Kratos or a connected trusted provider before it can become the canonical email")
	case errors.Is(err, ErrAccountEmailChangeInFlight):
		return errs.FailedPrecondition(err.Error())
	case errors.Is(err, ErrAccountEmailChangeConflict):
		return errs.FailedPrecondition("email is already used by another account")
	case errors.Is(err, gorm.ErrRecordNotFound):
		return errs.NotFound("identity", identityID)
	default:
		return errs.Internal(err)
	}
}
