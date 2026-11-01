package campaign

import (
	"time"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

const (
	CampaignTargetModeAll     = "all"
	CampaignTargetModeSegment = "segment"
)

type CampaignStatus string

const (
	CampaignStatusDraft     CampaignStatus = "CAMPAIGN_STATUS_DRAFT"
	CampaignStatusScheduled CampaignStatus = "CAMPAIGN_STATUS_SCHEDULED"
	CampaignStatusSending   CampaignStatus = "CAMPAIGN_STATUS_SENDING"
	CampaignStatusSent      CampaignStatus = "CAMPAIGN_STATUS_SENT"
	CampaignStatusFailed    CampaignStatus = "CAMPAIGN_STATUS_FAILED"
)

type Campaign struct {
	ID                string
	ContentDocumentID *string
	SourceLocale      string
	Name              string
	Subject           string
	ContentHTML       *string
	Status            CampaignStatus
	TargetMode        string
	SegmentID         *string
	LayoutID          *string
	ScheduledAt       *time.Time
	SentAt            *time.Time
	SentCount         int
	RecipientScope    string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type DeliveryRun struct {
	ID                      string
	RunKind                 string
	CampaignID              *string
	TermsID                 *string
	PrivacyID               *string
	Status                  string
	ScheduledAt             time.Time
	StartedAt               *time.Time
	CompletedAt             *time.Time
	TemplateEventKey        *string
	TemplateData            map[string]any
	RenderSnapshot          map[string]any
	SnapshotSchemaVersion   int16
	DefinitionSealed        bool
	SourceTemplateID        *string
	SourceLayoutID          *string
	AudienceSegmentID       *string
	SourceCampaignUpdatedAt *time.Time
	SourceTemplateUpdatedAt *time.Time
	SourceLayoutUpdatedAt   *time.Time
	SourceTermsVersion      *int32
	SourcePrivacyVersion    *int32
	TargetQueryVersion      int16
	TargetMode              string
	TargetRecipientScope    string
	TargetCreatedAfter      *time.Time
	TargetCreatedBefore     *time.Time
	TargetCount             int
	SentCount               int
	SkippedCount            int
	FailedCount             int
	BlockedCount            int
	SuppressedCount         int
	LastError               *string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type DeliveryRecipient struct {
	ID                       string
	RunID                    string
	RecipientEmail           string
	NormalizedRecipientEmail string
	IdentityID               *string
	MemberID                 *string
	Locale                   *string
	RecipientContextType     string
	Status                   string
	ErrorType                *string
	ProviderMessageID        *string
	TerminalAt               *time.Time
	DeliveryClaimID          *string
	DeliveryClaimExpiresAt   *time.Time
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// CampaignDocument is the editor projection of a Campaign.
type CampaignDocument struct {
	Campaign
	Document         *contentv1.RichTextDocument
	DocumentRevision string
}

type Pagination struct {
	Total, Limit, Offset int32
	HasMore              bool
}

type CollaborationPrincipal struct{ SessionID string }

type TargetMode int32

const (
	TargetModeAll     TargetMode = 1
	TargetModeSegment TargetMode = 2
)

type RecipientScope int32

const (
	RecipientScopeSubscribedUsers  RecipientScope = 1
	RecipientScopeAllMatchingUsers RecipientScope = 2
)

type CampaignStats struct{ TotalSent, TotalSkipped, TotalFailed, TotalBlocked, TotalSuppressed int32 }

type CampaignLocaleMetadata struct {
	Locale  string
	Subject *string
}
type CampaignRenderLocale struct {
	Locale         string
	Subject        *string
	IsSourceLocale bool
}
