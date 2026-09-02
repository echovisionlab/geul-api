package member

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MutateNewsletterSubscription changes only the Identity-owned subscription
// row. Its boolean result is true exactly when the row-presence authority
// transitioned, so callers can avoid auditing replayed opt-in/out requests.
func MutateNewsletterSubscription(ctx context.Context, db *gorm.DB, identityID string, subscribed bool) (bool, error) {
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return false, err
	}
	if subscribed {
		row := model.NewsletterSubscription{
			IdentityID:   identityID,
			SubscribedAt: time.Now().UTC(),
		}
		result := db.WithContext(ctx).
			Clauses(clause.OnConflict{DoNothing: true}).
			Create(&row)
		return result.RowsAffected == 1, result.Error
	}
	result := db.WithContext(ctx).
		Where("identity_id = ?", identityID).
		Delete(&model.NewsletterSubscription{})
	return result.RowsAffected == 1, result.Error
}

func newsletterSubscriptionState(
	ctx context.Context,
	db *gorm.DB,
	identityID string,
) (*managev1.NewsletterSubscriptionState, error) {
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return nil, err
	}
	var row model.NewsletterSubscription
	err := db.WithContext(ctx).
		Where("identity_id = ?", identityID).
		Take(&row).Error
	if err == gorm.ErrRecordNotFound {
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

// SubscriptionState returns the Identity-owned newsletter fact.
func SubscriptionState(ctx context.Context, db *gorm.DB, identityID string) (*managev1.NewsletterSubscriptionState, error) {
	return newsletterSubscriptionState(ctx, db, identityID)
}

func newsletterSubscriptionAuditStates(subscribed bool) (sharedtelemetry.AuditState, sharedtelemetry.AuditState) {
	if subscribed {
		return sharedtelemetry.AuditStateUnsubscribed, sharedtelemetry.AuditStateSubscribed
	}
	return sharedtelemetry.AuditStateSubscribed, sharedtelemetry.AuditStateUnsubscribed
}

// NewsletterAuditStates returns the exact transition for account transport.
func NewsletterAuditStates(subscribed bool) (sharedtelemetry.AuditState, sharedtelemetry.AuditState) {
	return newsletterSubscriptionAuditStates(subscribed)
}

func appendNewsletterSubscriptionRequestAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	previous, next sharedtelemetry.AuditState,
) error {
	return domainaudit.AppendRequest(
		ctx,
		tx,
		writer,
		sharedtelemetry.AuditAccountUpdated,
		newsletterSubscriptionAuditBuilder(memberID, previous, next),
	)
}

// AppendNewsletterSubscriptionRequestAudit appends the owning Member fact
// transition through its authenticated Account transport.
func AppendNewsletterSubscriptionRequestAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, memberID string, previous, next sharedtelemetry.AuditState) error {
	return appendNewsletterSubscriptionRequestAudit(ctx, tx, writer, memberID, previous, next)
}

// AppendNewsletterSubscriptionMemberAudit records a capability-authenticated
// token opt-out with the exact linked Member as its actor and account target.
func AppendNewsletterSubscriptionMemberAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	previous, next sharedtelemetry.AuditState,
) error {
	return domainaudit.AppendMember(
		ctx,
		tx,
		writer,
		memberID,
		sharedtelemetry.AuditAccountUpdated,
		newsletterSubscriptionAuditBuilder(memberID, previous, next),
	)
}

func newsletterSubscriptionAuditBuilder(
	memberID string,
	previous, next sharedtelemetry.AuditState,
) domainaudit.Builder {
	return func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewAccountNewsletterSubscriptionUpdatedAuditRecord(metadata, memberID, previous, next)
	}
}
