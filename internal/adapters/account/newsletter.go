package accountadapter

import (
	"context"

	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	"google.golang.org/protobuf/types/known/timestamppb"

	accountdomain "github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/member"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

type NewsletterSubscription struct{}

var _ accountdomain.NewsletterSubscription = NewsletterSubscription{}

func (NewsletterSubscription) Mutate(ctx context.Context, db *gorm.DB, identityID string, subscribed bool) (bool, error) {
	return member.MutateNewsletterSubscription(ctx, memberpostgres.Bind(db), identityID, subscribed)
}

func (NewsletterSubscription) State(ctx context.Context, db *gorm.DB, identityID string) (*managev1.NewsletterSubscriptionState, error) {
	state, err := member.SubscriptionState(ctx, memberpostgres.Bind(db), identityID)
	if err != nil {
		return nil, err
	}
	message := &managev1.NewsletterSubscriptionState{Subscribed: state.Subscribed}
	if state.SubscribedAt != nil {
		message.SubscribedAt = timestamppb.New(*state.SubscribedAt)
	}
	return message, nil
}

func (NewsletterSubscription) AuditStates(subscribed bool) (sharedtelemetry.AuditState, sharedtelemetry.AuditState) {
	return member.NewsletterAuditStates(subscribed)
}
