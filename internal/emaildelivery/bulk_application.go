package emaildelivery

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"golang.org/x/time/rate"
)

const (
	DefaultBulkBatchSize     = 100
	DefaultBulkRatePerSecond = 10
)

type BulkDelivery struct {
	RecipientID    string
	RecipientEmail string
	TemplateType   string
	Command        *managev1.SendEmailEvent
}

type BulkCampaignStore interface {
	Materialize(context.Context, string) error
	ListPending(context.Context, string, string, int) ([]BulkDelivery, error)
	MarkResult(context.Context, string, string, string) error
}

type BulkSuppressionStore interface {
	Lookup(context.Context, []string) (map[string]Suppression, error)
}

type EmailCommandPublisher interface {
	PublishSendEmail(context.Context, *managev1.SendEmailEvent) error
}

type BulkApplication struct {
	campaign     BulkCampaignStore
	suppressions BulkSuppressionStore
	publisher    EmailCommandPublisher
	metrics      DeliveryMetrics
}

func NewBulkApplication(
	campaign BulkCampaignStore,
	suppressions BulkSuppressionStore,
	publisher EmailCommandPublisher,
	metrics DeliveryMetrics,
) *BulkApplication {
	if campaign == nil || suppressions == nil || publisher == nil || metrics == nil {
		panic("bulk email application dependencies are required")
	}
	return &BulkApplication{
		campaign: campaign, suppressions: suppressions,
		publisher: publisher, metrics: metrics,
	}
}

func (a *BulkApplication) Handle(
	ctx context.Context,
	job *managev1.SendBulkEmailBatchEvent,
) error {
	if job == nil {
		return fmt.Errorf("bulk email batch is required")
	}
	batchSize := int(job.GetBatchSize())
	if batchSize <= 0 {
		batchSize = DefaultBulkBatchSize
	}
	ratePerSecond := job.GetRatePerSecond()
	if ratePerSecond <= 0 {
		ratePerSecond = DefaultBulkRatePerSecond
	}
	runID := strings.TrimSpace(job.GetDeliveryRunId())
	if runID == "" {
		return fmt.Errorf("delivery_run_id is required")
	}
	return a.Run(ctx, runID, batchSize, ratePerSecond)
}

func (a *BulkApplication) Run(
	ctx context.Context,
	runID string,
	batchSize int,
	ratePerSecond int32,
) error {
	if err := a.campaign.Materialize(ctx, runID); err != nil {
		return fmt.Errorf("materialize email delivery run: %w", err)
	}
	limiter := rate.NewLimiter(rate.Limit(ratePerSecond), 1)
	afterID := ""
	for {
		deliveries, err := a.campaign.ListPending(ctx, runID, afterID, batchSize)
		if err != nil {
			return err
		}
		if len(deliveries) == 0 {
			return nil
		}
		emails := make([]string, 0, len(deliveries))
		for _, delivery := range deliveries {
			emails = append(emails, delivery.RecipientEmail)
		}
		suppressions, err := a.suppressions.Lookup(ctx, emails)
		if err != nil {
			return err
		}
		for _, delivery := range deliveries {
			afterID = delivery.RecipientID
			if err := limiter.Wait(ctx); err != nil {
				return err
			}
			normalized := normalizeSuppressionAddress(delivery.RecipientEmail)
			if suppression, found := suppressions[normalized]; found {
				reason := strings.TrimSpace(suppression.Reason)
				if reason == "" {
					reason = "email_suppressed"
				}
				if err := a.campaign.MarkResult(ctx, delivery.RecipientID, RecipientStatusSuppressed, reason); err != nil {
					return err
				}
				a.metrics.RecordRecipientStatus(ctx, delivery.TemplateType, RecipientStatusSuppressed)
				continue
			}
			if delivery.Command == nil || delivery.Command.GetRecipientContext() == nil {
				if err := a.campaign.MarkResult(ctx, delivery.RecipientID, RecipientStatusBlocked, "recipient_context_missing"); err != nil {
					return err
				}
				a.metrics.RecordRecipientStatus(ctx, delivery.TemplateType, RecipientStatusBlocked)
				continue
			}
			if err := a.publisher.PublishSendEmail(ctx, delivery.Command); err != nil {
				slog.Error(
					"Failed to publish campaign recipient command",
					"domain", "mail", "event", "mail.delivery.command_publish_failed",
					"outcome", "failed", "run_id", runID,
					"delivery_recipient_id", delivery.RecipientID,
				)
				return err
			}
		}
	}
}
