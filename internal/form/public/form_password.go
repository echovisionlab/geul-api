package public

import (
	"context"
	"errors"
	"log/slog"

	"golang.org/x/crypto/bcrypt"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/form"
)

// VerifyFormPasswordQuery checks one Form access password, with the share
// link that opens the Form when the visitor holds one.
type VerifyFormPasswordQuery struct {
	Slug          string
	Password      string
	ShareToken    *string
	SharePassword *string
	// ClientAddress is the trusted client address that owns the password
	// attempt budget.
	ClientAddress string
}

// VerifyFormPassword validates one Form access password without exposing its
// hash. A Form without a password accepts none.
func (s *Service) VerifyFormPassword(ctx context.Context, query VerifyFormPasswordQuery) (bool, error) {
	found, err := s.findFormBySlugOrID(ctx, query.Slug)
	if err != nil {
		return false, err
	}
	shareTokenState := s.validateShareToken(
		ctx,
		&found,
		query.ShareToken,
		query.SharePassword,
		ShareLinkForm,
		ShareLinkFormDashboard,
	)
	if err := s.enforceFormAccess(ctx, s.reads(), &found, formAccessOptions{
		context:                  ContextURL,
		hasValidPreviewToken:     shareTokenState.valid,
		bypassAuth:               shareTokenState.valid,
		bypassRoles:              shareTokenState.valid,
		draftAsNotFound:          true,
		enforcePassword:          false,
		checkSubmissionLimit:     false,
		checkDuplicateSubmission: true,
	}); err != nil {
		return false, err
	}
	if !found.HasPassword() {
		return false, nil
	}
	return s.checkFormPassword(ctx, &found, query.ClientAddress, query.Password)
}

// checkFormPassword verifies a password for found within the client's failed
// attempt budget. An exhausted budget rejects even a correct password until
// the window passes, so guesses cannot be confirmed by timing the limit.
func (s *Service) checkFormPassword(ctx context.Context, found *form.Form, clientAddress, password string) (bool, error) {
	key := passwordAttemptKey(found.ID, clientAddress)
	if s.attempts.exhausted(key) {
		return false, domainerr.TooManyAttempts("too many Form password attempts; try again later")
	}
	matched := s.verifyFormPassword(ctx, password, *found.AccessPassword)
	s.attempts.record(key, matched)
	return matched, nil
}

// verifyFormPassword checks if the password matches the hash.
// Supports both legacy bcrypt hashes and new argon2id hashes for backward compatibility.
func (s *Service) verifyFormPassword(ctx context.Context, password, hash string) bool {
	// Check if it's a legacy bcrypt hash
	if crypto.IsBcryptHash(hash) {
		err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
		if err != nil && !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			slog.ErrorContext(ctx, "Form password hash verification failed; rejecting the password", "domain", "form", "event", "form.operation_failed", "operation", "verify_form_password", "outcome", "failed", failurecause.Attr(err, password, hash))
		}
		return err == nil
	}

	// Otherwise, use argon2id
	match, err := s.password.Verify(password, hash)
	if err != nil {
		slog.ErrorContext(ctx, "Form password hash verification failed; rejecting the password", "domain", "form", "event", "form.operation_failed", "operation", "verify_form_password", "outcome", "failed", failurecause.Attr(err, password, hash))
		return false
	}
	return match
}
