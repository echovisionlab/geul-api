package emaildeliveryadapter

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/stretchr/testify/require"
)

const testSESTopicARN = "arn:aws:sns:ap-northeast-2:123456789012:geul-ses-events"

func TestAWSSNSVerifierAcceptsCanonicalSignatureAndExactTopic(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	now := time.Now().UTC()
	certificate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "sns.ap-northeast-2.amazonaws.com"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)
	certificate, err = x509.ParseCertificate(certificateDER)
	require.NoError(t, err)
	certURL := "https://sns.ap-northeast-2.amazonaws.com/SimpleNotificationService-test.pem"
	envelope := snsEnvelope{
		Type:             "Notification",
		MessageID:        "sns-message-1",
		TopicARN:         testSESTopicARN,
		Message:          `{"eventType":"Delivery"}`,
		Timestamp:        now.Format(time.RFC3339Nano),
		SignatureVersion: "2",
		SigningCertURL:   certURL,
	}
	canonical, err := canonicalSNSMessage(envelope)
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(canonical))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	require.NoError(t, err)
	envelope.Signature = base64.StdEncoding.EncodeToString(signature)

	verifier := &awsSNSVerifier{
		topicARN: testSESTopicARN,
		cache: map[string]cachedSNSCertificate{
			certURL: {certificate: certificate, expiresAt: now.Add(time.Hour)},
		},
	}
	require.NoError(t, verifier.Verify(t.Context(), envelope))

	envelope.TopicARN = "arn:aws:sns:ap-northeast-2:123456789012:other"
	require.ErrorContains(t, verifier.Verify(t.Context(), envelope), "unexpected SNS topic ARN")
}

func TestAWSSNSVerifierRejectsStaleOrFutureTimestampBeforeCertificateFetch(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	verifier := &awsSNSVerifier{
		topicARN: testSESTopicARN,
		now:      func() time.Time { return now },
	}
	envelope := snsEnvelope{
		Type:             "Notification",
		MessageID:        "sns-message-1",
		TopicARN:         testSESTopicARN,
		Message:          `{}`,
		SignatureVersion: "2",
		SigningCertURL:   "https://sns.ap-northeast-2.amazonaws.com/SimpleNotificationService-test.pem",
	}

	envelope.Timestamp = now.Add(-maxSNSMessageAge - time.Second).Format(time.RFC3339Nano)
	require.ErrorContains(t, verifier.Verify(t.Context(), envelope), "outside the accepted delivery window")

	envelope.Timestamp = now.Add(maxSNSClockSkew + time.Second).Format(time.RFC3339Nano)
	require.ErrorContains(t, verifier.Verify(t.Context(), envelope), "outside the accepted delivery window")
}

func TestValidatedSNSURLRejectsSpoofedOrMismatchedURLs(t *testing.T) {
	validCertificateURL := "https://sns.ap-northeast-2.amazonaws.com/SimpleNotificationService-test.pem"
	_, err := validatedSNSURL(testSESTopicARN, validCertificateURL, false)
	require.NoError(t, err)

	for _, rawURL := range []string{
		"http://sns.ap-northeast-2.amazonaws.com/SimpleNotificationService-test.pem",
		"https://sns.ap-northeast-2.amazonaws.com.evil.test/SimpleNotificationService-test.pem",
		"https://sns.us-east-1.amazonaws.com/SimpleNotificationService-test.pem",
		"https://sns.ap-northeast-2.amazonaws.com:8443/SimpleNotificationService-test.pem",
		"https://user@sns.ap-northeast-2.amazonaws.com/SimpleNotificationService-test.pem",
	} {
		_, err := validatedSNSURL(testSESTopicARN, rawURL, false)
		require.Error(t, err, rawURL)
	}
}

func TestParseSESEventTimeRejectsMalformedProviderTimestamp(t *testing.T) {
	parsed, err := parseSESEventTime("2026-08-01T00:00:00.123Z")
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 8, 1, 0, 0, 0, 123_000_000, time.UTC), parsed)

	_, err = parseSESEventTime("not-a-timestamp")
	require.ErrorContains(t, err, "invalid SES event timestamp")
}

func TestSESCallbackMetricLabelsAreBounded(t *testing.T) {
	require.Equal(t, "delivery", boundedSESCallbackMetricType("Delivery"))
	require.Equal(t, "subscription_confirmation", boundedSESCallbackMetricType("SubscriptionConfirmation"))
	require.Equal(t, "unknown", boundedSESCallbackMetricType("attacker-controlled-"+strings.Repeat("x", 100)))
	require.Equal(t, "applied", boundedSESCallbackMetricOutcome("applied"))
	require.Equal(t, "unknown", boundedSESCallbackMetricOutcome("arbitrary"))
}

func TestSESEventHandlerVerifiesThenProcessesNotification(t *testing.T) {
	processor := &recordingSESNotificationProcessor{}
	handler := &SESEventHandler{
		topicARN:  testSESTopicARN,
		verifier:  stubSNSVerifier{},
		processor: processor,
	}
	notification := `{"eventType":"Delivery","mail":{"messageId":"ses-provider-1"},"delivery":{"timestamp":"2026-08-01T00:00:00Z","recipients":["reader@example.test"]}}`
	envelope := snsEnvelope{
		Type:      "Notification",
		MessageID: "sns-message-1",
		TopicARN:  testSESTopicARN,
		Message:   notification,
	}
	body, err := json.Marshal(envelope)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, SESEventCallbackPath, bytes.NewReader(body))
	request.Header.Set("x-amz-sns-message-type", "Notification")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusNoContent, response.Code)
	require.Len(t, processor.notifications, 1)
	require.Equal(t, "ses-provider-1", processor.notifications[0].ProviderMessageID)
}

func TestSESEventHandlerRejectsUnverifiedMessage(t *testing.T) {
	handler := &SESEventHandler{
		topicARN:  testSESTopicARN,
		verifier:  stubSNSVerifier{err: context.Canceled},
		processor: &recordingSESNotificationProcessor{},
	}
	body, err := json.Marshal(snsEnvelope{Type: "Notification", TopicARN: testSESTopicARN})
	require.NoError(t, err)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, SESEventCallbackPath, bytes.NewReader(body)))
	require.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestSESEventHandlerConfirmsOnlyVerifiedSubscription(t *testing.T) {
	confirmer := &recordingSNSConfirmer{}
	handler := &SESEventHandler{
		topicARN:  testSESTopicARN,
		verifier:  stubSNSVerifier{},
		confirmer: confirmer,
		processor: &recordingSESNotificationProcessor{},
	}
	envelope := snsEnvelope{
		Type:         "SubscriptionConfirmation",
		MessageID:    "sns-confirm-1",
		TopicARN:     testSESTopicARN,
		SubscribeURL: "https://sns.ap-northeast-2.amazonaws.com/?Action=ConfirmSubscription",
	}
	body, err := json.Marshal(envelope)
	require.NoError(t, err)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, SESEventCallbackPath, bytes.NewReader(body)))
	require.Equal(t, http.StatusNoContent, response.Code)
	require.Equal(t, []string{envelope.SubscribeURL}, confirmer.urls)
}

type stubSNSVerifier struct{ err error }

func (v stubSNSVerifier) Verify(context.Context, snsEnvelope) error { return v.err }

type recordingSNSConfirmer struct {
	urls []string
	err  error
}

func (c *recordingSNSConfirmer) Confirm(_ context.Context, rawURL string) error {
	c.urls = append(c.urls, rawURL)
	return c.err
}

type recordingSESNotificationProcessor struct {
	notifications []emaildelivery.ProviderNotification
	err           error
}

func (p *recordingSESNotificationProcessor) ProcessProviderNotification(_ context.Context, notification emaildelivery.ProviderNotification) error {
	p.notifications = append(p.notifications, notification)
	return p.err
}
