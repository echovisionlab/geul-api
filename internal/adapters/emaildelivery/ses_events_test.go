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
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/telemetry"
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

func TestSESFailureLogsPreserveSanitizedCause(t *testing.T) {
	for _, tc := range []struct {
		name, kind, level string
		status            int
	}{
		{"verification", "Notification", "WARN", http.StatusUnauthorized},
		{"confirmation", "SubscriptionConfirmation", "ERROR", http.StatusBadGateway},
		{"processing", "Notification", "ERROR", http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
			t.Cleanup(func() { slog.SetDefault(previous) })
			failure := errors.New("upstream unavailable: reader@example.test token=private-token https://sns.example.test/?Token=private-token")
			handler := &SESEventHandler{topicARN: testSESTopicARN, verifier: stubSNSVerifier{}, confirmer: &recordingSNSConfirmer{}, processor: &recordingSESNotificationProcessor{}}
			switch tc.name {
			case "verification":
				handler.verifier = stubSNSVerifier{err: failure}
			case "confirmation":
				handler.confirmer = &recordingSNSConfirmer{err: failure}
			case "processing":
				handler.processor = &recordingSESNotificationProcessor{err: failure}
			}
			envelope := snsEnvelope{Type: tc.kind, MessageID: "sns-log-1", Message: `{"eventType":"Delivery","mail":{"messageId":"ses-1"},"delivery":{"timestamp":"2026-08-01T00:00:00Z","recipients":["reader@example.test"]}}`}
			body, err := json.Marshal(envelope)
			require.NoError(t, err)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, SESEventCallbackPath, bytes.NewReader(body)))
			require.Equal(t, tc.status, response.Code)
			var entry map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
			require.Equal(t, tc.level, entry["level"])
			require.Equal(t, "sns-log-1", entry["message_id"])
			require.Contains(t, entry["failure_cause"], "upstream unavailable")
			require.Contains(t, entry["failure_cause"], "https://sns.example.test/")
			for _, private := range []string{"reader@example.test", "private-token"} {
				require.NotContains(t, output.String(), private)
			}
		})
	}
}

func TestAWSSNSVerifierCompletesChainFromIssuerURL(t *testing.T) {
	now := time.Now().UTC()
	newKey := func() *rsa.PrivateKey {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		return key
	}
	rootKey, intermediateKey, leafKey := newKey(), newKey(), newKey()
	root := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test root"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	require.NoError(t, err)
	root, err = x509.ParseCertificate(rootDER)
	require.NoError(t, err)
	intermediate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "test intermediate"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	intermediateDER, err := x509.CreateCertificate(rand.Reader, intermediate, root, &intermediateKey.PublicKey, rootKey)
	require.NoError(t, err)
	intermediate, err = x509.ParseCertificate(intermediateDER)
	require.NoError(t, err)

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "sns.amazonaws.com"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IssuingCertificateURL: []string{server.URL + "/intermediate.cer"},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, intermediate, &leafKey.PublicKey, intermediateKey)
	require.NoError(t, err)
	mux.HandleFunc("/leaf.pem", func(w http.ResponseWriter, _ *http.Request) {
		_ = pem.Encode(w, &pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	})
	issuerFetches := 0
	mux.HandleFunc("/intermediate.cer", func(w http.ResponseWriter, _ *http.Request) {
		issuerFetches++
		_, _ = w.Write(intermediateDER)
	})
	roots := x509.NewCertPool()
	roots.AddCert(root)

	rejecting := &awsSNSVerifier{client: server.Client(), roots: roots, cache: map[string]cachedSNSCertificate{}}
	_, err = rejecting.loadCertificate(t.Context(), server.URL+"/leaf.pem")
	require.ErrorContains(t, err, "unknown authority")
	require.Zero(t, issuerFetches)

	verifier := &awsSNSVerifier{
		client: server.Client(), roots: roots, cache: map[string]cachedSNSCertificate{},
		issuerAllowed: func(issuerURL *url.URL) bool { return issuerURL.Host == strings.TrimPrefix(server.URL, "http://") },
	}
	certificate, err := verifier.loadCertificate(t.Context(), server.URL+"/leaf.pem")
	require.NoError(t, err)
	require.Equal(t, "sns.amazonaws.com", certificate.Subject.CommonName)
	require.Equal(t, 1, issuerFetches)

	otherRoots := x509.NewCertPool()
	untrusted := &awsSNSVerifier{
		client: server.Client(), roots: otherRoots, cache: map[string]cachedSNSCertificate{},
		issuerAllowed: verifier.issuerAllowed,
	}
	_, err = untrusted.loadCertificate(t.Context(), server.URL+"/leaf.pem")
	require.ErrorContains(t, err, "unknown authority")
}

func TestAmazonTrustIssuerURLAllowsOnlyAmazonTrustHosts(t *testing.T) {
	for rawURL, want := range map[string]bool{
		"http://crt.r2m04.amazontrust.com/r2m04.cer":      true,
		"https://crt.rootca1.amazontrust.com/rootca1.cer": true,
		"http://amazontrust.com.example.com/r2m04.cer":    false,
		"http://crt.r2m04.amazontrust.com:8080/r2m04.cer": false,
		"ftp://crt.r2m04.amazontrust.com/r2m04.cer":       false,
		"http://user@crt.r2m04.amazontrust.com/r2m04.cer": false,
	} {
		parsed, err := url.Parse(rawURL)
		require.NoError(t, err)
		require.Equal(t, want, amazonTrustIssuerURL(parsed), rawURL)
	}
}
