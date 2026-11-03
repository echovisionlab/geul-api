package campaignpostgres

import (
	"context"
	"time"

	campaigndomain "github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type Publisher interface {
	EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error
}
type Routes interface {
	EnsureResourceRouteAvailableInTx(context.Context, *gorm.DB, string, string, string) error
}
type CampaignEmailAuthoringPort interface {
	LockLayoutsForCampaign(
		context.Context,
		*gorm.DB,
		...string,
	) (map[string]campaigndomain.CampaignLayoutReference, error)
	LoadLayoutSnapshot(
		context.Context,
		*gorm.DB,
		string,
	) ([]campaigndomain.CampaignLayoutLocaleSnapshot, error)
}
type CampaignEmailRenderingPort interface {
	CheckTestSendReadiness(context.Context, *gorm.DB, string) error
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
}
type LegalNoticeDeliveryPort interface {
	PrepareAutomaticPreviewShareLink(
		context.Context,
		*gorm.DB,
		campaigndomain.DeliveryRun,
		time.Time,
	) error
}
type CampaignCriteriaTargetPort interface {
	LockTarget(
		context.Context,
		*gorm.DB,
		string,
	) (campaigndomain.CampaignCriteriaTarget, error)
}
type ContributorMembers interface {
	// LockMembers takes a KEY SHARE lock on each existing Member of
	// memberIDs and returns the IDs it locked.
	LockMembers(ctx context.Context, tx *gorm.DB, memberIDs []string) ([]string, error)
}
type Ports struct {
	Publisher       Publisher
	Routes          Routes
	ContentBlocks   *contentblock.Store
	EmailAuthoring  CampaignEmailAuthoringPort
	EmailRendering  CampaignEmailRenderingPort
	CriteriaTargets CampaignCriteriaTargetPort
	Members         ContributorMembers
	Checkpoints     persistencecheckpoint.ContributorFence
	SpiceDB         RecipientAuthorizer
	LegalNotice     LegalNoticeDeliveryPort
}
