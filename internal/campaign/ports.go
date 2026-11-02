package campaign

import (
	"context"
	"time"
)

type CampaignLayoutReference struct {
	ID        string
	UpdatedAt time.Time
}

type CampaignLayoutLocaleSnapshot struct {
	Locale         string
	HTMLContent    string
	IsSourceLocale bool
}

type CampaignDeliveryMetrics interface {
	RecordRunDuration(context.Context, DeliveryRun, time.Time)
}

// CampaignAudienceTarget is the validated Audience-owned target snapshot that
// Campaign needs while sealing an immutable delivery definition.
type CampaignAudienceTarget struct {
	Archived          bool
	Valid             bool
	SegmentType       AudienceSegmentType
	CreatedAfter      *time.Time
	CreatedBefore     *time.Time
	MemberTagIDs      []string
	AccountRoles      []string
	ExcludedMemberIDs []string
}

// CampaignDeliveryRunDefinition is the complete Campaign-owned immutable
// definition passed to delivery persistence for sealing.
type CampaignDeliveryRunDefinition struct {
	CampaignID              string
	ScheduledAt             time.Time
	SnapshotSchemaVersion   int16
	Snapshot                CampaignDeliverySnapshot
	SourceLayoutID          *string
	AudienceSegmentID       *string
	SourceCampaignUpdatedAt time.Time
	SourceLayoutUpdatedAt   *time.Time
	Target                  CampaignDeliveryTarget
}

// CampaignDeliveryRunRef is the delivery identity Campaign retains after the
// immutable definition has been sealed.
type CampaignDeliveryRunRef struct {
	ID string
}

type CampaignDeliveryStats struct {
	TotalSent  int32
	Skipped    int32
	Failed     int32
	Blocked    int32
	Suppressed int32
}

type CampaignDeliveryRecipient struct {
	Email      string
	Status     string
	MemberID   string
	ErrorType  *string
	TerminalAt *time.Time
}

type CampaignDeliveryRecipientPage struct {
	Recipients []CampaignDeliveryRecipient
	Total      int64
}

type LocaleNormalizer interface{ NormalizeSupportedLocale(string) *string }
type Runtime interface {
	LocaleNormalizer
	AppendCampaignRecipientsAccess(context.Context, string) error
}
