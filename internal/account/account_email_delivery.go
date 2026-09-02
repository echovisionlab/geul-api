package account

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"gorm.io/gorm"
)

const (
	AccountEmailSkipReasonIdentityMissing   = "identity_missing"
	AccountEmailSkipReasonIdentityInactive  = "identity_inactive"
	AccountEmailSkipReasonEmailMissing      = "email_missing"
	AccountEmailSkipReasonCanonicalMismatch = "canonical_email_mismatch"
	AccountEmailSkipReasonEmailUnverified   = "email_unverified"
	AccountEmailSkipReasonMemberLink        = "member_link_mismatch"
)

type VerifiedAccountEmail struct {
	Identity *auth.Identity
	Email    string
}

// LoadIdentityWithEmailCredentials merges the OIDC and email-code credential
// views needed to evaluate delivery candidates. Kratos returns credential
// config per include request, so a single read cannot safely answer both.
func LoadIdentityWithEmailCredentials(
	ctx context.Context,
	identityManager auth.IdentityGetter,
	identityID string,
) (*auth.Identity, error) {
	return loadIdentityWithCredentials(ctx, identityManager, identityID, "oidc", "code")
}

// ResolveMemberPrimaryEmailForIdentity starts from the exact active Identity,
// validates its bilateral Member link, and returns the canonical delivery
// address only while the Member projection and current proven candidate agree.
func ResolveMemberPrimaryEmailForIdentity(
	ctx context.Context,
	db *gorm.DB,
	memberEmails MemberEmailProjection,
	identityManager auth.IdentityGetter,
	identityID string,
) (*VerifiedAccountEmail, string, error) {
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return nil, AccountEmailSkipReasonIdentityMissing, nil
	}
	if db == nil || memberEmails == nil || identityManager == nil {
		return nil, "", fmt.Errorf("member email projection, database, and identity manager are required")
	}
	identity, err := LoadIdentityWithEmailCredentials(ctx, identityManager, identityID)
	if err != nil {
		return nil, "", err
	}
	if identity == nil || strings.TrimSpace(identity.ID) != identityID {
		return nil, AccountEmailSkipReasonIdentityMissing, nil
	}
	if identity.IsBanned() {
		return nil, AccountEmailSkipReasonIdentityInactive, nil
	}
	memberID := strings.TrimSpace(identity.ExternalID)
	if memberID == "" {
		return nil, AccountEmailSkipReasonMemberLink, nil
	}
	primaryEmail, err := memberEmails.PrimaryEmail(ctx, db, memberID, identityID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, AccountEmailSkipReasonMemberLink, nil
		}
		return nil, "", err
	}
	primaryEmail = strings.TrimSpace(primaryEmail)
	if primaryEmail == "" {
		return nil, AccountEmailSkipReasonEmailMissing, nil
	}
	if emailutil.NormalizeAddressForDelivery(primaryEmail) != emailutil.NormalizeAddressForDelivery(identity.CurrentEmail()) {
		return nil, AccountEmailSkipReasonCanonicalMismatch, nil
	}
	if !IdentityHasUsableDeliveryEmail(ctx, identity, primaryEmail) {
		return nil, AccountEmailSkipReasonEmailUnverified, nil
	}
	return &VerifiedAccountEmail{Identity: identity, Email: primaryEmail}, "", nil
}

// IdentityHasUsableDeliveryEmail verifies that email is still backed by an
// address-specific Email Code credential or trusted linked-provider assertion.
// For older OIDC credential records, the provider assertion may need to be
// refreshed from the connected provider endpoint before the delivery decision.
func IdentityHasUsableDeliveryEmail(ctx context.Context, identity *auth.Identity, email string) bool {
	if identity == nil {
		return false
	}
	providerCandidates := ResolveAccountEmailProviderCandidates(ctx, identity.Credentials)
	row := findProjectionRow(projectedAccountEmailRows(identity, providerCandidates), email)
	return row != nil && row.UsableForDelivery
}
