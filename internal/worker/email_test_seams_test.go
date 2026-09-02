package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (h *Handlers) buildMaterializedCampaignEmailJob(
	delivery campaign.CampaignDeliveryRecipientJob,
) (*managev1.SendEmailEvent, bool) {
	return h.bulkCampaignStore().BuildCommand(delivery)
}

func acceptedProviderMessageID(result *email.SendResult) (string, error) {
	if result == nil {
		return "", fmt.Errorf("mail adapter returned nil send result")
	}
	messageID := strings.TrimSpace(result.MessageID)
	if messageID == "" {
		return "", fmt.Errorf("mail adapter returned no provider message id")
	}
	return messageID, nil
}

func (h *Handlers) resolveEmailTargetLocale(ctx context.Context, job *managev1.SendEmailEvent) string {
	if job == nil {
		return ""
	}
	if locale := normalizeEmailTargetLocale(job.Locale); locale != "" {
		return locale
	}
	return h.lookupRecipientLocale(ctx, job.Recipient)
}

func (h *Handlers) lookupRecipientLocale(ctx context.Context, recipient string) string {
	return emaildelivery.LookupEmailRecipientLocale(ctx, h.db, strings.TrimSpace(recipient))
}

func normalizeEmailTargetLocale(locale *string) string {
	if locale == nil {
		return ""
	}
	if normalized := localization.NormalizeSupportedLocale(strings.TrimSpace(*locale)); normalized != nil {
		return *normalized
	}
	return ""
}

type emailAdapterFailure struct {
	err       error
	errorType string
}

type emailAdapterOutcome struct {
	accepted          bool
	providerMessageID string
	failureStatus     string
	failureErrorType  string
	failureErr        error
}

func summarizeEmailAdapterOutcome(
	providerMessageID string,
	failures []emailAdapterFailure,
	fallback error,
) emailAdapterOutcome {
	providerMessageID = strings.TrimSpace(providerMessageID)
	outcome := emailAdapterOutcome{
		accepted: providerMessageID != "", providerMessageID: providerMessageID,
	}
	if outcome.accepted || (fallback == nil && len(failures) == 0) {
		return outcome
	}
	outcome.failureStatus, outcome.failureErrorType, outcome.failureErr =
		classifyEmailDeliveryFailure(failures, fallback)
	return outcome
}

func classifyEmailDeliveryFailure(
	failures []emailAdapterFailure,
	fallback error,
) (string, string, error) {
	domainFailures := make([]emaildelivery.ProviderFailure, 0, len(failures))
	for _, failure := range failures {
		domainFailures = append(domainFailures, emaildelivery.ProviderFailure{
			Err: failure.err, ErrorType: failure.errorType,
		})
	}
	return emaildelivery.ClassifyProviderFailures(domainFailures, fallback)
}

func deliveryStatusForEmailError(errorType string) string {
	return emaildelivery.StatusForProviderError(errorType)
}

func emailDeliveryErrorDecision(err error) (string, bool) {
	return emaildelivery.ProviderErrorDecision(err)
}

func (h *Handlers) enforceEmailRecipientContext(
	ctx context.Context,
	job *managev1.SendEmailEvent,
) (bool, error) {
	decision, err := emaildeliveryadapter.NewRecipientPolicy(h.db, h.kratosClient).Authorize(ctx, job)
	if err != nil || !decision.Blocked {
		return false, err
	}
	if err := campaign.MarkCampaignDeliveryRecipientResultWithAudit(
		ctx, h.db, h.auditWriter, job.GetDeliveryRecipientId(),
		campaign.CampaignDeliveryRecipientStatusBlocked, "", decision.Reason, h.mailMetrics,
	); err != nil {
		return false, fmt.Errorf("persist recipient context block: %w", err)
	}
	logEmailLifecycle(
		ctx, slog.LevelWarn, "Email blocked by recipient context gate",
		"mail.delivery.blocked", job,
		durableEmailMessageID(job.GetDeliveryRecipientId(), job.GetMessageId(), job.GetReferenceId()),
		"blocked", decision.Reason, "recipient_context_blocked",
	)
	return true, nil
}

func durableEmailMessageID(deliveryRecipientID string, referenceIDs ...string) string {
	return emaildelivery.DurableMessageID(deliveryRecipientID, referenceIDs...)
}

func logEmailLifecycle(
	ctx context.Context,
	level slog.Level,
	message string,
	event string,
	job *managev1.SendEmailEvent,
	logicalMessageID string,
	outcome string,
	reason string,
	errorType string,
	extra ...structured.Value,
) {
	attrs := structured.Values{"domain", "mail", "event", event, "outcome", strings.TrimSpace(outcome)}
	if job != nil {
		attrs = append(attrs,
			"command_id", strings.TrimSpace(job.GetMessageId()),
			"template_type", strings.TrimSpace(job.GetTemplateType()),
		)
	}
	if logicalMessageID = strings.TrimSpace(logicalMessageID); logicalMessageID != "" {
		attrs = append(attrs, "logical_message_id", logicalMessageID)
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		attrs = append(attrs, "reason", reason)
	}
	if errorType = strings.TrimSpace(errorType); errorType != "" {
		attrs = append(attrs, "error_type", errorType)
	}
	attrs = append(attrs, safeMailLogAttrs(extra)...)
	slog.Log(ctx, level, message, attrs...)
}

func safeMailLogAttrs(attrs structured.Values) structured.Values {
	safe := make(structured.Values, 0, len(attrs))
	for index := 0; index+1 < len(attrs); index += 2 {
		key, ok := attrs[index].(string)
		if !ok {
			continue
		}
		switch key {
		case "recipient", "to", "from", "subject", "html", "text", "body", "token", "error":
			continue
		default:
			safe = append(safe, key, attrs[index+1])
		}
	}
	return safe
}

func (h *Handlers) renderEmailJobTemplate(
	ctx context.Context,
	job *managev1.SendEmailEvent,
	targetLocale string,
	templateData map[string]string,
) (*email.RenderedEmail, error) {
	return h.emailDeliveryRenderer().RenderResolved(ctx, job, targetLocale, templateData)
}

func (m mailMetrics) recordSendAttempt(ctx context.Context, templateType string) {
	m.RecordSendAttempt(ctx, templateType)
}

func (m mailMetrics) recordSendResult(ctx context.Context, templateType string, result string) {
	m.RecordSendResult(ctx, templateType, result)
}

func (m mailMetrics) recordRecipientStatus(ctx context.Context, templateType string, status string) {
	m.RecordRecipientStatus(ctx, templateType, status)
}

func mailTemplateClass(templateType string) string {
	return emaildeliveryadapter.TemplateClass(templateType)
}
