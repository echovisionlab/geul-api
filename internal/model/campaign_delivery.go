package model

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

type CampaignDeliveryRun struct {
	ID                      string     `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	RunKind                 string     `gorm:"column:run_kind;type:varchar(50);not null"`
	CampaignID              *string    `gorm:"column:campaign_id;type:uuid"`
	TermsID                 *string    `gorm:"column:terms_id;type:uuid"`
	PrivacyID               *string    `gorm:"column:privacy_id;type:uuid"`
	Status                  string     `gorm:"column:status;type:varchar(50);not null"`
	ScheduledAt             time.Time  `gorm:"column:scheduled_at;type:timestamptz;not null"`
	StartedAt               *time.Time `gorm:"column:started_at;type:timestamptz"`
	CompletedAt             *time.Time `gorm:"column:completed_at;type:timestamptz"`
	TemplateEventKey        *string    `gorm:"column:template_event_key;type:varchar(255)"`
	TemplateData            JSONFields `gorm:"column:template_data;type:jsonb;not null"`
	RenderSnapshot          JSONFields `gorm:"column:render_snapshot;type:jsonb;not null"`
	SnapshotSchemaVersion   int16      `gorm:"column:snapshot_schema_version;not null"`
	DefinitionSealed        bool       `gorm:"column:definition_sealed;not null;default:false"`
	SourceTemplateID        *string    `gorm:"column:source_template_id;type:uuid"`
	SourceLayoutID          *string    `gorm:"column:source_layout_id;type:uuid"`
	AudienceSegmentID       *string    `gorm:"column:audience_segment_id;type:uuid"`
	SourceCampaignUpdatedAt *time.Time `gorm:"column:source_campaign_updated_at;type:timestamptz"`
	SourceTemplateUpdatedAt *time.Time `gorm:"column:source_template_updated_at;type:timestamptz"`
	SourceLayoutUpdatedAt   *time.Time `gorm:"column:source_layout_updated_at;type:timestamptz"`
	SourceTermsVersion      *int32     `gorm:"column:source_terms_version"`
	SourcePrivacyVersion    *int32     `gorm:"column:source_privacy_version"`
	TargetQueryVersion      int16      `gorm:"column:target_query_version;not null"`
	TargetMode              string     `gorm:"column:target_mode;type:varchar(40);not null"`
	TargetRecipientScope    string     `gorm:"column:target_recipient_scope;type:varchar(32);not null"`
	TargetCreatedAfter      *time.Time `gorm:"column:target_created_after;type:timestamptz"`
	TargetCreatedBefore     *time.Time `gorm:"column:target_created_before;type:timestamptz"`
	TargetCount             int        `gorm:"column:target_count;not null;default:0"`
	SentCount               int        `gorm:"column:sent_count;not null;default:0"`
	SkippedCount            int        `gorm:"column:skipped_count;not null;default:0"`
	FailedCount             int        `gorm:"column:failed_count;not null;default:0"`
	BlockedCount            int        `gorm:"column:blocked_count;not null;default:0"`
	SuppressedCount         int        `gorm:"column:suppressed_count;not null;default:0"`
	LastError               *string    `gorm:"column:last_error;type:text"`
	CreatedAt               time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt               time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

func (CampaignDeliveryRun) TableName() string {
	return "email_delivery_run"
}

func (r *CampaignDeliveryRun) BeforeCreate(_ *gorm.DB) error {
	if r.TemplateData == nil {
		r.TemplateData = JSONFields{}
	}
	if strings.TrimSpace(r.RunKind) == "" {
		return gorm.ErrInvalidData
	}
	if len(r.RenderSnapshot) == 0 {
		return gorm.ErrInvalidData
	}
	if r.SnapshotSchemaVersion == 0 {
		r.SnapshotSchemaVersion = 1
	}
	if r.SnapshotSchemaVersion != 1 {
		return gorm.ErrInvalidData
	}
	return nil
}

type CampaignDeliveryRecipient struct {
	ID                       string     `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	RunID                    string     `gorm:"column:run_id;type:uuid;not null"`
	RecipientEmail           string     `gorm:"column:recipient_email;type:varchar(255);not null"`
	NormalizedRecipientEmail string     `gorm:"column:normalized_recipient_email;type:varchar(255);not null"`
	IdentityID               *string    `gorm:"column:identity_id;type:uuid"`
	MemberID                 *string    `gorm:"column:member_id;type:uuid"`
	Locale                   *string    `gorm:"column:locale;type:varchar(20)"`
	RecipientContextType     string     `gorm:"column:recipient_context_type;type:varchar(50);not null"`
	Status                   string     `gorm:"column:status;type:varchar(50);not null;default:pending"`
	ErrorType                *string    `gorm:"column:error_type;type:varchar(100)"`
	ProviderMessageID        *string    `gorm:"column:provider_message_id;type:text"`
	TerminalAt               *time.Time `gorm:"column:terminal_at;type:timestamptz"`
	CreatedAt                time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt                time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

func (CampaignDeliveryRecipient) TableName() string {
	return "email_delivery_recipient"
}

func (r *CampaignDeliveryRecipient) BeforeCreate(_ *gorm.DB) error {
	if strings.TrimSpace(r.NormalizedRecipientEmail) == "" {
		r.NormalizedRecipientEmail = strings.ToLower(strings.TrimSpace(r.RecipientEmail))
	}
	switch r.RecipientContextType {
	case "newsletter_subscription", "account_current":
		if r.IdentityID == nil || r.MemberID == nil {
			return gorm.ErrInvalidData
		}
	default:
		return gorm.ErrInvalidData
	}
	if strings.TrimSpace(r.Status) == "" {
		r.Status = "pending"
	}
	return nil
}

type EmailDeliveryRunTargetUserTag struct {
	RunID     string `gorm:"column:run_id;type:uuid;primaryKey"`
	UserTagID string `gorm:"column:user_tag_id;type:uuid;primaryKey"`
}

func (EmailDeliveryRunTargetUserTag) TableName() string {
	return "email_delivery_run_target_user_tag"
}

type EmailDeliveryRunTargetUserRole struct {
	RunID string `gorm:"column:run_id;type:uuid;primaryKey"`
	Role  string `gorm:"column:role;type:varchar(16);primaryKey"`
}

func (EmailDeliveryRunTargetUserRole) TableName() string {
	return "email_delivery_run_target_user_role"
}

type EmailDeliveryRunTargetExcludedMember struct {
	RunID      string `gorm:"column:run_id;type:uuid;primaryKey"`
	MemberID   string `gorm:"column:member_id;type:uuid;primaryKey"`
	IdentityID string `gorm:"column:identity_id;type:uuid;not null"`
}

func (EmailDeliveryRunTargetExcludedMember) TableName() string {
	return "email_delivery_run_target_excluded_member"
}
