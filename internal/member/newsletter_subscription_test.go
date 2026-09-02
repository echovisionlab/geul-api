package member

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const newsletterSubscriptionTestSecret = "newsletter-subscription-test-secret-at-least-32-bytes"

func TestNewsletterSubscriptionUsesExactIdentityAndKeepsFirstOptInTime(t *testing.T) {
	db := newNewsletterSubscriptionTestDB(t)
	identityID := uuid.NewString()

	changed, err := MutateNewsletterSubscription(t.Context(), db, identityID, true)
	require.NoError(t, err)
	require.True(t, changed)
	first, err := newsletterSubscriptionState(t.Context(), db, identityID)
	require.NoError(t, err)
	require.True(t, first.GetSubscribed())
	require.NotNil(t, first.GetSubscribedAt())

	changed, err = MutateNewsletterSubscription(t.Context(), db, identityID, true)
	require.NoError(t, err)
	require.False(t, changed)
	second, err := newsletterSubscriptionState(t.Context(), db, identityID)
	require.NoError(t, err)
	require.Equal(t, first.GetSubscribedAt().AsTime(), second.GetSubscribedAt().AsTime())

	changed, err = MutateNewsletterSubscription(t.Context(), db, identityID, false)
	require.NoError(t, err)
	require.True(t, changed)
	changed, err = MutateNewsletterSubscription(t.Context(), db, identityID, false)
	require.NoError(t, err)
	require.False(t, changed)
	removed, err := newsletterSubscriptionState(t.Context(), db, identityID)
	require.NoError(t, err)
	require.False(t, removed.GetSubscribed())
	require.Nil(t, removed.GetSubscribedAt())
}

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

func newNewsletterSubscriptionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE newsletter_subscription (
			identity_id TEXT PRIMARY KEY,
			subscribed_at DATETIME NOT NULL
		)
	`).Error)
	return db
}
