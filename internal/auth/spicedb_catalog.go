package auth

import (
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/uuidutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// AccountIdentitySubject is the only direct SpiceDB subject. Member is a
// product/profile projection and must never be used as an authorization key.
type AccountIdentitySubject struct{ ID IdentityID }

func NewAccountIdentitySubject(identityID IdentityID) (AccountIdentitySubject, error) {
	if strings.TrimSpace(identityID.String()) == "" {
		return AccountIdentitySubject{}, fmt.Errorf("account identity id is required")
	}
	if _, err := uuidutil.ParseCanonical(identityID.String(), "account identity id"); err != nil {
		return AccountIdentitySubject{}, err
	}
	return AccountIdentitySubject{ID: identityID}, nil
}

var spiceDBAccountIdentityObjectType = policyv1.AccountIdentity.Type()
