package account

import (
	"context"
	"errors"
	"net/http"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// SetMyCanonicalEmailCommand makes a proven address the principal's
// canonical account email.
type SetMyCanonicalEmailCommand struct{ Email string }

// RequestEmailChangeCommand checks whether the principal may change to
// NewEmail.
type RequestEmailChangeCommand struct{ NewEmail string }

// RevokeMySessionCommand ends one of the principal's other sessions.
type RevokeMySessionCommand struct{ SessionID string }

// EmailChangeAvailability answers whether an email change can continue, and
// why not when it cannot.
type EmailChangeAvailability struct {
	Available bool
	Message   string
}

// freshSecurityPrincipal is the authenticated principal of a security
// settings mutation, which requires a recent authentication.
func (s *Service) freshSecurityPrincipal(ctx context.Context) (*auth.UserInfo, error) {
	p, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireFreshSecuritySession(ctx, p.IdentityID.String(), p.SessionID.String()); err != nil {
		return nil, err
	}
	return p, nil
}

// SetMyCanonicalEmail makes a proven address the principal's canonical
// account email and returns the security settings it leaves.
func (s *Service) SetMyCanonicalEmail(ctx context.Context, command SetMyCanonicalEmailCommand) (*Security, error) {
	p, err := s.freshSecurityPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.changeCanonicalEmail(ctx, p.MemberID.String(), p.IdentityID.String(), command.Email); err != nil {
		return nil, err
	}
	return s.securityForIdentity(ctx, p.MemberID.String(), p.IdentityID.String(), p.SessionID.String())
}

// changeCanonicalEmail moves the canonical email through the email change
// lifecycle, which holds the Identity fence, and records an actual change.
func (s *Service) changeCanonicalEmail(ctx context.Context, memberID, identityID, email string) error {
	previous, current, changed, err := s.setIdentityCanonicalEmail(ctx, identityID, email)
	if err != nil || !changed {
		return err
	}
	return s.appendAccountUpdatedAudit(ctx, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewAccountCanonicalEmailUpdatedAuditRecord(metadata, memberID, previous, current)
	})
}

// accountIdentityLookupError keeps the diagnostic cause available to callers
// without exposing an identity provider response body in the RPC message.
type accountIdentityLookupError struct{ cause error }

const msgAccountIdentityLookupFailed = "identity lookup failed"

func (accountIdentityLookupError) Error() string   { return msgAccountIdentityLookupFailed }
func (e accountIdentityLookupError) Unwrap() error { return e.cause }

// OperationContext renders the cause in the failure record, which redacts the
// provider body the cause carries.
func (e accountIdentityLookupError) OperationContext() (rendered, operation string) {
	return msgAccountIdentityLookupFailed, msgAccountIdentityLookupFailed + ": " + e.cause.Error()
}

// RequestEmailChange checks that the principal may change to an address no
// other account uses; the change itself continues in Kratos settings.
func (s *Service) RequestEmailChange(ctx context.Context, command RequestEmailChangeCommand) (EmailChangeAvailability, error) {
	p, err := s.freshSecurityPrincipal(ctx)
	if err != nil {
		return EmailChangeAvailability{}, err
	}
	identity, err := s.identity.GetIdentity(ctx, p.IdentityID.String())
	if err != nil {
		var upstreamError *auth.KratosHTTPError
		if errors.As(err, &upstreamError) && upstreamError.StatusCode == http.StatusNotFound {
			return EmailChangeAvailability{}, domainerr.ResourceNotFound("identity", p.IdentityID.String()).WithCause(err)
		}
		return EmailChangeAvailability{}, accountIdentityLookupError{cause: err}
	}
	if identity == nil {
		return EmailChangeAvailability{}, domainerr.ResourceNotFound("identity", p.IdentityID.String())
	}
	normalized, ok := normalizeAccountEmailInput(command.NewEmail)
	if !ok {
		return EmailChangeAvailability{Message: "Enter a valid email address."}, nil
	}
	if normalized == emailutil.NormalizeAddressForDelivery(identity.CurrentEmail()) {
		return EmailChangeAvailability{Message: "This is already the current email for this account."}, nil
	}
	used, err := emailCodeAddressUsedByAnotherIdentity(ctx, s.identity, p.IdentityID.String(), normalized)
	if err != nil {
		return EmailChangeAvailability{}, err
	}
	if used {
		return EmailChangeAvailability{Message: "An account with this email already exists."}, nil
	}
	return EmailChangeAvailability{Available: true, Message: "Email change can continue in account settings."}, nil
}

// RevokeMySession ends one of the principal's other active sessions.
func (s *Service) RevokeMySession(ctx context.Context, command RevokeMySessionCommand) (*Security, error) {
	p, err := s.freshSecurityPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := uuidutil.ParseCanonical(command.SessionID, "session_id"); err != nil {
		return nil, domainerr.InvalidField("session_id", "must be a canonical UUID")
	}
	if command.SessionID == p.SessionID.String() {
		return nil, domainerr.Precondition("the current session can only be ended through logout")
	}
	var owned bool
	if err := s.read(ctx, func(store Store) error {
		var err error
		owned, err = store.OwnsLiveSession(ctx, p.IdentityID.String(), command.SessionID)
		return err
	}); err != nil {
		return nil, err
	}
	if !owned {
		return nil, domainerr.ResourceNotFound("session", command.SessionID)
	}
	if err := s.identity.DeleteSession(ctx, command.SessionID); err != nil {
		return nil, err
	}
	if err := s.appendAccountUpdatedAudit(ctx, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewAccountSessionRevokedAuditRecord(
			metadata, p.MemberID.String(), sharedtelemetry.AccountSessionScopeOne, []string{command.SessionID},
		)
	}); err != nil {
		return nil, err
	}
	return s.securityForIdentity(ctx, p.MemberID.String(), p.IdentityID.String(), p.SessionID.String())
}

// RevokeMyOtherSessions ends every active session of the principal except
// the current one.
func (s *Service) RevokeMyOtherSessions(ctx context.Context) (*Security, error) {
	p, err := s.freshSecurityPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if p.SessionID == "" {
		return nil, domainerr.InvalidSession()
	}
	revokedSessionIDs, err := s.revokeOtherSessions(ctx, p.IdentityID.String(), p.SessionID.String())
	if err != nil {
		return nil, err
	}
	if len(revokedSessionIDs) > 0 {
		if err := s.appendAccountUpdatedAudit(ctx, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewAccountSessionRevokedAuditRecord(
				metadata, p.MemberID.String(), sharedtelemetry.AccountSessionScopeOthers, revokedSessionIDs,
			)
		}); err != nil {
			return nil, err
		}
	}
	return s.securityForIdentity(ctx, p.MemberID.String(), p.IdentityID.String(), p.SessionID.String())
}

// RequestAccountDeletion emails the principal a deletion confirmation link.
func (s *Service) RequestAccountDeletion(ctx context.Context) (AccountDeletionRequestResult, error) {
	p, err := s.freshSecurityPrincipal(ctx)
	if err != nil {
		return AccountDeletionRequestResult{}, err
	}
	result, err := s.requestDeletion(ctx, p)
	if err != nil {
		return AccountDeletionRequestResult{}, err
	}
	return *result, nil
}
