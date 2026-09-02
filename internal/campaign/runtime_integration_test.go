//go:build integration

package campaign

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
)

type campaignRuntimeFixture struct {
	publisher          TransactionalPublisher
	personalDataAccess *securityaccess.Recorder
}

func newCampaignRuntimeFixture(
	publisher TransactionalPublisher,
	securityWriter securityaccess.Appender,
) Runtime {
	fixture := &campaignRuntimeFixture{publisher: publisher}
	if securityWriter != nil {
		fixture.personalDataAccess = securityaccess.New(securityWriter)
	}
	return fixture
}

func (r *campaignRuntimeFixture) EnqueueProtobufWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	queue string,
	messageID string,
	message proto.Message,
) error {
	if r.publisher == nil {
		return nil
	}
	return r.publisher.EnqueueProtobufWithExecutor(ctx, executor, queue, messageID, message)
}

func (*campaignRuntimeFixture) EnsureResourceRouteAvailableInTx(
	ctx context.Context,
	tx *gorm.DB,
	entity string,
	namespace string,
	slug string,
) error {
	return routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, entity, namespace, slug)
}

func (*campaignRuntimeFixture) NormalizeSupportedLocale(value string) *string {
	return localization.NormalizeSupportedLocale(value)
}

func (r *campaignRuntimeFixture) AppendCampaignRecipientsAccess(ctx context.Context, campaignID string) error {
	if r.personalDataAccess == nil {
		return securityaccess.Unavailable()
	}
	return r.personalDataAccess.AppendCampaignRecipients(ctx, campaignID)
}
