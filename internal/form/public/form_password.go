package public

import (
	"context"
	"errors"
	"log/slog"

	"golang.org/x/crypto/bcrypt"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// VerifyFormPasswordQuery checks one Form access password, with the share
// link that opens the Form when the visitor holds one.
type VerifyFormPasswordQuery struct {
	Slug          string
	Password      string
	ShareToken    *string
	SharePassword *string
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
	return s.verifyFormPassword(ctx, query.Password, *found.AccessPassword), nil
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
