package authentication

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func injectAuthCodeIssuanceProvenance(
	contentType string,
	body []byte,
	provenance AuthCodeIssuanceProvenance,
) ([]byte, error) {
	mediaType := kratosRequestMediaType(contentType)
	if mediaType == "application/json" {
		var payload structured.Fields
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		transient, err := authCodeTransientPayload(payload["transient_payload"])
		if err != nil {
			return nil, err
		}
		transient[AuthCodeIssuanceProvenanceNamespace] = authCodeIssuanceProvenanceMap(provenance)
		payload["transient_payload"] = transient
		return json.Marshal(payload)
	}

	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	transient, err := authCodeTransientPayload(values.Get("transient_payload"))
	if err != nil {
		return nil, err
	}
	transient[AuthCodeIssuanceProvenanceNamespace] = authCodeIssuanceProvenanceMap(provenance)
	encodedTransient, err := json.Marshal(transient)
	if err != nil {
		return nil, err
	}
	values.Set("transient_payload", string(encodedTransient))
	return []byte(values.Encode()), nil
}

func authCodeTransientPayload(raw structured.Value) (structured.Fields, error) {
	switch value := raw.(type) {
	case nil:
		return make(structured.Fields), nil
	case structured.Fields:
		return value, nil
	case string:
		if strings.TrimSpace(value) == "" {
			return make(structured.Fields), nil
		}
		var decoded structured.Fields
		if err := json.Unmarshal([]byte(value), &decoded); err != nil || decoded == nil {
			return nil, errors.New("transient payload must be a JSON object")
		}
		return decoded, nil
	default:
		return nil, errors.New("transient payload must be an object")
	}
}

func authCodeIssuanceProvenanceMap(provenance AuthCodeIssuanceProvenance) structured.Fields {
	return structured.Fields{
		"version":     provenance.Version,
		"issuance_id": provenance.IssuanceID,
		"issued_at":   provenance.IssuedAt,
		"purpose":     provenance.Purpose,
		"recipient":   provenance.Recipient,
		"mac":         provenance.MAC,
	}
}

func inspectAuthCodeIssuanceRequest(
	request *http.Request,
) (AuthCodeIssuanceRequest, []byte, bool, error) {
	if request == nil || request.Method != http.MethodPost {
		return AuthCodeIssuanceRequest{}, nil, false, nil
	}

	eventKey, supported := authCodeEventForKratosPath(request.URL.Path)
	if !supported {
		return AuthCodeIssuanceRequest{}, nil, false, nil
	}
	body, err := readBoundedKratosBody(request.Body)
	if err != nil {
		return AuthCodeIssuanceRequest{}, nil, false, err
	}

	method, submittedCode, recipient := authCodeRequestFields(request.Header.Get("Content-Type"), body)
	recipient, eligible := eligibleAuthCodeRequest(
		request.URL.Path,
		request.Header.Get("Content-Type"),
		body,
		method,
		submittedCode,
		recipient,
	)
	if !eligible {
		return AuthCodeIssuanceRequest{}, body, false, nil
	}

	flowID := strings.TrimSpace(request.URL.Query().Get("flow"))
	if strings.TrimSpace(recipient) == "" && flowID == "" {
		// Kratos cannot generate a code without a flow or recipient. Forward the
		// invalid request so Kratos remains the schema-validation authority.
		return AuthCodeIssuanceRequest{}, body, false, nil
	}

	return AuthCodeIssuanceRequest{
		EventKey:  eventKey,
		Recipient: recipient,
		FlowID:    flowID,
	}, body, true, nil
}

func eligibleAuthCodeRequest(path, contentType string, body []byte, method, submittedCode, recipient string) (string, bool) {
	if strings.TrimSuffix(path, "/") == "/self-service/settings" {
		if !strings.EqualFold(strings.TrimSpace(method), "profile") {
			return "", false
		}
		pendingEmail := authCodePendingEmailRequestField(contentType, body)
		return pendingEmail, pendingEmail != ""
	}
	eligible := strings.EqualFold(strings.TrimSpace(method), "code") && strings.TrimSpace(submittedCode) == ""
	return recipient, eligible
}

func authCodeEventForKratosPath(path string) (email.EventKey, bool) {
	switch strings.TrimSuffix(path, "/") {
	case "/self-service/login":
		return email.EventLoginCode, true
	case "/self-service/registration":
		return email.EventRegistrationCode, true
	case "/self-service/verification":
		return email.EventVerificationCode, true
	case "/self-service/settings":
		return email.EventVerificationCode, true
	default:
		return "", false
	}
}

func authCodePendingEmailRequestField(contentType string, body []byte) string {
	mediaType := kratosRequestMediaType(contentType)
	if mediaType == "application/json" {
		var payload structured.Fields
		if json.Unmarshal(body, &payload) != nil {
			return ""
		}
		traits, _ := payload["traits"].(structured.Fields)
		return stringMapValue(traits, "pending_email")
	}

	values, err := url.ParseQuery(string(body))
	if err != nil {
		return ""
	}
	return firstNonEmptyFormValue(values, "traits.pending_email", "traits[pending_email]")
}

func firstNonEmptyFormValue(values url.Values, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func authCodeRequestFields(contentType string, body []byte) (method, code, recipient string) {
	mediaType := kratosRequestMediaType(contentType)
	if mediaType == "application/json" {
		var payload structured.Fields
		if json.Unmarshal(body, &payload) != nil {
			return "", "", ""
		}
		method = stringMapValue(payload, "method")
		code = stringMapValue(payload, "code")
		for _, key := range []string{"identifier", "email"} {
			if recipient = stringMapValue(payload, key); recipient != "" {
				return method, code, recipient
			}
		}
		if traits, ok := payload["traits"].(structured.Fields); ok {
			recipient = stringMapValue(traits, "email")
		}
		return method, code, recipient
	}

	values, err := url.ParseQuery(string(body))
	if err != nil {
		return "", "", ""
	}
	method = values.Get("method")
	code = values.Get("code")
	for _, key := range []string{"identifier", "email", "traits.email", "traits[email]"} {
		if recipient = strings.TrimSpace(values.Get(key)); recipient != "" {
			break
		}
	}
	return method, code, recipient
}

func stringMapValue(values structured.Fields, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func authCodeClientIP(request *http.Request) string {
	remoteHost := request.RemoteAddr
	if host, _, err := net.SplitHostPort(remoteHost); err == nil {
		remoteHost = host
	}
	remoteIP := net.ParseIP(strings.Trim(remoteHost, "[]"))
	if remoteIP == nil {
		return ""
	}

	if remoteIP.IsPrivate() || remoteIP.IsLoopback() {
		if candidate := authCodeForwardedClientIP(request); candidate != "" {
			return candidate
		}
	}
	return remoteIP.String()
}

func authCodeForwardedClientIP(request *http.Request) string {
	for _, header := range []string{"CF-Connecting-IP", "X-Real-IP"} {
		if candidate := exactIP(request.Header.Get(header)); candidate != "" {
			return candidate
		}
	}
	forwarded, _, _ := strings.Cut(request.Header.Get("X-Forwarded-For"), ",")
	return exactIP(forwarded)
}

func exactIP(value string) string {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return ""
	}
	return ip.String()
}
