package account

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
)

// emailCodeAddressUsedByAnotherIdentity checks only the address-specific Email
// Code credential identifier. Provider email assertions are deliberately
// allowed to repeat across identities and must never reserve an email string
// globally. The finder is explicit because this check has a stronger
// dependency than the email projection service itself.
func emailCodeAddressUsedByAnotherIdentity(
	ctx context.Context,
	finder auth.IdentityCredentialFinder,
	identityID string,
	normalizedEmail string,
) (bool, error) {
	normalizedEmail = emailutil.NormalizeAddressForDelivery(normalizedEmail)
	if !validProjectedAccountEmail(normalizedEmail) {
		return false, nil
	}
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return false, fmt.Errorf("identity id is required")
	}
	if finder == nil {
		return false, fmt.Errorf("identity credential finder is required")
	}
	identity, found, err := finder.FindIdentityByCredentialIdentifier(ctx, normalizedEmail)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if identity == nil {
		return false, fmt.Errorf("identity credential finder returned no identity")
	}
	return strings.TrimSpace(identity.ID) != identityID, nil
}
