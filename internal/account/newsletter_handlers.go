package account

import (
	"context"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

func (s *AccountService) SetMyNewsletterSubscription(
	ctx context.Context, req *connect.Request[managev1.SetMyNewsletterSubscriptionRequest],
) (*connect.Response[managev1.NewsletterSubscriptionMutationResponse], error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.mutateNewsletterSubscriptionWithAudit(
		ctx, principal.IdentityID.String(), principal.MemberID.String(), req.Msg.GetSubscribed(),
	); err != nil {
		return nil, errs.Internal(err)
	}
	if s.newsletter == nil {
		return nil, errs.InternalMsg("member newsletter subscription is unavailable")
	}
	state, err := s.newsletter.State(ctx, s.db, principal.IdentityID.String())
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(
		&managev1.NewsletterSubscriptionMutationResponse{NewsletterSubscription: state},
	), nil
}

func (s *AccountService) UnsubscribeAccountFromNewsletter(
	ctx context.Context, req *connect.Request[managev1.UnsubscribeAccountFromNewsletterRequest],
) (*connect.Response[managev1.NewsletterSubscriptionMutationResponse], error) {
	if _, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb); err != nil {
		return nil, err
	}
	memberID := req.Msg.GetMemberId()
	identityID, err := s.identityIDForMember(ctx, memberID)
	if err != nil {
		return nil, err
	}
	if err := s.mutateNewsletterSubscriptionWithAudit(ctx, identityID, memberID, false); err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(
		&managev1.NewsletterSubscriptionMutationResponse{
			NewsletterSubscription: &managev1.NewsletterSubscriptionState{},
		},
	), nil
}

func (s *AccountService) mutateNewsletterSubscriptionWithAudit(ctx context.Context, identityID, memberID string, subscribed bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if s.newsletter == nil {
			return errs.InternalMsg("member newsletter subscription is unavailable")
		}
		changed, err := s.newsletter.Mutate(ctx, tx, identityID, subscribed)
		if err != nil || !changed || s.auditWriter == nil {
			return err
		}
		previous, next := s.newsletter.AuditStates(subscribed)
		return s.newsletter.AppendRequestAudit(ctx, tx, s.auditWriter, memberID, previous, next)
	})
}
