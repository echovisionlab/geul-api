package account

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// SetMyNewsletterSubscriptionCommand subscribes or unsubscribes the
// principal.
type SetMyNewsletterSubscriptionCommand struct{ Subscribed bool }

// UnsubscribeAccountFromNewsletterCommand unsubscribes a Member.
type UnsubscribeAccountFromNewsletterCommand struct{ MemberID string }

// SetMyNewsletterSubscription changes the principal's newsletter
// subscription and returns its state.
func (s *Service) SetMyNewsletterSubscription(ctx context.Context, command SetMyNewsletterSubscriptionCommand) (NewsletterState, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return NewsletterState{}, err
	}
	if err := s.mutateNewsletterSubscription(
		ctx, mutation.PrincipalFence(principal), nil, principal.IdentityID.String(), principal.MemberID.String(), command.Subscribed,
	); err != nil {
		return NewsletterState{}, err
	}
	var state NewsletterState
	err = s.read(ctx, func(store Store) error {
		var err error
		state, err = store.NewsletterState(ctx, principal.IdentityID.String())
		return err
	})
	return state, err
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

// newsletterAuditStates is the subscription transition a change records.
func newsletterAuditStates(subscribed bool) (sharedtelemetry.AuditState, sharedtelemetry.AuditState) {
	if subscribed {
		return sharedtelemetry.AuditStateUnsubscribed, sharedtelemetry.AuditStateSubscribed
	}
	return sharedtelemetry.AuditStateSubscribed, sharedtelemetry.AuditStateUnsubscribed
}

func (s *Service) mutateNewsletterSubscription(
	ctx context.Context, access mutation.Access, subjects []string, identityID, memberID string, subscribed bool,
) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[bool, Store]{
		Subjects: subjects,
		Access:   access,
		Change: func(ctx context.Context, store Store) (bool, error) {
			return store.MutateNewsletter(ctx, identityID, subscribed)
		},
		Audit: func(changed bool) mutation.Audit {
			if !changed {
				return mutation.Unchanged()
			}
			previous, next := newsletterAuditStates(subscribed)
			return mutation.RequestAudit(sharedtelemetry.AuditAccountUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAccountNewsletterSubscriptionUpdatedAuditRecord(metadata, memberID, previous, next)
			})
		},
	})
	return err
}
