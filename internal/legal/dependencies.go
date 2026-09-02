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
// dispatches it only after the authoritative policy commit.
type NoticeDelivery interface {
	CreateRun(context.Context, *gorm.DB, string, string, string, map[string]string, time.Time) (*model.CampaignDeliveryRun, error)
	DispatchRun(context.Context, string) error
}

type Dependencies struct {
	OG     OG
	Notice NoticeDelivery
}
