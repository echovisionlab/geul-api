package emaildelivery

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/failurecause"

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
) (handleErr error) {
	defer func() {
		if handleErr != nil {
			slog.ErrorContext(ctx, "Bulk email delivery failed", "domain", "mail", "event", "mail.bulk.failed", "outcome", "failed", "run_id", job.GetDeliveryRunId(), failurecause.Attr(handleErr))
		}
	}()
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
		if err := a.dispatchBatch(ctx, runID, limiter, deliveries); err != nil {
			return err
		}
		afterID = deliveries[len(deliveries)-1].RecipientID
	}
}

// dispatchBatch publishes the send command of each pending delivery at the
// run's rate, and ends a suppressed delivery or one without recipient
// context in place.
func (a *BulkApplication) dispatchBatch(ctx context.Context, runID string, limiter *rate.Limiter, deliveries []BulkDelivery) error {
	emails := make([]string, 0, len(deliveries))
	for _, delivery := range deliveries {
		emails = append(emails, delivery.RecipientEmail)
	}
	suppressions, err := a.suppressions.Lookup(ctx, emails)
	if err != nil {
		return err
	}
	for _, delivery := range deliveries {
		if err := limiter.Wait(ctx); err != nil {
			return err
		}
		if suppression, found := suppressions[normalizeSuppressionAddress(delivery.RecipientEmail)]; found {
			reason := strings.TrimSpace(suppression.Reason)
			if reason == "" {
				reason = "email_suppressed"
			}
			if err := a.finishBulkDelivery(ctx, delivery, RecipientStatusSuppressed, reason); err != nil {
				return err
			}
			continue
		}
		if delivery.Command == nil || delivery.Command.GetRecipientContext() == nil {
			if err := a.finishBulkDelivery(ctx, delivery, RecipientStatusBlocked, "recipient_context_missing"); err != nil {
				return err
			}
			continue
		}
		if err := a.publisher.PublishSendEmail(ctx, delivery.Command); err != nil {
			LogDeliveryFailure(ctx, "mail.delivery.command_publish_failed", delivery.Command, err, "run_id", runID, "delivery_recipient_id", delivery.RecipientID)
			return err
		}
	}
	return nil
}

func (a *BulkApplication) finishBulkDelivery(ctx context.Context, delivery BulkDelivery, status, reason string) error {
	if err := a.campaign.MarkResult(ctx, delivery.RecipientID, status, reason); err != nil {
		return err
	}
	a.metrics.RecordRecipientStatus(ctx, delivery.TemplateType, status)
	return nil
}
