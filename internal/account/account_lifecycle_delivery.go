package account

import (
	"context"
	"errors"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/failurecause"
)

func (s *AccountLifecycleService) resolveVerifiedAccountEmailForDelivery(
	ctx context.Context,
	identityID string,
	emailType string,
) *VerifiedAccountEmail {
	accountEmail, reason, err := ResolveMemberPrimaryEmailForIdentity(ctx, s.db, s.memberEmails, s.kratosClient, identityID)
	if err != nil {
		var upstreamError *auth.KratosHTTPError
		privateBody := ""
		if errors.As(err, &upstreamError) {
			privateBody = upstreamError.Body
		}
		slog.ErrorContext(ctx, "failed to resolve verified account email for delivery", "identity_id", identityID, "email_type", emailType, "failure_cause", failurecause.Message(err, privateBody))
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
