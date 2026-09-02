package account

import (
	"context"
	"log/slog"
)

func (s *AccountLifecycleService) resolveVerifiedAccountEmailForDelivery(
	ctx context.Context,
	identityID string,
	emailType string,
) *VerifiedAccountEmail {
	accountEmail, reason, err := ResolveMemberPrimaryEmailForIdentity(ctx, s.db, s.memberEmails, s.kratosClient, identityID)
	if err != nil {
		slog.Warn("failed to resolve verified account email for delivery", "identity_id", identityID, "email_type", emailType, "error", err)
	}
	if accountEmail != nil {
		return accountEmail
	}
	message := "account email is not deliverable"
	if reason != "" {
		message = reason
	}
	slog.Info("account email delivery skipped before publish", "identity_id", identityID, "email_type", emailType, "reason", reason, "message", message)
	return nil
}
