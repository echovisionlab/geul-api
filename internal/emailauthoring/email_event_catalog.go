package emailauthoring

type automaticEmailEventKey string

const (
	eventAccountDeletionConfirm   automaticEmailEventKey = "account_deletion_confirm"
	eventAccountDeletionScheduled automaticEmailEventKey = "account_deletion_scheduled"
	eventAccountDeletionCancelled automaticEmailEventKey = "account_deletion_cancelled"
	eventAccountDeletionComplete  automaticEmailEventKey = "account_deletion_complete"
	eventAccountRecoveryConfirm   automaticEmailEventKey = "account_recovery_confirm"
	eventAccountRecoveryComplete  automaticEmailEventKey = "account_recovery_complete"
	eventPrimaryEmailChanged      automaticEmailEventKey = "primary_email_changed"
	eventEmailAdded               automaticEmailEventKey = "email_added"
	eventEmailRemoved             automaticEmailEventKey = "email_removed"
	eventPasskeyAdded             automaticEmailEventKey = "passkey_added"
	eventPasskeyRemoved           automaticEmailEventKey = "passkey_removed"
	eventSocialLoginAdded         automaticEmailEventKey = "social_login_added"
	eventSocialLoginRemoved       automaticEmailEventKey = "social_login_removed"
	eventWelcome                  automaticEmailEventKey = "welcome"
	eventTermsUpdate              automaticEmailEventKey = "terms_update"
	eventTermsEffective           automaticEmailEventKey = "terms_effective"
	eventPrivacyUpdate            automaticEmailEventKey = "privacy_update"
	eventPrivacyEffective         automaticEmailEventKey = "privacy_effective"
	eventVerificationCode         automaticEmailEventKey = "verification_code"
	eventLoginCode                automaticEmailEventKey = "login_code"
	eventRegistrationCode         automaticEmailEventKey = "registration_code"
)

func (key automaticEmailEventKey) String() string { return string(key) }

func automaticEmailEventKeys() []automaticEmailEventKey {
	return []automaticEmailEventKey{
		eventAccountDeletionConfirm, eventAccountDeletionScheduled,
		eventAccountDeletionCancelled, eventAccountDeletionComplete,
		eventAccountRecoveryConfirm, eventAccountRecoveryComplete,
		eventPrimaryEmailChanged, eventEmailAdded, eventEmailRemoved,
		eventPasskeyAdded, eventPasskeyRemoved, eventSocialLoginAdded,
		eventSocialLoginRemoved, eventWelcome, eventTermsUpdate,
		eventTermsEffective, eventPrivacyUpdate, eventPrivacyEffective,
		eventVerificationCode, eventLoginCode, eventRegistrationCode,
	}
}
