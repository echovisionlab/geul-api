package emaildeliveryadapter

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

const (
	SESEventCallbackPath = "/callbacks/ses"
	maxSNSMessageBytes   = 256 << 10
	maxSNSMessageAge     = 70 * time.Minute
	maxSNSClockSkew      = 5 * time.Minute
)

type snsEnvelope struct {
	Type             string `json:"Type"`
	MessageID        string `json:"MessageId"`
	TopicARN         string `json:"TopicArn"`
	Subject          string `json:"Subject,omitempty"`
	Message          string `json:"Message"`
	Timestamp        string `json:"Timestamp"`
	SignatureVersion string `json:"SignatureVersion"`
	Signature        string `json:"Signature"`
	SigningCertURL   string `json:"SigningCertURL"`
	SubscribeURL     string `json:"SubscribeURL,omitempty"`
	Token            string `json:"Token,omitempty"`
}

type sesNotification struct {
	EventType        string  `json:"eventType"`
	NotificationType string  `json:"notificationType"`
	Mail             sesMail `json:"mail"`
	Bounce           *struct {
		BounceType        string `json:"bounceType"`
		BounceSubType     string `json:"bounceSubType"`
		BouncedRecipients []struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"bouncedRecipients"`
		Timestamp string `json:"timestamp"`
	} `json:"bounce,omitempty"`
	Complaint *struct {
		ComplainedRecipients []struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"complainedRecipients"`
		Timestamp string `json:"timestamp"`
	} `json:"complaint,omitempty"`
	Delivery *struct {
		Recipients []string `json:"recipients"`
		Timestamp  string   `json:"timestamp"`
	} `json:"delivery,omitempty"`
}

type sesMail struct {
	MessageID   string   `json:"messageId"`
	Destination []string `json:"destination"`
}

type snsEnvelopeVerifier interface {
	Verify(context.Context, snsEnvelope) error
}

type snsSubscriptionConfirmer interface {
	Confirm(context.Context, string) error
}

type ProviderNotificationProcessor interface {
	ProcessProviderNotification(context.Context, emaildelivery.ProviderNotification) error
}

type SESEventHandler struct {
	topicARN  string
	verifier  snsEnvelopeVerifier
	confirmer snsSubscriptionConfirmer
	processor ProviderNotificationProcessor
	metrics   sesEventMetrics
}

func NewSESEventHandler(
	topicARN string,
	processor ProviderNotificationProcessor,
) *SESEventHandler {
	if processor == nil {
		panic("SES provider notification processor is required")
	}
	topicARN = strings.TrimSpace(topicARN)
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &SESEventHandler{
		topicARN: topicARN,
		verifier: &awsSNSVerifier{
			topicARN: topicARN,
			client:   client,
			cache:    make(map[string]cachedSNSCertificate),
		},
		confirmer: &awsSNSSubscriptionConfirmer{topicARN: topicARN, client: client},
		processor: processor,
		metrics:   newSESEventMetrics(),
	}
}

func (h *SESEventHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h == nil || h.verifier == nil || h.processor == nil || strings.TrimSpace(h.topicARN) == "" {
		http.Error(w, "SES event callback is not configured", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSNSMessageBytes))
	if err != nil {
		http.Error(w, "invalid SNS message body", http.StatusBadRequest)
		return
	}
	var envelope snsEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		http.Error(w, "invalid SNS message", http.StatusBadRequest)
		return
	}
	if headerType := strings.TrimSpace(r.Header.Get("x-amz-sns-message-type")); headerType != "" && headerType != envelope.Type {
		http.Error(w, "SNS message type mismatch", http.StatusBadRequest)
		return
	}
	if err := h.verifier.Verify(r.Context(), envelope); err != nil {
		h.metrics.record(r.Context(), envelope.Type, "rejected")
		slog.Warn("Rejected unauthenticated SES callback", "domain", "mail", "event", "mail.provider.callback_rejected", "outcome", "blocked", "reason", "sns_verification_failed")
		http.Error(w, "invalid SNS signature", http.StatusUnauthorized)
		return
	}

	switch envelope.Type {
	case "SubscriptionConfirmation":
		if h.confirmer == nil {
			http.Error(w, "SNS subscription confirmer is unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := h.confirmer.Confirm(r.Context(), envelope.SubscribeURL); err != nil {
			h.metrics.record(r.Context(), envelope.Type, "failed")
			slog.Error("SES callback subscription confirmation failed", "domain", "mail", "event", "mail.provider.subscription_confirmation_failed", "outcome", "failed", "message_id", envelope.MessageID)
			http.Error(w, "SNS subscription confirmation failed", http.StatusBadGateway)
			return
		}
		h.metrics.record(r.Context(), envelope.Type, "confirmed")
		slog.Info("SES callback subscription confirmed", "domain", "mail", "event", "mail.provider.subscription_confirmed", "outcome", "succeeded", "message_id", envelope.MessageID)
	case "Notification":
		var notification sesNotification
		if err := json.Unmarshal([]byte(envelope.Message), &notification); err != nil {
			http.Error(w, "invalid SES notification", http.StatusBadRequest)
			return
		}
		providerNotification, err := toProviderNotification(notification)
		if err != nil {
			h.metrics.record(r.Context(), typeNameForSESMetric(notification), "failed")
			http.Error(w, "invalid SES notification", http.StatusBadRequest)
			return
		}
		if err := h.processor.ProcessProviderNotification(r.Context(), providerNotification); err != nil {
			h.metrics.record(r.Context(), typeNameForSESMetric(notification), "failed")
			slog.Error("SES callback processing failed", "domain", "mail", "event", "mail.provider.callback_failed", "outcome", "failed", "message_id", envelope.MessageID)
			http.Error(w, "SES callback processing failed", http.StatusInternalServerError)
			return
		}
		h.metrics.record(r.Context(), typeNameForSESMetric(notification), "applied")
	case "UnsubscribeConfirmation":
		h.metrics.record(r.Context(), envelope.Type, "observed")
		slog.Warn("SES callback SNS subscription was removed", "domain", "mail", "event", "mail.provider.subscription_removed", "outcome", "observed", "message_id", envelope.MessageID)
	default:
		http.Error(w, "unsupported SNS message type", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type sesEventMetrics struct {
	callbackCounter otelmetric.Int64Counter
}

func newSESEventMetrics() sesEventMetrics {
	counter, err := otel.Meter(sharedtelemetry.ServiceBackend.Instrumentation("mail")).Int64Counter(
		"mail_provider_callback_total",
		otelmetric.WithDescription("Counts authenticated mail provider callbacks by type and result."),
	)
	if err != nil {
		slog.Warn("Failed to create mail provider callback counter", "error", err)
		return sesEventMetrics{}
	}
	return sesEventMetrics{callbackCounter: counter}
}

func (m sesEventMetrics) record(ctx context.Context, callbackType string, result string) {
	if m.callbackCounter == nil {
		return
	}
	m.callbackCounter.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("event_type", boundedSESCallbackMetricType(callbackType)),
		attribute.String("outcome", boundedSESCallbackMetricOutcome(result)),
	))
}

func boundedSESCallbackMetricType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "delivery":
		return "delivery"
	case "bounce":
		return "bounce"
	case "complaint":
		return "complaint"
	case "subscriptionconfirmation":
		return "subscription_confirmation"
	case "unsubscribeconfirmation":
		return "unsubscribe_confirmation"
	default:
		return "unknown"
	}
}

func boundedSESCallbackMetricOutcome(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "applied", "rejected", "failed", "confirmed", "observed":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func typeNameForSESMetric(notification sesNotification) string {
	if value := strings.TrimSpace(notification.EventType); value != "" {
		return value
	}
	return notification.NotificationType
}

func toProviderNotification(notification sesNotification) (emaildelivery.ProviderNotification, error) {
	providerMessageID := strings.TrimSpace(notification.Mail.MessageID)
	if providerMessageID == "" {
		return emaildelivery.ProviderNotification{}, fmt.Errorf("SES notification mail.messageId is required")
	}
	typeName := strings.ToLower(strings.TrimSpace(notification.EventType))
	if typeName == "" {
		typeName = strings.ToLower(strings.TrimSpace(notification.NotificationType))
	}

	var (
		eventAt    time.Time
		eventErr   error
		errorType  string
		recipients []string
		permanent  bool
	)
	switch typeName {
	case "delivery":
		if notification.Delivery == nil {
			return emaildelivery.ProviderNotification{}, fmt.Errorf("SES delivery payload is required")
		}
		eventAt, eventErr = parseSESEventTime(notification.Delivery.Timestamp)
		recipients = notification.Delivery.Recipients
	case "bounce":
		if notification.Bounce == nil {
			return emaildelivery.ProviderNotification{}, fmt.Errorf("SES bounce payload is required")
		}
		permanent = strings.EqualFold(strings.TrimSpace(notification.Bounce.BounceType), "Permanent")
		eventAt, eventErr = parseSESEventTime(notification.Bounce.Timestamp)
		errorType = boundedSESErrorType("bounce", notification.Bounce.BounceSubType)
		for _, recipient := range notification.Bounce.BouncedRecipients {
			recipients = append(recipients, recipient.EmailAddress)
		}
	case "complaint":
		if notification.Complaint == nil {
			return emaildelivery.ProviderNotification{}, fmt.Errorf("SES complaint payload is required")
		}
		eventAt, eventErr = parseSESEventTime(notification.Complaint.Timestamp)
		errorType = "complaint"
		for _, recipient := range notification.Complaint.ComplainedRecipients {
			recipients = append(recipients, recipient.EmailAddress)
		}
	default:
		return emaildelivery.ProviderNotification{}, fmt.Errorf("unsupported SES notification type %q", typeName)
	}
	if eventErr != nil {
		return emaildelivery.ProviderNotification{}, eventErr
	}
	return emaildelivery.ProviderNotification{
		Type: typeName, ProviderMessageID: providerMessageID,
		OccurredAt: eventAt, Recipients: recipients,
		FallbackRecipients: append([]string(nil), notification.Mail.Destination...),
		Permanent:          permanent, ErrorType: errorType,
	}, nil
}

func parseSESEventTime(raw string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid SES event timestamp: %w", err)
	}
	return parsed.UTC(), nil
}

func boundedSESErrorType(prefix string, detail string) string {
	value := strings.ToLower(strings.TrimSpace(prefix))
	if detail = strings.ToLower(strings.TrimSpace(detail)); detail != "" {
		value += ":" + detail
	}
	if len(value) > 100 {
		value = value[:100]
	}
	return value
}

type cachedSNSCertificate struct {
	certificate *x509.Certificate
	expiresAt   time.Time
}

type awsSNSVerifier struct {
	topicARN string
	client   *http.Client
	roots    *x509.CertPool
	now      func() time.Time
	mu       sync.Mutex
	cache    map[string]cachedSNSCertificate
}

func (v *awsSNSVerifier) Verify(ctx context.Context, envelope snsEnvelope) error {
	if strings.TrimSpace(envelope.TopicARN) != strings.TrimSpace(v.topicARN) {
		return fmt.Errorf("unexpected SNS topic ARN")
	}
	timestamp, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(envelope.Timestamp))
	if err != nil {
		return fmt.Errorf("invalid SNS timestamp: %w", err)
	}
	now := time.Now().UTC()
	if v.now != nil {
		now = v.now().UTC()
	}
	timestamp = timestamp.UTC()
	if timestamp.Before(now.Add(-maxSNSMessageAge)) || timestamp.After(now.Add(maxSNSClockSkew)) {
		return fmt.Errorf("SNS timestamp is outside the accepted delivery window")
	}
	certURL, err := validatedSNSURL(v.topicARN, envelope.SigningCertURL, false)
	if err != nil {
		return err
	}
	certificate, err := v.loadCertificate(ctx, certURL.String())
	if err != nil {
		return err
	}
	publicKey, ok := certificate.PublicKey.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("SNS signing certificate is not RSA")
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(envelope.Signature))
	if err != nil {
		return fmt.Errorf("decode SNS signature: %w", err)
	}
	canonical, err := canonicalSNSMessage(envelope)
	if err != nil {
		return err
	}
	var (
		hash   crypto.Hash
		digest []byte
	)
	switch envelope.SignatureVersion {
	case "1":
		sum := sha1.Sum([]byte(canonical))
		hash, digest = crypto.SHA1, sum[:]
	case "2":
		sum := sha256.Sum256([]byte(canonical))
		hash, digest = crypto.SHA256, sum[:]
	default:
		return fmt.Errorf("unsupported SNS signature version")
	}
	if err := rsa.VerifyPKCS1v15(publicKey, hash, digest, signature); err != nil {
		return fmt.Errorf("verify SNS signature: %w", err)
	}
	return nil
}

func (v *awsSNSVerifier) loadCertificate(ctx context.Context, rawURL string) (*x509.Certificate, error) {
	now := time.Now()
	v.mu.Lock()
	if cached, ok := v.cache[rawURL]; ok && now.Before(cached.expiresAt) {
		v.mu.Unlock()
		return cached.certificate, nil
	}
	v.mu.Unlock()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := v.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch SNS signing certificate: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch SNS signing certificate: HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read SNS signing certificate: %w", err)
	}
	block, _ := pem.Decode(body)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("invalid SNS signing certificate PEM")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse SNS signing certificate: %w", err)
	}
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: v.roots}); err != nil {
		return nil, fmt.Errorf("verify SNS signing certificate chain: %w", err)
	}
	expiresAt := certificate.NotAfter
	if cacheLimit := now.Add(24 * time.Hour); expiresAt.After(cacheLimit) {
		expiresAt = cacheLimit
	}
	v.mu.Lock()
	v.cache[rawURL] = cachedSNSCertificate{certificate: certificate, expiresAt: expiresAt}
	v.mu.Unlock()
	return certificate, nil
}

func canonicalSNSMessage(envelope snsEnvelope) (string, error) {
	fields := []struct {
		name  string
		value string
	}{{"Message", envelope.Message}, {"MessageId", envelope.MessageID}}
	switch envelope.Type {
	case "Notification":
		if envelope.Subject != "" {
			fields = append(fields, struct{ name, value string }{"Subject", envelope.Subject})
		}
		fields = append(fields,
			struct{ name, value string }{"Timestamp", envelope.Timestamp},
			struct{ name, value string }{"TopicArn", envelope.TopicARN},
			struct{ name, value string }{"Type", envelope.Type},
		)
	case "SubscriptionConfirmation", "UnsubscribeConfirmation":
		fields = append(fields,
			struct{ name, value string }{"SubscribeURL", envelope.SubscribeURL},
			struct{ name, value string }{"Timestamp", envelope.Timestamp},
			struct{ name, value string }{"Token", envelope.Token},
			struct{ name, value string }{"TopicArn", envelope.TopicARN},
			struct{ name, value string }{"Type", envelope.Type},
		)
	default:
		return "", fmt.Errorf("unsupported SNS message type")
	}
	var builder strings.Builder
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" && field.name != "Subject" {
			return "", fmt.Errorf("SNS field %s is required", field.name)
		}
		builder.WriteString(field.name)
		builder.WriteByte('\n')
		builder.WriteString(field.value)
		builder.WriteByte('\n')
	}
	return builder.String(), nil
}

type awsSNSSubscriptionConfirmer struct {
	topicARN string
	client   *http.Client
}

func (c *awsSNSSubscriptionConfirmer) Confirm(ctx context.Context, rawURL string) error {
	confirmURL, err := validatedSNSURL(c.topicARN, rawURL, true)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, confirmURL.String(), nil)
	if err != nil {
		return err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("SNS subscription confirmation returned HTTP %d", response.StatusCode)
	}
	return nil
}

func validatedSNSURL(topicARN string, rawURL string, confirmation bool) (*url.URL, error) {
	partition, region, err := snsTopicPartitionRegion(topicARN)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("parse SNS URL: %w", err)
	}
	if parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" {
		return nil, fmt.Errorf("SNS URL must use HTTPS without userinfo or custom port")
	}
	expectedHost := "sns." + region + ".amazonaws.com"
	if partition == "aws-cn" {
		expectedHost += ".cn"
	}
	if !strings.EqualFold(parsed.Hostname(), expectedHost) {
		return nil, fmt.Errorf("unexpected SNS URL host")
	}
	if confirmation {
		query := parsed.Query()
		if query.Get("Action") != "ConfirmSubscription" ||
			query.Get("TopicArn") != topicARN ||
			strings.TrimSpace(query.Get("Token")) == "" {
			return nil, fmt.Errorf("invalid SNS subscription confirmation URL")
		}
		return parsed, nil
	}
	if !strings.HasPrefix(parsed.EscapedPath(), "/SimpleNotificationService-") ||
		!strings.HasSuffix(parsed.EscapedPath(), ".pem") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("invalid SNS signing certificate URL")
	}
	return parsed, nil
}

func snsTopicPartitionRegion(topicARN string) (string, string, error) {
	parts := strings.SplitN(strings.TrimSpace(topicARN), ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "sns" || parts[3] == "" || parts[5] == "" {
		return "", "", fmt.Errorf("invalid SNS topic ARN")
	}
	switch parts[1] {
	case "aws", "aws-cn", "aws-us-gov":
	default:
		return "", "", fmt.Errorf("unsupported SNS partition")
	}
	return parts[1], parts[3], nil
}
