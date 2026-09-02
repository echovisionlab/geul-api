package account

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/email"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

// PublishAccountSecurityEventEmail sends a specific security notice to the active
// identity's canonical Member primary-email projection. The worker re-checks the exact
// Identity/Member projection before delivery, so the address is never taken
// from a caller.
func PublishAccountSecurityEventEmail(
	ctx context.Context, publisher EmailCommandPublisher, db *gorm.DB,
	memberEmails MemberEmailProjection, identityManager auth.IdentityGetter, identityID string, eventKey email.EventKey,
	operation string, templateData map[string]string,
) error {
	identityID = strings.TrimSpace(identityID)
	operation = strings.TrimSpace(operation)
	if db == nil || identityManager == nil || identityID == "" || eventKey == "" {
		return fmt.Errorf("account security email database, identity, operation, and event are required")
	}
	if operation == "" {
		operation = eventKey.String()
	}
	accountEmail, reason, err := ResolveMemberPrimaryEmailForIdentity(ctx, db, memberEmails, identityManager, identityID)
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
	messageID := "account-security:" + eventKey.String() + ":" + identityID + ":" + operation
	target, err := authorizationtarget.ActiveOnboardedMemberForIdentity(ctx, db, identityID)
	if err != nil {
		return err
	}
	if target.MemberID != memberID {
		return fmt.Errorf("active Identity is not linked to an onboarded Member")
	}
	return email.PublishCommand(ctx, publisher, &managev1.SendEmailEvent{
		Recipient:        accountEmail.Email,
		TemplateType:     eventKey.String(),
		TemplateData:     templateData,
		ReferenceId:      &referenceID,
		RecipientContext: email.AccountSelectedPrimaryEmailContext(identityID),
	}, messageID)
}
