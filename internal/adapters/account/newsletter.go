package accountadapter

import (
	"context"

	accountdomain "github.com/echovisionlab/geul-api/internal/account"
	accountpostgres "github.com/echovisionlab/geul-api/internal/account/postgres"
	"github.com/echovisionlab/geul-api/internal/member"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	"gorm.io/gorm"
)

// NewsletterSubscription is the Member-owned newsletter fact an Account
// request changes, in the Account transaction.
type NewsletterSubscription struct{}

var _ accountpostgres.Newsletter = NewsletterSubscription{}

func (NewsletterSubscription) Mutate(ctx context.Context, db *gorm.DB, identityID string, subscribed bool) (bool, error) {
	return member.MutateNewsletterSubscription(ctx, memberpostgres.Bind(db), identityID, subscribed)
}

func (NewsletterSubscription) State(ctx context.Context, db *gorm.DB, identityID string) (accountdomain.NewsletterState, error) {
	state, err := member.SubscriptionState(ctx, memberpostgres.Bind(db), identityID)
	return accountdomain.NewsletterState{Subscribed: state.Subscribed, SubscribedAt: state.SubscribedAt}, err
}
