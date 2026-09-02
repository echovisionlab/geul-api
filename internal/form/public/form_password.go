package public

import (
	"context"

	"connectrpc.com/connect"
	"golang.org/x/crypto/bcrypt"

	"github.com/echovisionlab/geul-api/internal/crypto"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// VerifyPassword validates one Form access password without exposing its hash.
func (s *FormService) VerifyPassword(
	ctx context.Context,
	req *connect.Request[openv1.VerifyFormPasswordRequest],
) (*connect.Response[openv1.VerifyFormPasswordResponse], error) {
	form, err := s.findFormBySlugOrID(ctx, req.Msg.Slug)
	if err != nil {
		return nil, err
	}

	shareTokenState := s.validateShareToken(
		ctx,
		form,
		req.Msg.ShareToken,
		req.Msg.SharePassword,
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

	// If form has no password, return false
	if form.AccessPassword == nil || *form.AccessPassword == "" {
		return connect.NewResponse(&openv1.VerifyFormPasswordResponse{
			Valid: false,
		}), nil
	}

	// Verify password
	valid := s.verifyFormPassword(req.Msg.Password, *form.AccessPassword)

	return connect.NewResponse(&openv1.VerifyFormPasswordResponse{
		Valid: valid,
	}), nil
}

// verifyFormPassword checks if the password matches the hash.
// Supports both legacy bcrypt hashes and new argon2id hashes for backward compatibility.
func (s *FormService) verifyFormPassword(password, hash string) bool {
	// Check if it's a legacy bcrypt hash
	if crypto.IsBcryptHash(hash) {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	}

	// Otherwise, use argon2id
	match, err := s.password.Verify(password, hash)
	if err != nil {
		return false
	}
	return match
}
