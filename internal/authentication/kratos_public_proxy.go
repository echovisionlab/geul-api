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
		slog.ErrorContext(request.Context(), "Kratos proxy request failed", "status_code", http.StatusBadGateway, failurecause.Attr(err, privateValues...))
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
		slog.WarnContext(request.Context(), "Operation failed", "reason", "inspect_code_request", "status_code", http.StatusRequestEntityTooLarge, failurecause.Attr(err))
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
	if strings.TrimSpace(issuance.Recipient) == "" {
		issuance.Recipient, err = p.resolveFlowRecipient(request, issuance)
		if err != nil {
			slog.WarnContext(request.Context(), "Operation failed", "reason", "resolve_flow_recipient", "status_code", http.StatusBadRequest, failurecause.Attr(err), "template_type", issuance.EventKey.String())
			writeKratosProxyError(request.Context(), w, http.StatusBadRequest, "authentication code request is invalid", 0)
			return
		}
	}

	issuance.ClientIP = authCodeClientIP(request)
	reservation, allowed, retryAfter, err := p.limiter.Reserve(request.Context(), issuance)
	if err != nil {
		slog.ErrorContext(request.Context(), "Operation failed", "reason", "reserve_code_issuance", "status_code", http.StatusServiceUnavailable, failurecause.Attr(err, issuance.Recipient), "template_type", issuance.EventKey.String())
		writeKratosProxyError(request.Context(), w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
		return
	}
	reservation.quota.write(w.Header())
	if !allowed {
		writeKratosProxyError(request.Context(), w, http.StatusTooManyRequests, "please wait before requesting another code", retryAfter)
		return
	}
	provenance, err := NewAuthCodeIssuanceProvenance(
		p.issuanceKey,
		issuance.EventKey,
		issuance.Recipient,
		reservation.token,
		reservation.issuedAt,
	)
	if err != nil {
		p.releaseReservation(request, reservation)
		slog.WarnContext(request.Context(), "Operation failed", "reason", "create_issuance_provenance", "status_code", http.StatusBadRequest, failurecause.Attr(err, issuance.Recipient, reservation.token), "template_type", issuance.EventKey.String())
		writeKratosProxyError(request.Context(), w, http.StatusBadRequest, "authentication code request is invalid", 0)
		return
	}
	body, err = injectAuthCodeIssuanceProvenance(
		request.Header.Get("Content-Type"),
		body,
		provenance,
	)
	if err != nil {
		p.releaseReservation(request, reservation)
		slog.WarnContext(request.Context(), "Operation failed", "reason", "inject_issuance_provenance", "status_code", http.StatusBadRequest, failurecause.Attr(err, issuance.Recipient, reservation.token), "template_type", issuance.EventKey.String())
		writeKratosProxyError(request.Context(), w, http.StatusBadRequest, "authentication code request is invalid", 0)
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

func (p *KratosPublicProxy) resolveFlowRecipient(
	request *http.Request,
	issuance AuthCodeIssuanceRequest,
) (string, error) {
	flowKind := ""
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
		return "", err
	}
	flowRequest.Header.Set("Accept", "application/json")
	sharedtelemetry.InjectCorrelation(request.Context(), propagation.HeaderCarrier(flowRequest.Header))
	if cookie := request.Header.Get("Cookie"); cookie != "" {
		flowRequest.Header.Set("Cookie", cookie)
	}
	response, err := p.httpClient.Do(flowRequest)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("authentication flow lookup returned %d", response.StatusCode)
	}
	body, err := readBoundedKratosBody(response.Body)
	if err != nil {
		return "", err
	}
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
		return "", err
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
		slog.ErrorContext(releaseCtx, "Operation failed", "reason", "release_code_issuance", failurecause.Attr(err, reservation.token))
	}
}
