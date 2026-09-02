package model

import "time"

type AccountEmailSourceType string

const (
	AccountEmailSourceTypeIdentityCurrent AccountEmailSourceType = "identity_current"
	AccountEmailSourceTypeEmailCode       AccountEmailSourceType = "email_code"
	AccountEmailSourceTypeOIDCProvider    AccountEmailSourceType = "oidc_provider"
)

type AccountProvider struct {
	Provider   string
	Identifier string
}

type AccountEmailSource struct {
	SourceType      AccountEmailSourceType
	Provider        *string
	ProviderSubject *string
}

type AccountEmailCandidate struct {
	Email             string
	NormalizedEmail   string
	Current           bool
	IdentityVerified  bool
	EffectiveTrusted  bool
	UsableForDelivery bool
	Sources           []AccountEmailSource
}

type AccountSecurity struct {
	Providers       []AccountProvider
	EmailCandidates []AccountEmailCandidate
}

type AccountBanDetails struct {
	MetadataBanned bool
	IdentityState  string
	InactiveState  bool
	Reason         *string
	ExpiresAt      *time.Time
}

// UserDeletionRequest represents the user_deletion_request table.
// Used for GDPR account deletion workflow.
type UserDeletionRequest struct {
	ID                          string     `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	MemberID                    string     `gorm:"column:member_id;type:uuid;not null"`
	IdentityID                  string     `gorm:"column:identity_id;type:uuid;not null"`
	Token                       string     `gorm:"column:token;type:varchar(255);uniqueIndex;not null"`
	TokenExpiresAt              time.Time  `gorm:"column:token_expires_at;type:timestamptz;not null"`
	ConfirmedAt                 *time.Time `gorm:"column:confirmed_at;type:timestamptz"`
	ScheduledAt                 *time.Time `gorm:"column:scheduled_at;type:timestamptz"`
	LifecycleState              string     `gorm:"column:lifecycle_state;type:varchar(40);not null;default:'confirmation_pending'"`
	NotificationEmail           *string    `gorm:"column:notification_email;type:text"`
	NotificationEmailVerifiedAt *time.Time `gorm:"column:notification_email_verified_at;type:timestamptz"`
	NotificationName            *string    `gorm:"column:notification_name;type:text"`
	NotificationLocale          *string    `gorm:"column:notification_locale;type:text"`
	CreatedAt                   time.Time  `gorm:"column:created_at;type:timestamptz;not null;default:now()"`
	UpdatedAt                   time.Time  `gorm:"column:updated_at;type:timestamptz;not null;default:now()"`
}

// TableName returns the table name for GORM
func (UserDeletionRequest) TableName() string {
	return "user_deletion_request"
}
