package authentication

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

func buildRegistrationCodePayload(
	body []byte,
	contentType string,
	flow unifiedAuthFlow,
	initialChallenge bool,
) ([]byte, error) {
	method, code, email := authCodeRequestFields(contentType, body)
	if !strings.EqualFold(method, "code") {
		if initialChallenge {
			return nil, errors.New("registration requires an email code request")
		}
		return body, nil
	}
	if !initialChallenge {
		if flowEmail := flow.nodeString("traits.email"); flowEmail != "" {
			email = flowEmail
		}
	}
	if email == "" {
		if initialChallenge {
			return nil, errors.New("registration requires an email code request")
		}
		return nil, errors.New("registration email is missing")
	}
	transient, err := requestTransientPayload(contentType, body)
	if err != nil {
		return nil, err
	}
	if len(transient) == 0 && len(flow.TransientPayload) != 0 {
		transient = flow.TransientPayload
	}
	transient, err = validateRegistrationTransientPayload(transient)
	if err != nil {
		return nil, err
	}
	payload := unifiedAuthObject{
		"method":            "code",
		"csrf_token":        flow.nodeString("csrf_token"),
		"traits":            registrationTraits(email),
		"transient_payload": transient,
	}
	if !initialChallenge && strings.TrimSpace(code) != "" {
		payload["code"] = strings.TrimSpace(code)
	}
	return json.Marshal(payload)
}

func registrationTraits(email string) unifiedAuthObject {
	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	return unifiedAuthObject{
		"email": normalizedEmail,
	}
}

func validateRegistrationTransientPayload(transient unifiedAuthObject) (unifiedAuthObject, error) {
	normalized := make(unifiedAuthObject)
	if value, exists := transient["preferred_locale"]; exists && value != nil {
		locale, valid := value.(string)
		if !valid {
			return nil, fmt.Errorf("registration preferred_locale must be a string")
		}
		if locale = strings.TrimSpace(locale); locale != "" {
			normalized["preferred_locale"] = locale
		}
	}
	return normalized, nil
}

func requestTransientPayload(contentType string, body []byte) (unifiedAuthObject, error) {
	mediaType := kratosRequestMediaType(contentType)
	if mediaType == "application/json" {
		var payload unifiedAuthObject
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		return authCodeTransientPayload(payload["transient_payload"])
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	return authCodeTransientPayload(values.Get("transient_payload"))
}
