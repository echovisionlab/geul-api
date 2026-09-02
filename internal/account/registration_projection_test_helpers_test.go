package account

import (
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
)

type RegistrationMemberInput struct {
	IdentityID string
	Email      string
}

func validateRegistrationIdentity(input RegistrationMemberInput, identity *auth.Identity, candidates []AccountEmailProviderCandidate) error {
	if identity == nil || identity.ID != input.IdentityID {
		return fmt.Errorf("identity lookup returned a different identity")
	}
	if _, err := uuidutil.ParseCanonical(identity.ID, "identity_id"); err != nil {
		return err
	}
	if identity.State != auth.KratosStateActive || identity.IsBanned() {
		return fmt.Errorf("registration identity is not active")
	}
	if emailutil.NormalizeAddressForDelivery(identity.CurrentEmail()) != emailutil.NormalizeAddressForDelivery(input.Email) {
		return fmt.Errorf("registration email does not match the exact identity")
	}
	row := FindAccountEmailProjection(ProjectedAccountEmailRows(identity, candidates), input.Email)
	if row == nil || !row.UsableForDelivery {
		return fmt.Errorf("registration email is not proven by the exact identity")
	}
	return nil
}
