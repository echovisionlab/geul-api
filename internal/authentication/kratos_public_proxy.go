package authentication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/structured"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"go.opentelemetry.io/otel/propagation"
)

const maxKratosProxyRequestBytes = 1 << 20

func kratosRequestMediaType(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
}

func readBoundedKratosBody(body io.Reader) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(body, maxKratosProxyRequestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > maxKratosProxyRequestBytes {
		return nil, errors.New("request body is too large")
	}
	return payload, nil
}

func parseKratosPublicURL(rawURL string) (*url.URL, error) {
	target, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("parse kratos public URL: %w", err)
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return nil, errors.New("kratos public URL must use HTTP or HTTPS")
	}
	if target.Host == "" || target.User != nil {
		return nil, errors.New("kratos public URL must have a host and no userinfo")
	}
	return target, nil
}

type authCodeIssuancePreflight interface {
	Reserve(
		ctx context.Context,
		request AuthCodeIssuanceRequest,
	) (AuthCodeIssuanceReservation, bool, time.Duration, error)
	Release(ctx context.Context, reservation AuthCodeIssuanceReservation) error
}

type KratosPublicProxy struct {
	proxy       *httputil.ReverseProxy
	target      *url.URL
	httpClient  *http.Client
	limiter     authCodeIssuancePreflight
	issuanceKey []byte
}

func NewKratosPublicProxy(
	targetURL string,
	limiter authCodeIssuancePreflight,
	issuanceKey []byte,
) (*KratosPublicProxy, error) {
	if limiter == nil {
		return nil, errors.New("auth code issuance limiter is required")
	}
	if len(issuanceKey) == 0 {
		return nil, errors.New("auth code issuance provenance key is required")
	}
	target, err := parseKratosPublicURL(targetURL)
	if err != nil {
		return nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = stripUpstreamCORSHeaders
	proxy.ErrorHandler = func(w http.ResponseWriter, request *http.Request, err error) {
		privateValues := []string{request.Header.Get("Authorization"), request.Header.Get("Cookie")}
		for _, cookie := range request.Cookies() {
			privateValues = append(privateValues, cookie.Value)
		}
		recordAuthenticationFailure(request.Context(), fmt.Errorf("proxy Kratos request: %w", err), privateValues...)
		w.WriteHeader(http.StatusBadGateway)
	}

	return &KratosPublicProxy{
		proxy:       proxy,
		target:      target,
		httpClient:  &http.Client{Timeout: 5 * time.Second},
		limiter:     limiter,
		issuanceKey: append([]byte(nil), issuanceKey...),
	}, nil
}

func (p *KratosPublicProxy) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	sharedtelemetry.InjectCorrelation(request.Context(), propagation.HeaderCarrier(request.Header))
	issuance, body, candidate, err := inspectAuthCodeIssuanceRequest(request)
	if err != nil {
		writeKratosProxyError(request.Context(), w, http.StatusRequestEntityTooLarge, "request body is too large", 0)
		return
	}
	if body != nil {
		request.Body = io.NopCloser(bytes.NewReader(body))
		request.ContentLength = int64(len(body))
	}
	if !candidate {
		p.proxy.ServeHTTP(w, request)
		return
	}
	if !p.completeRecipient(w, request, &issuance) {
		return
	}

	issuance.ClientIP = authCodeClientIP(request)
	reservation, allowed, retryAfter, err := p.limiter.Reserve(request.Context(), issuance)
	if err != nil {
		recordAuthenticationFailure(request.Context(), issuanceFailure("reserve code issuance", issuance, err), issuance.Recipient)
		writeKratosProxyError(request.Context(), w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
		return
	}
	reservation.quota.write(w.Header())
	if !allowed {
		writeKratosProxyError(request.Context(), w, http.StatusTooManyRequests, "please wait before requesting another code", retryAfter)
		return
	}
	body, ok := p.withProvenance(w, request, issuance, reservation, body)
	if !ok {
		return
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))

	response := newKratosResponseCapture(w, maxKratosProxyRequestBytes)
	p.proxy.ServeHTTP(response, request)
	if kratosResponseAcceptedIssuance(response.statusCode(), response.body.Bytes()) {
		return
	}

	p.releaseReservation(request, reservation)
}

// completeRecipient fills a code request without a recipient from its Kratos
// flow. When the flow has none, it has answered w and reports false.
func (p *KratosPublicProxy) completeRecipient(w http.ResponseWriter, request *http.Request, issuance *AuthCodeIssuanceRequest) bool {
	if strings.TrimSpace(issuance.Recipient) != "" {
		return true
	}
	recipient, err := p.resolveFlowRecipient(request, *issuance)
	if err != nil {
		recordAuthenticationFailure(request.Context(), issuanceFailure("resolve flow recipient", *issuance, err))
		if errors.Is(err, errKratosFlowLookupUnavailable) {
			writeKratosProxyError(request.Context(), w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
			return false
		}
		writeKratosProxyError(request.Context(), w, http.StatusBadRequest, "authentication code request is invalid", 0)
		return false
	}
	issuance.Recipient = recipient
	return true
}

// withProvenance returns body carrying the signed provenance of reservation.
// When it cannot, it releases the reservation, has answered w and reports
// false.
func (p *KratosPublicProxy) withProvenance(
	w http.ResponseWriter,
	request *http.Request,
	issuance AuthCodeIssuanceRequest,
	reservation AuthCodeIssuanceReservation,
	body []byte,
) ([]byte, bool) {
	provenance, err := NewAuthCodeIssuanceProvenance(
		p.issuanceKey,
		issuance.EventKey,
		issuance.Recipient,
		reservation.token,
		reservation.issuedAt,
	)
	if err != nil {
		p.releaseReservation(request, reservation)
		recordAuthenticationFailure(request.Context(), issuanceFailure("create issuance provenance", issuance, err), issuance.Recipient, reservation.token)
		writeKratosProxyError(request.Context(), w, http.StatusBadRequest, "authentication code request is invalid", 0)
		return nil, false
	}
	body, err = injectAuthCodeIssuanceProvenance(
		request.Header.Get("Content-Type"),
		body,
		provenance,
	)
	if err != nil {
		p.releaseReservation(request, reservation)
		recordAuthenticationFailure(request.Context(), issuanceFailure("inject issuance provenance", issuance, err), issuance.Recipient, reservation.token)
		writeKratosProxyError(request.Context(), w, http.StatusBadRequest, "authentication code request is invalid", 0)
		return nil, false
	}
	return body, true
}

// issuanceFailure names the failed code issuance step and its template for the
// request record.
func issuanceFailure(operation string, issuance AuthCodeIssuanceRequest, err error) error {
	return failurecause.WithAttrs(fmt.Errorf("%s: %w", operation, err), slog.String("template_type", issuance.EventKey.String()))
}

// errKratosFlowLookupUnavailable marks a flow lookup that failed on the Kratos
// side (unreachable, 5xx, unreadable answer) rather than on the client's flow.
var errKratosFlowLookupUnavailable = errors.New("authentication flow lookup unavailable")

func (p *KratosPublicProxy) resolveFlowRecipient(
	request *http.Request,
	issuance AuthCodeIssuanceRequest,
) (string, error) {
	var flowKind string
	switch issuance.EventKey {
	case email.EventLoginCode:
		flowKind = "login"
	case email.EventRegistrationCode:
		flowKind = "registration"
	case email.EventVerificationCode:
		flowKind = "verification"
	default:
		return "", errors.New("unsupported authentication flow")
	}
	flowURL := p.target.ResolveReference(&url.URL{
		Path:     "/self-service/" + flowKind + "/flows",
		RawQuery: url.Values{"id": {issuance.FlowID}}.Encode(),
	})
	flowRequest, err := http.NewRequestWithContext(request.Context(), http.MethodGet, flowURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errKratosFlowLookupUnavailable, err)
	}
	flowRequest.Header.Set("Accept", "application/json")
	sharedtelemetry.InjectCorrelation(request.Context(), propagation.HeaderCarrier(flowRequest.Header))
	if cookie := request.Header.Get("Cookie"); cookie != "" {
		flowRequest.Header.Set("Cookie", cookie)
	}
	response, err := p.httpClient.Do(flowRequest)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errKratosFlowLookupUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusInternalServerError {
		return "", fmt.Errorf("%w: Kratos returned %d", errKratosFlowLookupUnavailable, response.StatusCode)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("authentication flow lookup returned %d", response.StatusCode)
	}
	body, err := readBoundedKratosBody(response.Body)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errKratosFlowLookupUnavailable, err)
	}
	return flowRecipient(body)
}

// flowRecipient is the first delivery address among the identifier and email
// nodes of a Kratos flow body.
func flowRecipient(body []byte) (string, error) {
	var flow struct {
		UI struct {
			Nodes []struct {
				Attributes struct {
					Name  string           `json:"name"`
					Value structured.Value `json:"value"`
				} `json:"attributes"`
			} `json:"nodes"`
		} `json:"ui"`
	}
	if err := json.Unmarshal(body, &flow); err != nil {
		return "", fmt.Errorf("%w: %w", errKratosFlowLookupUnavailable, err)
	}
	for _, node := range flow.UI.Nodes {
		switch node.Attributes.Name {
		case "identifier", "email", "traits.email":
			if recipient, ok := node.Attributes.Value.(string); ok {
				recipient = email.NormalizeAddressForDelivery(recipient)
				if recipient != "" {
					return recipient, nil
				}
			}
		}
	}
	return "", errors.New("authentication flow recipient is missing")
}

func (p *KratosPublicProxy) releaseReservation(
	request *http.Request,
	reservation AuthCodeIssuanceReservation,
) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 2*time.Second)
	defer cancel()
	if err := p.limiter.Release(releaseCtx, reservation); err != nil {
		slog.ErrorContext(releaseCtx, "Auth code issuance reservation release failed", "operation", "release_code_issuance", failurecause.Attr(err, reservation.token))
	}
}
