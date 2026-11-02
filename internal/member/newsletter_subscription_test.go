package member

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const newsletterSubscriptionTestSecret = "newsletter-subscription-test-secret-at-least-32-bytes"

func TestNewsletterUnsubscribeTokenAcceptsOnlyIdentityScopedTokens(t *testing.T) {
	identityID := uuid.NewString()
	token := crypto.GenerateSignedToken(
		NewsletterUnsubscribeTokenID(identityID),
		crypto.PurposeUnsubscribe,
		time.Time{},
		newsletterSubscriptionTestSecret,
	)

	got, err := ValidateNewsletterUnsubscribeToken(token, newsletterSubscriptionTestSecret)
	require.NoError(t, err)
	require.Equal(t, identityID, got)

	memberToken := crypto.GenerateSignedToken(
		"newsletter-member:"+uuid.NewString(),
		crypto.PurposeUnsubscribe,
		time.Time{},
		newsletterSubscriptionTestSecret,
	)
	_, err = ValidateNewsletterUnsubscribeToken(memberToken, newsletterSubscriptionTestSecret)
	require.Error(t, err)
}
