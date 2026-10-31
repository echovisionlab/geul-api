package public

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/member"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type NewsletterService struct {
	openv1connect.UnimplementedNewsletterServiceHandler
	db          *gorm.DB
	tokenSecret string
	auditWriter domainaudit.Appender
}

func NewNewsletterService(db *gorm.DB, tokenSecret string) *NewsletterService {
	if db == nil {
		panic("db is required")
	}
	if strings.TrimSpace(tokenSecret) == "" {
		panic("newsletter token secret is required")
	}
	return &NewsletterService{db: db, tokenSecret: tokenSecret}
}

func NewAuditedNewsletterService(
	db *gorm.DB,
	tokenSecret string,
	auditWriter domainaudit.Appender,
) *NewsletterService {
	if auditWriter == nil {
		panic("newsletter audit writer is required")
	}
	service := NewNewsletterService(db, tokenSecret)
	service.auditWriter = auditWriter
	return service
}

func (s *NewsletterService) Unsubscribe(
	ctx context.Context,
	req *connect.Request[openv1.UnsubscribeNewsletterRequest],
) (*connect.Response[openv1.UnsubscribeNewsletterResponse], error) {
	identityID, err := member.ValidateNewsletterUnsubscribeToken(req.Msg.GetToken(), s.tokenSecret)
	if err != nil {
		return nil, errs.InvalidArgument("token", "must be a valid unsubscribe token")
	}
	if err := s.unsubscribeWithAudit(ctx, identityID); err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&openv1.UnsubscribeNewsletterResponse{Success: true}), nil
}

func (s *NewsletterService) unsubscribeWithAudit(ctx context.Context, identityID string) error {
	if s.auditWriter == nil {
		_, err := member.MutateNewsletterSubscription(ctx, s.db, identityID, false)
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		memberID, err := newsletterMemberIDForIdentity(ctx, tx, identityID)
		if err != nil {
			return err
		}
		changed, err := member.MutateNewsletterSubscription(ctx, tx, identityID, false)
		if err != nil || !changed {
			return err
		}
		return member.AppendNewsletterSubscriptionMemberAudit(
			ctx,
			tx,
			s.auditWriter,
			memberID,
			sharedtelemetry.AuditStateSubscribed,
			sharedtelemetry.AuditStateUnsubscribed,
		)
	})
}

func newsletterMemberIDForIdentity(ctx context.Context, tx *gorm.DB, identityID string) (string, error) {
	var member struct {
		ID string `gorm:"column:id"`
	}
	err := tx.WithContext(ctx).
		Table("member AS m").
		Select("m.id::text AS id").
		Where("m.account_identity_id = ?::uuid AND m.deleted_at IS NULL", identityID).
		Take(&member).Error
	return member.ID, err
}
