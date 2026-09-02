//go:build integration

package legal_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type failingLegalNoticeBulkPublisher struct {
	calls              int
	transactionalCalls int
}

func (*failingLegalNoticeBulkPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (*failingLegalNoticeBulkPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (p *failingLegalNoticeBulkPublisher) EnqueueProtobufWithExecutor(_ context.Context, executor eventpkg.DBTX, _ string, _ string, _ proto.Message) error {
	if executor == nil {
		return errors.New("transactional executor is required")
	}
	p.transactionalCalls++
	return nil
}

func (p *failingLegalNoticeBulkPublisher) PublishSendBulkEmail(
	_ context.Context,
	_ *managev1.SendBulkEmailBatchEvent,
) error {
	p.calls++
	return errors.New("test publish failure")
}

func TestScheduledLegalNoticeCreatesOneAutomaticPreviewAndSentNoticeBlocksCancelIntegration(
	t *testing.T,
) {
	db := newLegalIntegrationDB(t)
	ensureBulkEmailAudienceKratosIdentityColumns(t, db)
	seedLegalDeliveryTemplateIntegration(t, db, email.EventTermsUpdate.String())
	now := time.Now().UTC()
	seedBulkEmailAudienceIdentity(
		t,
		db,
		"legal-preview-"+uuid.NewString()+"@example.test",
		now,
	)
	ctx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
	store, err := contentblock.NewGeneratedStore(filemedia.NewContentBlockFileReuseAuthorizer(spiceDB))
	require.NoError(t, err)
	service := newTermsServiceForLegalIntegrationTest(
		db,
		"https://example.test",
		"https://cdn.example.test",
		spiceDB,
		legaldomain.WithTermsContentBlockStore(store),
	)
	draft, err := service.CreateTermsVersion(
		ctx,
		connect.NewRequest(&managev1.CreateTermsVersionRequest{
			Title:    ptrString("September contributor terms"),
			Document: legalPolicyDocumentFixture("en", "September contributor terms body"),
		}),
	)
	require.NoError(t, err)
	effectiveAt := now.Add(6 * 24 * time.Hour)
	_, err = service.ScheduleTerms(
		ctx,
		connect.NewRequest(&managev1.ScheduleTermsRequest{
			Id:               draft.Msg.Id,
			ExpectedRevision: draft.Msg.Revision,
			EffectiveFrom:    timestamppb.New(effectiveAt),
		}),
	)
	require.NoError(t, err)

	var run model.CampaignDeliveryRun
	require.NoError(t, db.Where(
		"terms_id = ? AND template_event_key = ?",
		draft.Msg.Id,
		email.EventTermsUpdate.String(),
	).Take(&run).Error)
	require.WithinDuration(t, now, run.ScheduledAt, time.Second)
	data, err := campaign.CampaignDeliveryRunTemplateData(run)
	require.NoError(t, err)
	require.Equal(t, "September contributor terms", data["policy_title"])
	require.Equal(t, effectiveAt.Format("2006-01-02"), data["effective_date"])
	automaticToken := legalNoticePreviewToken(t, data["preview_url"])
	require.Zero(t, countAutomaticLegalPreviewLinks(t, db, automaticToken))

	failingPublisher := &failingLegalNoticeBulkPublisher{}
	err = campaign.NewAuditedCampaignDeliveryDispatcher(
		db,
		spiceDB,
		failingPublisher,
		noopDomainAuditAppender{},
		campaign.WithLegalNoticeDeliveryPort(legaladapter.NewRuntime()),
	).
		DispatchEmailDeliveryRun(t.Context(), run.ID)
	require.NoError(t, err)
	require.Zero(t, failingPublisher.calls)
	require.Equal(t, int64(1), countAutomaticLegalPreviewLinks(t, db, automaticToken))

	retryPublisher := &recordingLegalEmailPublisher{}
	resumed, err := campaign.NewAuditedCampaignDeliveryDispatcher(
		db,
		spiceDB,
		retryPublisher,
		noopDomainAuditAppender{},
		campaign.WithLegalNoticeDeliveryPort(legaladapter.NewRuntime()),
	).
		ResumeActiveEmailDeliveryRuns(t.Context(), 25)
	require.NoError(t, err)
	require.Equal(t, 1, resumed)
	require.Len(t, retryPublisher.sendBulkJobs, 1)
	require.Equal(t, int64(1), countAutomaticLegalPreviewLinks(t, db, automaticToken))

	manualToken := "manual-" + uuid.NewString()
	manualPasswordHash, err := crypto.NewPasswordHasher(nil).Hash("manual-password")
	require.NoError(t, err)
	manualExpiry := effectiveAt.Add(-time.Hour)
	require.NoError(t, db.Create(&model.ShareLink{
		Token:        manualToken,
		EntityType:   managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS.String(),
		EntityID:     draft.Msg.Id,
		PasswordHash: &manualPasswordHash,
		ExpiresAt:    &manualExpiry,
		CreatedAt:    now,
	}).Error)
	require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).
		Where("id = ?", run.ID).
		Updates(structured.Fields{
			"status":       legaldomain.CampaignDeliveryRunStatusSent,
			"completed_at": time.Now().UTC(),
		}).Error)

	_, err = service.CancelTermsSchedule(
		ctx,
		connect.NewRequest(&managev1.CancelTermsScheduleRequest{Id: draft.Msg.Id}),
	)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Equal(t, int64(1), countAutomaticLegalPreviewLinks(t, db, automaticToken))
	requireRelationWhereCount(t, db, "share_link", "token = ?", 1, manualToken)
	requireRelationWhereCount(
		t,
		db,
		"terms_history",
		"id = ? AND status = ?",
		1,
		draft.Msg.Id,
		managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(),
	)
}

func countAutomaticLegalPreviewLinks(t *testing.T, db *gorm.DB, token string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&model.ShareLink{}).Where("token = ?", token).Count(&count).Error)
	return count
}

func legalNoticePreviewToken(t *testing.T, value string) string {
	t.Helper()
	const marker = "/s/"
	index := strings.LastIndex(value, marker)
	require.GreaterOrEqual(t, index, 0)
	token := value[index+len(marker):]
	require.NotEmpty(t, token)
	require.NotContains(t, token, "/")
	return token
}
