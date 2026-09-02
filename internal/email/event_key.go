package email

// EventKey represents a system email event identifier.
// Templates are matched by event_key in the email_template table.
type EventKey string

const (
	// Account Management
	EventAccountDeletionConfirm   EventKey = "account_deletion_confirm"
	EventAccountDeletionScheduled EventKey = "account_deletion_scheduled"
	EventAccountDeletionCancelled EventKey = "account_deletion_cancelled"
	EventAccountDeletionComplete  EventKey = "account_deletion_complete"
	EventAccountRecoveryConfirm   EventKey = "account_recovery_confirm"
	EventAccountRecoveryComplete  EventKey = "account_recovery_complete"
	EventPrimaryEmailChanged      EventKey = "primary_email_changed"
	EventEmailAdded               EventKey = "email_added"
	EventEmailRemoved             EventKey = "email_removed"
	EventPasskeyAdded             EventKey = "passkey_added"
	EventPasskeyRemoved           EventKey = "passkey_removed"
	EventSocialLoginAdded         EventKey = "social_login_added"
	EventSocialLoginRemoved       EventKey = "social_login_removed"
	EventWelcome                  EventKey = "welcome"

	// Terms & Policy
	EventTermsUpdate      EventKey = "terms_update"
	EventTermsEffective   EventKey = "terms_effective"
	EventPrivacyUpdate    EventKey = "privacy_update"
	EventPrivacyEffective EventKey = "privacy_effective"

	// Authentication
	EventVerificationCode EventKey = "verification_code"
	EventLoginCode        EventKey = "login_code"
	EventRegistrationCode EventKey = "registration_code"
)

func (e EventKey) String() string {
	return string(e)
}
