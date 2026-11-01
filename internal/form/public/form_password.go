package public

import (
	"context"
	"errors"
	"log/slog"

	"golang.org/x/crypto/bcrypt"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
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
func (s *Service) VerifyFormPassword(ctx context.Context, query VerifyFormPasswordQuery) (*openv1.VerifyFormPasswordResponse, error) {
	form, err := s.findFormBySlugOrID(ctx, query.Slug)
	if err != nil {
		return nil, err
	}
	shareTokenState := s.validateShareToken(
		ctx,
		form,
		query.ShareToken,
		query.SharePassword,
		managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM,
		managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD,
	)
	if err := s.enforceFormAccess(ctx, form, formAccessOptions{
		context:                  openv1.FormAccessContext_FORM_ACCESS_CONTEXT_URL,
		hasValidPreviewToken:     shareTokenState.valid,
		bypassAuth:               shareTokenState.valid,
		bypassRoles:              shareTokenState.valid,
		draftAsNotFound:          true,
		enforcePassword:          false,
		checkSubmissionLimit:     false,
		checkDuplicateSubmission: true,
	}); err != nil {
		return nil, err
	}
	if form.AccessPassword == nil || *form.AccessPassword == "" {
		return &openv1.VerifyFormPasswordResponse{Valid: false}, nil
	}
	return &openv1.VerifyFormPasswordResponse{Valid: s.verifyFormPassword(ctx, query.Password, *form.AccessPassword)}, nil
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
