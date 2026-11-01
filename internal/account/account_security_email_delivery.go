package account

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
)

// PublishAccountSecurityEventEmail sends a specific security notice to the active
// identity's canonical Member primary-email projection. The worker re-checks the exact
// Identity/Member projection before delivery, so the address is never taken
// from a caller. read runs the Member reads.
func PublishAccountSecurityEventEmail(
	ctx context.Context, publisher EmailPublisher, read StepFunc,
	identityManager auth.IdentityGetter, identityID string, eventKey email.EventKey,
	operation string, templateData map[string]string,
) error {
	identityID = strings.TrimSpace(identityID)
	operation = strings.TrimSpace(operation)
	if read == nil || identityManager == nil || identityID == "" || eventKey == "" {
		return fmt.Errorf("account security email database, identity, operation, and event are required")
	}
	if operation == "" {
		operation = eventKey.String()
	}
	accountEmail, reason, err := ResolveMemberPrimaryEmailForIdentity(ctx, read, identityManager, identityID)
	if err != nil {
		return err
	}
	if accountEmail == nil || accountEmail.Identity == nil {
		if reason == "" {
			reason = AccountEmailSkipReasonEmailUnverified
		}
		return fmt.Errorf("active Identity has no usable Member primary email: %s", reason)
	}
	memberID := strings.TrimSpace(accountEmail.Identity.ExternalID)
	if memberID == "" {
		return fmt.Errorf("active Identity has no Member link")
	}
	referenceID := identityID
	var target Target
	if err := read(ctx, func(store Store) error {
		var err error
		target, err = store.ActiveOnboardedMemberForIdentity(ctx, identityID)
		return err
	}); err != nil {
		return err
	}
	if target.MemberID != memberID {
		return fmt.Errorf("active Identity is not linked to an onboarded Member")
	}
	job := Email{
		Recipient:         accountEmail.Email,
		TemplateType:      eventKey.String(),
		TemplateData:      templateData,
		ReferenceID:       &referenceID,
		AccountIdentityID: identityID,
		MessageID:         "account-security:" + eventKey.String() + ":" + identityID + ":" + operation,
	}
	if err := publisher.PublishEmail(ctx, job); err != nil {
		return emailPublishFailure(err, job)
	}
	return nil
}
