package account

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

// SetMyNewsletterSubscriptionCommand subscribes or unsubscribes the
// principal.
type SetMyNewsletterSubscriptionCommand struct{ Subscribed bool }

// UnsubscribeAccountFromNewsletterCommand unsubscribes a Member.
type UnsubscribeAccountFromNewsletterCommand struct{ MemberID string }

// SetMyNewsletterSubscription changes the principal's newsletter
// subscription and returns its state.
func (s *Service) SetMyNewsletterSubscription(ctx context.Context, command SetMyNewsletterSubscriptionCommand) (*managev1.NewsletterSubscriptionState, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.mutateNewsletterSubscription(
		ctx, mutation.PrincipalFence(principal), nil, principal.IdentityID.String(), principal.MemberID.String(), command.Subscribed,
	); err != nil {
		return nil, err
	}
	return s.newsletter.State(ctx, s.db, principal.IdentityID.String())
}

// UnsubscribeAccountFromNewsletter unsubscribes another Member on an
// administrator's behalf.
func (s *Service) UnsubscribeAccountFromNewsletter(ctx context.Context, command UnsubscribeAccountFromNewsletterCommand) error {
	if _, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb); err != nil {
		return err
	}
	identityID, err := s.identityIDForMember(ctx, command.MemberID)
	if err != nil {
		return err
	}
	return s.mutateNewsletterSubscription(ctx, freshAdmin(), []string{identityID}, identityID, command.MemberID, false)
}

func (s *Service) mutateNewsletterSubscription(
	ctx context.Context, access mutation.Access, subjects []string, identityID, memberID string, subscribed bool,
) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[bool]{
		Subjects: subjects,
		Access:   access,
		Change: func(ctx context.Context, tx *gorm.DB) (bool, error) {
			return s.newsletter.Mutate(ctx, tx, identityID, subscribed)
		},
		Audit: func(changed bool) mutation.Audit {
			if !changed {
				return mutation.Unchanged()
			}
			previous, next := s.newsletter.AuditStates(subscribed)
			return mutation.RequestAudit(sharedtelemetry.AuditAccountUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAccountNewsletterSubscriptionUpdatedAuditRecord(metadata, memberID, previous, next)
			})
		},
	})
	return err
}
