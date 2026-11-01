package member

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// MutateNewsletterSubscription changes only the Identity-owned subscription
// row. Its boolean result is true exactly when the row-presence authority
// transitioned, so callers can avoid auditing replayed opt-in/out requests.
func MutateNewsletterSubscription(ctx context.Context, store NewsletterStore, identityID string, subscribed bool) (bool, error) {
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return false, err
	}
	if subscribed {
		return store.SubscribeNewsletter(ctx, identityID, time.Now().UTC())
	}
	return store.UnsubscribeNewsletter(ctx, identityID)
}

func newsletterSubscriptionState(ctx context.Context, store NewsletterStore, identityID string) (NewsletterState, error) {
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return NewsletterState{}, err
	}
	subscribedAt, err := store.NewsletterSubscription(ctx, identityID)
	if err != nil {
		return NewsletterState{}, err
	}
	return newsletterState(subscribedAt), nil
}

// SubscriptionState returns the Identity-owned newsletter fact.
func SubscriptionState(ctx context.Context, store NewsletterStore, identityID string) (NewsletterState, error) {
	return newsletterSubscriptionState(ctx, store, identityID)
}

// NewsletterSubscriptionAuditBuilder builds the account record of a
// newsletter subscription transition of memberID.
func NewsletterSubscriptionAuditBuilder(
	memberID string,
	previous, next sharedtelemetry.AuditState,
) domainaudit.Builder {
	return func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewAccountNewsletterSubscriptionUpdatedAuditRecord(metadata, memberID, previous, next)
	}
}
