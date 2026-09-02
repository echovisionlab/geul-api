//go:build integration

package account

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

func integrationSpiceDB(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	return testutil.SetupOryStack(t).SpiceDBClient
}

type failingDomainAuditAppender struct{}

func (failingDomainAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return errors.New("audit unavailable")
}

type memberNewsletterSubscriptionIntegration struct{}

func (memberNewsletterSubscriptionIntegration) Mutate(ctx context.Context, db *gorm.DB, identityID string, subscribed bool) (bool, error) {
	return member.MutateNewsletterSubscription(ctx, db, identityID, subscribed)
}

func (memberNewsletterSubscriptionIntegration) State(ctx context.Context, db *gorm.DB, identityID string) (*managev1.NewsletterSubscriptionState, error) {
	return member.SubscriptionState(ctx, db, identityID)
}

func (memberNewsletterSubscriptionIntegration) AppendRequestAudit(ctx context.Context, db *gorm.DB, writer domainaudit.Appender, memberID string, previous, next sharedtelemetry.AuditState) error {
	return member.AppendNewsletterSubscriptionRequestAudit(ctx, db, writer, memberID, previous, next)
}

func (memberNewsletterSubscriptionIntegration) AuditStates(subscribed bool) (sharedtelemetry.AuditState, sharedtelemetry.AuditState) {
	return member.NewsletterAuditStates(subscribed)
}

func newsletterSubscriptionState(
	ctx context.Context,
	db *gorm.DB,
	identityID string,
) (*managev1.NewsletterSubscriptionState, error) {
	var row model.NewsletterSubscription
	err := db.WithContext(ctx).Where("identity_id = ?", identityID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &managev1.NewsletterSubscriptionState{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &managev1.NewsletterSubscriptionState{
		Subscribed:   true,
		SubscribedAt: timestamppb.New(row.SubscribedAt),
	}, nil
}
