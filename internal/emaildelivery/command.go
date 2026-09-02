package emaildelivery

import (
	"fmt"
	"strings"
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"

	"github.com/echovisionlab/geul-api/internal/email"
)

// EmailCommandExpiresAt validates the expiry contract shared by authentication
// commands. Ordinary email commands intentionally have no expiry.
func EmailCommandExpiresAt(job *managev1.SendEmailEvent) (time.Time, bool, error) {
	if job == nil {
		return time.Time{}, false, fmt.Errorf("email command is required")
	}
	authCommand := isAuthenticationCommandTemplateType(job.GetTemplateType())
	if !authCommand {
		if job.GetExpiresAt() != nil || strings.TrimSpace(job.GetIssuanceId()) != "" {
			return time.Time{}, false, fmt.Errorf("non-auth email command cannot carry auth expiry")
		}
		return time.Time{}, false, nil
	}
	if strings.TrimSpace(job.GetMessageId()) == "" {
		return time.Time{}, true, fmt.Errorf("authentication email message id is required")
	}
	if strings.TrimSpace(job.GetIssuanceId()) == "" {
		return time.Time{}, true, fmt.Errorf("authentication email issuance id is required")
	}
	if job.GetExpiresAt() == nil || !job.GetExpiresAt().IsValid() {
		return time.Time{}, true, fmt.Errorf("authentication email expiry is required")
	}
	return job.GetExpiresAt().AsTime().UTC(), true, nil
}

func isAuthenticationCommandTemplateType(templateType string) bool {
	switch email.EventKey(strings.TrimSpace(templateType)) {
	case email.EventVerificationCode, email.EventLoginCode, email.EventRegistrationCode:
		return true
	default:
		return false
	}
}

func EmailCommandExpired(job *managev1.SendEmailEvent, now time.Time) (bool, error) {
	expiresAt, authCommand, err := EmailCommandExpiresAt(job)
	if err != nil || !authCommand {
		return false, err
	}
	return !now.UTC().Before(expiresAt), nil
}
