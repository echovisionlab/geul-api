package campaign

import (
	"context"

	"gorm.io/gorm"

	campaigndomain "github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
)

type TransactionalPublisher interface {
	EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error
}

// Runtime adapts queue, route, locale, and personal-data access telemetry to
// Campaign-owned ports.
type Runtime struct {
	publisher          TransactionalPublisher
	personalDataAccess *securityaccess.Recorder
}

func NewRuntime(publisher TransactionalPublisher, securityWriter securityaccess.Appender) *Runtime {
	if publisher == nil {
		panic("Campaign runtime publisher is required")
	}
	return &Runtime{
		publisher:          publisher,
		personalDataAccess: securityaccess.New(securityWriter),
	}
}

func (r *Runtime) EnqueueProtobufWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	queue string,
	messageID string,
	message proto.Message,
) error {
	return r.publisher.EnqueueProtobufWithExecutor(ctx, executor, queue, messageID, message)
}

func (*Runtime) EnsureResourceRouteAvailableInTx(
	ctx context.Context,
	tx *gorm.DB,
	entity string,
	namespace string,
	slug string,
) error {
	return routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, entity, namespace, slug)
}

func (*Runtime) NormalizeSupportedLocale(value string) *string {
	return localization.NormalizeSupportedLocale(value)
}

func (r *Runtime) AppendCampaignRecipientsAccess(ctx context.Context, campaignID string) error {
	if r.personalDataAccess == nil {
		return securityaccess.Unavailable()
	}
	return r.personalDataAccess.AppendCampaignRecipients(ctx, campaignID)
}

var _ campaigndomain.Runtime = (*Runtime)(nil)
