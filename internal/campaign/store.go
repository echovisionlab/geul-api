package campaign

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"google.golang.org/protobuf/proto"
)

var (
	ErrNotFound         = errors.New("campaign store row not found")
	ErrCampaignNotFound = errors.New("campaign not found")
	ErrDurableReference = errors.New("campaign has a durable reference")
	ErrAudienceNotFound = errors.New("campaign audience not found")
	ErrDeliveryStarted  = errors.New("campaign delivery has already started")
)

type AudienceSegmentType string

const (
	AudienceSegmentAllMembers              AudienceSegmentType = "SEGMENT_TYPE_ALL_MEMBERS"
	AudienceSegmentMemberTags              AudienceSegmentType = "SEGMENT_TYPE_MEMBER_TAGS"
	AudienceSegmentMembersByFilter         AudienceSegmentType = "SEGMENT_TYPE_MEMBERS_BY_FILTER"
	BulkEmailContextAccountCurrent                             = "account_current"
	BulkEmailContextNewsletterSubscription                     = "newsletter_subscription"
)

type CampaignList struct {
	Filters       []*commonv1.FilterSpec
	Sorts         []*commonv1.SortSpec
	Limit, Offset int
}

type CampaignChange struct {
	Name          *string
	Configuration *CampaignConfiguration
	UpdatedAt     time.Time
}

type LockedDeliveryRun struct {
	Run      DeliveryRun
	Campaign *Campaign
}

type RecipientResult struct {
	RecipientID       string
	ClaimID           string
	Status            string
	ProviderMessageID string
	ErrorType         string
	RequireClaim      bool
}

// Store is bound to one transaction or read snapshot. Locking and content
// snapshot methods require a writable transaction or ReadLocked unit.
type Store interface {
	emailcontent.Store
	FindCampaign(context.Context, string) (Campaign, error)
	LockCampaign(context.Context, string) (Campaign, error)
	InsertCampaign(context.Context, *Campaign) error
	UpdateCampaign(context.Context, Campaign, CampaignChange) error
	SaveCampaign(context.Context, *Campaign) error
	UpdateCampaignStatus(context.Context, Campaign, CampaignStatus) error
	PrepareLegalNoticePreview(context.Context, DeliveryRun, time.Time) error
	DeleteCampaign(context.Context, Campaign) error
	ListCampaigns(context.Context, CampaignList) ([]Campaign, int64, error)
	CampaignLayoutID(context.Context, string) (*string, error)
	CampaignDocumentID(context.Context, string, bool) (*string, error)
	CampaignSourceLocale(context.Context, string, bool) (string, error)
	CampaignStatus(context.Context, string) (CampaignStatus, error)
	CampaignAIDocumentRoot(context.Context, string, emailcontent.RowLock) (Campaign, error)
	CampaignSourceLocales(context.Context, []string) (map[string]string, error)
	CampaignLocaleMetadata(context.Context, string) ([]CampaignLocaleMetadata, error)
	CampaignRenderLocales(context.Context, string) ([]CampaignRenderLocale, error)
	ActiveCampaignRunCount(context.Context, string) (int64, error)
	RenderableCampaignContentCount(context.Context, string) (int64, error)
	CampaignLocaleSubject(context.Context, string, string, bool) (*string, bool, error)
	InsertSourceSubject(context.Context, string, string, string, time.Time) error
	UpdateSourceSubject(context.Context, string, string, string, time.Time) error
	SetSourceSubject(context.Context, string, string, time.Time) error
	EnsureCampaignRoute(context.Context, string) error
	LockLayouts(context.Context, ...string) (map[string]CampaignLayoutReference, error)
	LayoutSnapshot(context.Context, string) ([]CampaignLayoutLocaleSnapshot, error)
	LockAudienceTarget(context.Context, string) (CampaignAudienceTarget, error)
	ResolveSessionPrincipal(context.Context, string) (*auth.UserInfo, error)
	LockMembers(context.Context, []string) ([]string, error)
	RequireCurrentContributors(context.Context, string, []string) error
	EmailRenderData(context.Context, string, string, string, map[string]string) map[string]string
	WrapWithLayout(context.Context, string, string, string, map[string]string) (string, error)
	CheckTestSendReadiness(context.Context, string) error
	Enqueue(context.Context, string, string, proto.Message) error
	CountRecipients(context.Context, CampaignDeliveryTarget) (int64, error)
	InsertSealedRun(context.Context, *DeliveryRun, *CampaignDeliveryTarget) error
	FindRun(context.Context, string) (DeliveryRun, error)
	LockRun(context.Context, string) (DeliveryRun, error)
	// LockDeliveryRun locks the Campaign before its run, and can skip locked rows.
	LockDeliveryRun(context.Context, string, bool) (LockedDeliveryRun, bool, error)
	SaveRun(context.Context, *DeliveryRun) error
	StartRun(context.Context, DeliveryRun, time.Time) error
	FailRun(context.Context, string, time.Time) error
	DispatchRun(context.Context, DeliveryRun, time.Time) error
	DispatchCampaign(context.Context, string, string) (bool, error)
	DueRuns(context.Context, time.Time, int) ([]DeliveryRun, error)
	ActiveRuns(context.Context, int) ([]DeliveryRun, error)
	LoadDeliveryTarget(context.Context, DeliveryRun) (CampaignDeliveryTarget, error)
	CancelActiveRuns(context.Context, string, time.Time) error
	HasHistory(context.Context, string) (bool, error)
	LatestStats(context.Context, string) (*CampaignDeliveryStats, error)
	ListRecipients(context.Context, string, int, int) (CampaignDeliveryRecipientPage, error)
	MaterializeRecipients(context.Context, DeliveryRun, CampaignDeliveryTarget) error
	RecipientCount(context.Context, string) (int64, error)
	SetTargetCount(context.Context, string, int) error
	PendingRecipients(context.Context, string, string, int, time.Time) ([]DeliveryRecipient, error)
	HasPendingRecipients(context.Context, string) (bool, error)
	FindRecipient(context.Context, string) (DeliveryRecipient, error)
	RecipientIdentity(context.Context, string) (DeliveryRecipient, error)
	RecipientStatus(context.Context, string) (string, error)
	LockReleaseRecipient(context.Context, string) (DeliveryRecipient, error)
	LockClaimRecipient(context.Context, string) (DeliveryRecipient, error)
	LockResultRecipient(context.Context, string) (DeliveryRecipient, error)
	ClaimRecipient(context.Context, DeliveryRecipient, string, time.Time, time.Time) (bool, error)
	ReleaseRecipientClaim(context.Context, DeliveryRecipient, string) error
	StoreRecipientResult(context.Context, RecipientResult, string, time.Time) error
	CompletionCounts(context.Context, string) (DeliveryCompletionCounts, error)
	ProviderOutcomeRunIDs(context.Context, string) ([]string, error)
	LockProviderRecipients(context.Context, string, string) ([]DeliveryRecipient, error)
	UpdateProviderRecipient(context.Context, DeliveryRecipient, string, time.Time, *string) (int, error)
	RefreshBusinessCounts(context.Context, string, DeliveryCompletionCounts) error
}

// Joined carries the Store and audit bound to a caller's transaction.
type Joined struct {
	Store Store
	Audit domainaudit.Appender
}

type Units = mutation.UnitOfWork[Store]
