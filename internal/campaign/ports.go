package campaign

import (
	"context"
	"time"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type LocaleNormalizer interface {
	NormalizeSupportedLocale(string) *string
}

// Runtime is Campaign's boundary to queue, route, locale, and security-access
// infrastructure. Concrete implementations live under internal/adapters/campaign.
type Runtime interface {
	EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error
	EnsureResourceRouteAvailableInTx(context.Context, *gorm.DB, string, string, string) error
	LocaleNormalizer
	AppendCampaignRecipientsAccess(context.Context, string) error
}

type CampaignLayoutReference struct {
	ID        string
	UpdatedAt time.Time
}

type CampaignLayoutLocaleSnapshot struct {
	Locale         string
	HTMLContent    string
	IsSourceLocale bool
}

// CampaignEmailAuthoringPort is Campaign's narrow read/lock dependency on
// Email Authoring. Campaign never mutates Template or Layout authoring state.
type CampaignEmailAuthoringPort interface {
	LockLayoutsForCampaign(
		context.Context,
		*gorm.DB,
		...string,
	) (map[string]CampaignLayoutReference, error)
	LoadLayoutSnapshot(
		context.Context,
		*gorm.DB,
		string,
	) ([]CampaignLayoutLocaleSnapshot, error)
}

// CampaignEmailRenderingPort owns site-default render data and Layout
// rendering. Campaign owns the selected content and locale.
type CampaignEmailRenderingPort interface {
	BuildRenderData(
		context.Context,
		*gorm.DB,
		string,
		string,
		string,
		map[string]string,
	) map[string]string
	WrapWithLayout(
		context.Context,
		*gorm.DB,
		string,
		string,
		string,
		map[string]string,
	) (string, error)
	RenderVariables(string, map[string]string) string
	NormalizeHTML(string) string
	PlainText(string) string
	TestRecipientContext(string) *managev1.SendEmailEvent_TestEmail
}

type LegalNoticeDeliveryPort interface {
	PrepareAutomaticPreviewShareLink(
		context.Context,
		*gorm.DB,
		model.CampaignDeliveryRun,
		time.Time,
	) error
}

type CampaignDeliveryMetrics interface {
	RecordRunDuration(context.Context, model.CampaignDeliveryRun, time.Time)
}

// CampaignAudienceTarget is the validated Audience-owned target snapshot that
// Campaign needs while sealing an immutable delivery definition.
type CampaignAudienceTarget struct {
	Archived          bool
	Valid             bool
	SegmentType       string
	CreatedAfter      *time.Time
	CreatedBefore     *time.Time
	MemberTagIDs      []string
	AccountRoles      []string
	ExcludedMemberIDs []string
}

// CampaignAudienceTargetPort owns the Audience row lock and normalized
// relation-backed configuration read. Campaign consumes only the immutable
// target facts required by delivery.
type CampaignAudienceTargetPort interface {
	LockTarget(
		context.Context,
		*gorm.DB,
		string,
	) (CampaignAudienceTarget, error)
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

// CampaignDeliveryPort owns generic delivery-run persistence, recipient
// selection execution, and delivery-history queries. Campaign supplies and
// validates the Campaign-specific immutable definition.
type CampaignDeliveryPort interface {
	CountRecipients(
		context.Context,
		*gorm.DB,
		CampaignDeliveryTarget,
	) (int64, error)
	SealRun(
		context.Context,
		*gorm.DB,
		CampaignDeliveryRunDefinition,
	) (CampaignDeliveryRunRef, error)
	StartRun(
		context.Context,
		*gorm.DB,
		string,
		int64,
		time.Time,
	) error
	CancelActiveRuns(
		context.Context,
		*gorm.DB,
		string,
		time.Time,
	) error
	HasHistory(context.Context, *gorm.DB, string) (bool, error)
	LatestStats(
		context.Context,
		*gorm.DB,
		string,
	) (*CampaignDeliveryStats, error)
	ListRecipients(
		context.Context,
		*gorm.DB,
		string,
		int,
		int,
	) (CampaignDeliveryRecipientPage, error)
}
