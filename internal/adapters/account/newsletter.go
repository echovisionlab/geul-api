package accountadapter

import (
	"context"

	accountdomain "github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/member"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

type NewsletterSubscription struct{}

var _ accountdomain.NewsletterSubscription = NewsletterSubscription{}

func (NewsletterSubscription) Mutate(ctx context.Context, db *gorm.DB, identityID string, subscribed bool) (bool, error) {
	return member.MutateNewsletterSubscription(ctx, db, identityID, subscribed)
}

func (NewsletterSubscription) State(ctx context.Context, db *gorm.DB, identityID string) (*managev1.NewsletterSubscriptionState, error) {
	return member.SubscriptionState(ctx, db, identityID)
}

func (NewsletterSubscription) AppendRequestAudit(
	ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, memberID string,
	previous, next sharedtelemetry.AuditState,
) error {
	return member.AppendNewsletterSubscriptionRequestAudit(ctx, tx, writer, memberID, previous, next)
}

func (NewsletterSubscription) AuditStates(subscribed bool) (sharedtelemetry.AuditState, sharedtelemetry.AuditState) {
	return member.NewsletterAuditStates(subscribed)
}
