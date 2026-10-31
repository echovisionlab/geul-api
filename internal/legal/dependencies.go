package legal

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

type CollaborationPermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
	CheckActorCan(context.Context, policyv1.Actor, policyv1.Can) (bool, error)
}

type CurrentRoute struct {
	ID    string
	Title string
}

// OG owns legal-route persistence and immutable generation snapshots.
type OG interface {
	LockActivation(context.Context, *gorm.DB, string) error
	CurrentForRoute(context.Context, *gorm.DB, string) (*CurrentRoute, error)
	RequestSaved(context.Context, *gorm.DB, string, string, string, bool, string) error
	RouteID(string) string
	ReleaseAssets(context.Context, *gorm.DB, string, string) error
	CancelAndRelease(context.Context, *gorm.DB, string, string) error
}

type OGDisposition uint8

const (
	OGUnavailable OGDisposition = iota
	OGPending
	OGReady
)

// PublicMedia owns public-route OG generation and asset projection concretes.
type PublicMedia interface {
	RouteID(string) string
	LocalizedOGDisposition(context.Context, *gorm.DB, string, string, string) (OGDisposition, error)
	ReadyLocalizedOGAsset(context.Context, *gorm.DB, string, string, string) (*commonv1.AssetRef, error)
}

// NoticeDelivery seals the required email run in the legal transaction and
// dispatches it only after the authoritative policy commit. Campaign owns the
// runs; the policy kind ("terms" or "privacy") names the version they
// reference.
type NoticeDelivery interface {
	CreateRun(context.Context, *gorm.DB, string, string, string, map[string]string, time.Time) (*model.CampaignDeliveryRun, error)
	DispatchRun(context.Context, string) error
	// LatestSealedVersion returns the highest version of kind a notice run
	// sealed, so a deleted version's number is never reused.
	LatestSealedVersion(ctx context.Context, tx *gorm.DB, kind string) (int, error)
	// CancelScheduledRuns cancels the version's scheduled notices and
	// rejects the cancellation once one started sending.
	CancelScheduledRuns(ctx context.Context, tx *gorm.DB, kind, policyID string) error
	// StopRunsForDeletedPolicy stops the deleted version's notices and keeps
	// their sealed definitions as history.
	StopRunsForDeletedPolicy(ctx context.Context, tx *gorm.DB, kind, policyID string) error
	// CancelUpdateNoticeAfterActivation cancels the still-scheduled update
	// notice of a version that became effective; it logs a failure.
	CancelUpdateNoticeAfterActivation(ctx context.Context, db *gorm.DB, kind, policyID string, now time.Time)
	// UpdateNoticeRuns returns the version's update notices with their
	// template data.
	UpdateNoticeRuns(ctx context.Context, tx *gorm.DB, kind, policyID string) ([]model.CampaignDeliveryRun, error)
}

// AutomaticPreview is the password-free preview link a scheduled update
// notice mails out; it expires when the version takes effect.
type AutomaticPreview struct {
	Token      string
	EntityType string
	EntityID   string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

// PreviewLinks writes the Share Links of policy versions, which the Share
// Link domain owns.
type PreviewLinks interface {
	CreateAutomaticPreview(ctx context.Context, tx *gorm.DB, preview AutomaticPreview) error
	DeleteAutomaticPreview(ctx context.Context, tx *gorm.DB, token, entityType, entityID string) error
	DeleteForEntity(ctx context.Context, tx *gorm.DB, entityType, entityID string) error
}

// MemberLocales reads the locale a Member prefers, which seeds the source
// locale of a version they create.
type MemberLocales interface {
	PreferredLocale(ctx context.Context, db *gorm.DB, memberID string) (*string, error)
}

type Dependencies struct {
	OG     OG
	Notice NoticeDelivery
}
