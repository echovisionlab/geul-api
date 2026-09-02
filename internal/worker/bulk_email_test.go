package worker

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/crypto"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/google/uuid"
)

const bulkEmailPayloadUnitTokenSecret = "bulk-email-payload-unit-secret-at-least-32-bytes"

func TestHandleSendBulkEmailBatchRejectsEventWithoutDeliveryRunID(t *testing.T) {
	publisher := &bulkEmailUnitPublisher{}
	handlers := &Handlers{
		config: &config.Config{
			SiteOrigin:         "https://www.example.test",
			TokenSigningSecret: bulkEmailPayloadUnitTokenSecret,
		},
		publisher: publisher,
	}

	job := &managev1.SendBulkEmailBatchEvent{
		BatchSize:     1,
		RatePerSecond: 1000,
	}

	require.ErrorContains(
		t,
		handlers.handleSendBulkEmailBatch(context.Background(), job),
		"delivery_run_id is required",
	)
	require.Empty(t, publisher.publishedEvents)
}

func TestBuildMaterializedCampaignEmailJobBuildsRecipientContexts(t *testing.T) {
	campaignID := "campaign-1"
	templateType := "campaign:" + campaignID
	locale := "ko"
	identityID := uuid.NewString()
	memberID := uuid.NewString()

	tests := []struct {
		name      string
		recipient model.CampaignDeliveryRecipient
		assert    func(t *testing.T, job *managev1.SendEmailEvent)
	}{
		{
			name:      "newsletter subscription context",
			recipient: materializedBulkEmailRecipient("recipient-newsletter", "member@example.com", &locale, &memberID, &identityID, campaign.BulkEmailContextNewsletterSubscription),
			assert: func(t *testing.T, job *managev1.SendEmailEvent) {
				require.NotNil(t, job.GetNewsletterSubscription())
				require.Equal(t, memberID, job.GetNewsletterSubscription().GetMemberId())
				requireBulkEmailPayloadUnitUnsubscribeLink(t, job.TemplateData["unsubscribe_link"], identityID)
			},
		},
		{
			name:      "account current context",
			recipient: materializedBulkEmailRecipient("recipient-account", "account@example.com", &locale, &memberID, &identityID, campaign.BulkEmailContextAccountCurrent),
			assert: func(t *testing.T, job *managev1.SendEmailEvent) {
				require.NotNil(t, job.GetAccountSelectedPrimaryEmail())
				require.Equal(t, identityID, job.GetAccountSelectedPrimaryEmail().GetIdentityId())
				requireBulkEmailPayloadUnitUnsubscribeLink(t, job.TemplateData["unsubscribe_link"], identityID)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handlers := &Handlers{
				config: &config.Config{
					SiteOrigin:         "https://www.example.test",
					TokenSigningSecret: bulkEmailPayloadUnitTokenSecret,
				},
			}

			job, ok := handlers.buildMaterializedCampaignEmailJob(campaign.CampaignDeliveryRecipientJob{
				Recipient: tt.recipient,
				Run: model.CampaignDeliveryRun{
					ID:           "run-1",
					RunKind:      campaign.EmailDeliveryRunKindCampaign,
					CampaignID:   &campaignID,
					TemplateData: model.JSONFields{},
				},
			})

			require.True(t, ok)
			require.Equal(t, tt.recipient.RecipientEmail, job.GetRecipient())
			require.Equal(t, templateType, job.GetTemplateType())
			require.Equal(t, campaignID, job.GetReferenceId())
			require.Equal(t, tt.recipient.ID, job.GetDeliveryRecipientId())
			require.Equal(t, tt.recipient.ID, job.GetMessageId())
			require.Equal(t, locale, job.GetLocale())
			require.Equal(t, tt.recipient.RecipientEmail, job.TemplateData["recipient_email"])
			tt.assert(t, job)
		})
	}
}

func TestBuildMaterializedCampaignEmailJobRejectsMissingRecipientContext(t *testing.T) {
	blank := " "
	identityID := "identity-without-context"

	tests := []struct {
		name      string
		recipient model.CampaignDeliveryRecipient
	}{
		{
			name:      "newsletter subscription context without identity id",
			recipient: materializedBulkEmailRecipient("recipient-newsletter-missing", "member@example.com", nil, nil, nil, campaign.BulkEmailContextNewsletterSubscription),
		},
		{
			name:      "account current context with blank identity id",
			recipient: materializedBulkEmailRecipient("recipient-account-blank", "account@example.com", nil, &identityID, &blank, campaign.BulkEmailContextAccountCurrent),
		},
		{
			name:      "identity id without explicit context",
			recipient: materializedBulkEmailRecipient("recipient-identity-no-context", "identity@example.com", nil, &identityID, &identityID, ""),
		},
		{
			name:      "no context identifiers",
			recipient: materializedBulkEmailRecipient("recipient-no-context", "missing@example.com", nil, nil, nil, ""),
		},
	}

	handlers := &Handlers{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job, ok := handlers.buildMaterializedCampaignEmailJob(campaign.CampaignDeliveryRecipientJob{
				Recipient: tt.recipient,
				Run: model.CampaignDeliveryRun{
					ID:               "run-missing",
					RunKind:          campaign.EmailDeliveryRunKindLegalNotice,
					TemplateEventKey: new("terms_update"),
					TemplateData: model.JSONFields{
						"policy_title":   "Contributor terms",
						"effective_date": "2026-07-30",
						"preview_url":    "https://example.test/terms",
					},
					TermsID: new("terms-1"),
				},
			})

			require.False(t, ok)
			require.Nil(t, job)
		})
	}
}

func TestBuildMaterializedCampaignEmailJobRejectsPersistedRecipientTemplateData(
	t *testing.T,
) {
	campaignID := "campaign-persisted-recipient-data"
	identityID := "identity-persisted-recipient-data"
	handlers := &Handlers{}
	job, ok := handlers.buildMaterializedCampaignEmailJob(
		campaign.CampaignDeliveryRecipientJob{
			Recipient: materializedBulkEmailRecipient(
				"recipient-persisted-recipient-data",
				"recipient@example.test",
				nil,
				&identityID,
				&identityID,
				campaign.BulkEmailContextNewsletterSubscription,
			),
			Run: model.CampaignDeliveryRun{
				ID:         "run-persisted-recipient-data",
				RunKind:    campaign.EmailDeliveryRunKindCampaign,
				CampaignID: &campaignID,
				TemplateData: model.JSONFields{
					"recipient_email": "persisted@example.test",
				},
			},
		},
	)
	require.False(t, ok)
	require.Nil(t, job)
}

func materializedBulkEmailRecipient(id string, email string, locale *string, memberID *string, identityID *string, contextType string) model.CampaignDeliveryRecipient {
	return model.CampaignDeliveryRecipient{
		ID:                       id,
		RunID:                    "run-1",
		RecipientEmail:           email,
		NormalizedRecipientEmail: emailutil.NormalizeAddressForDelivery(email),
		Locale:                   locale,
		MemberID:                 memberID,
		IdentityID:               identityID,
		RecipientContextType:     contextType,
		Status:                   campaign.CampaignDeliveryRecipientStatusPending,
	}
}

func requireBulkEmailPayloadUnitUnsubscribeLink(t *testing.T, unsubscribeLink string, wantID string) {
	t.Helper()
	parsed, err := url.Parse(unsubscribeLink)
	require.NoError(t, err)
	require.Equal(t, "https", parsed.Scheme)
	require.Equal(t, "www.example.test", parsed.Host)
	require.Equal(t, "/unsubscribe", parsed.Path)

	token := parsed.Query().Get("token")
	require.NotEmpty(t, token)
	signedToken, err := crypto.ValidateSignedToken(token, bulkEmailPayloadUnitTokenSecret)
	require.NoError(t, err)
	id, err := member.ValidateNewsletterUnsubscribeToken(token, bulkEmailPayloadUnitTokenSecret)
	require.NoError(t, err)
	require.Equal(t, wantID, id)
	require.Equal(t, crypto.PurposeUnsubscribe, signedToken.Purpose)
	require.True(t, signedToken.Expiry.IsZero())
}

type bulkEmailUnitPublisher struct {
	publishedEvents     []string
	sendEmailEvents     []*managev1.SendEmailEvent
	sendBulkEmailEvents []*managev1.SendBulkEmailBatchEvent
	fileDeleteErr       error
}

func (p *bulkEmailUnitPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (p *bulkEmailUnitPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (p *bulkEmailUnitPublisher) EnqueueProtobufWithExecutor(_ context.Context, executor eventpkg.DBTX, _ string, _ string, _ proto.Message) error {
	if executor == nil {
		return fmt.Errorf("transactional executor is required")
	}
	return nil
}

func (p *bulkEmailUnitPublisher) PublishFileDelete(context.Context, *managev1.FileDeleteEvent) error {
	p.publishedEvents = append(p.publishedEvents, "file_delete")
	return p.fileDeleteErr
}

func (p *bulkEmailUnitPublisher) PublishFileDeleteWithExecutor(_ context.Context, executor eventpkg.DBTX, _ *managev1.FileDeleteEvent) error {
	if executor == nil {
		return fmt.Errorf("transactional executor is required")
	}
	p.publishedEvents = append(p.publishedEvents, "file_delete")
	return p.fileDeleteErr
}

func (p *bulkEmailUnitPublisher) PublishMediaProcessingLifecycle(context.Context, *managev1.MediaProcessingLifecycleEvent) error {
	p.publishedEvents = append(p.publishedEvents, "media_processing_lifecycle")
	return nil
}

func (p *bulkEmailUnitPublisher) PublishSendBulkEmail(_ context.Context, event *managev1.SendBulkEmailBatchEvent) error {
	p.publishedEvents = append(p.publishedEvents, "send_bulk_email")
	p.sendBulkEmailEvents = append(p.sendBulkEmailEvents, event)
	return nil
}

func (p *bulkEmailUnitPublisher) PublishSendEmail(_ context.Context, event *managev1.SendEmailEvent) error {
	p.publishedEvents = append(p.publishedEvents, "send_email")
	p.sendEmailEvents = append(p.sendEmailEvents, event)
	return nil
}

func (p *bulkEmailUnitPublisher) PublishTranscodeCancel(context.Context, *managev1.TranscodeCancelEvent) error {
	p.publishedEvents = append(p.publishedEvents, "transcode_cancel")
	return nil
}

func (p *bulkEmailUnitPublisher) PublishWaveformGenerate(context.Context, *managev1.WaveformGenerateEvent) error {
	p.publishedEvents = append(p.publishedEvents, "waveform_generate")
	return nil
}
