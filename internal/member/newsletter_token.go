package member

import (
	"strings"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
)

// The Identity-specific prefix prevents a domain Member UUID from being
// silently reinterpreted as the subscription owner.
const newsletterIdentityUnsubscribeTokenPrefix = "newsletter-identity:"

func NewsletterUnsubscribeTokenID(identityID string) string {
	return newsletterIdentityUnsubscribeTokenPrefix + strings.TrimSpace(identityID)
}

func ValidateNewsletterUnsubscribeToken(rawToken, tokenSecret string) (string, error) {
	signedToken, err := crypto.ValidateSignedToken(strings.TrimSpace(rawToken), tokenSecret)
	if err != nil || signedToken.Purpose != crypto.PurposeUnsubscribe {
		return "", crypto.ErrInvalidToken
	}
	if !strings.HasPrefix(signedToken.ID, newsletterIdentityUnsubscribeTokenPrefix) {
		return "", crypto.ErrInvalidToken
	}
	identityID := strings.TrimPrefix(signedToken.ID, newsletterIdentityUnsubscribeTokenPrefix)
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return "", crypto.ErrInvalidToken
	}
	return identityID, nil
}
