package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// UnsubscribeNewsletterCommand opts out the Identity a signed unsubscribe
// token names.
type UnsubscribeNewsletterCommand struct{ Token string }

// UnsubscribeNewsletter removes the newsletter subscription of the token's
// Identity. Repeating it changes nothing.
func (s *Service) UnsubscribeNewsletter(ctx context.Context, command UnsubscribeNewsletterCommand) error {
	identityID, err := member.ValidateNewsletterUnsubscribeToken(command.Token, s.tokenSecret)
	if err != nil {
		return domainerr.InvalidField("token", "must be a valid unsubscribe token")
	}
	var memberID string
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[bool, Store]{
		// The signed token is the authority. It names an Identity whose live
		// Member the opt-out is attributed to.
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			var err error
			memberID, err = store.NewsletterMemberID(ctx, identityID)
			return err
		}),
		Change: func(ctx context.Context, store Store) (bool, error) {
			return member.MutateNewsletterSubscription(ctx, store, identityID, false)
		},
		Audit: func(changed bool) mutation.Audit {
			if !changed {
				return mutation.Unchanged()
			}
			return mutation.MemberAudit(memberID, sharedtelemetry.AuditAccountUpdated, member.NewsletterSubscriptionAuditBuilder(
				memberID, sharedtelemetry.AuditStateSubscribed, sharedtelemetry.AuditStateUnsubscribed,
			))
		},
	})
	return err
}
