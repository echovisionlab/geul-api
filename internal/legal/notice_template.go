package legal

import (
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
)

type emailDeliveryTemplateData struct {
	EffectiveDate *string
	PolicyTitle   *string
	PreviewURL    *string
	TermsURL      *string
	PrivacyURL    *string
}

func decodeEmailDeliveryTemplateData(
	runKind string,
	eventKey string,
	raw model.JSONFields,
) (emailDeliveryTemplateData, error) {
	values := make(map[string]string, len(raw))
	for key, value := range raw {
		stringValue, ok := value.(string)
		if !ok {
			return emailDeliveryTemplateData{}, fmt.Errorf("template data value %q must be a string", key)
		}
		values[key] = stringValue
	}
	return newEmailDeliveryTemplateData(runKind, eventKey, values)
}

func newEmailDeliveryTemplateData(
	runKind string,
	eventKey string,
	values map[string]string,
) (emailDeliveryTemplateData, error) {
	var data emailDeliveryTemplateData
	for key, value := range values {
		valueCopy := value
		switch key {
		case "effective_date":
			data.EffectiveDate = &valueCopy
		case "policy_title":
			data.PolicyTitle = &valueCopy
		case "preview_url":
			data.PreviewURL = &valueCopy
		case "terms_url":
			data.TermsURL = &valueCopy
		case "privacy_url":
			data.PrivacyURL = &valueCopy
		default:
			return emailDeliveryTemplateData{}, fmt.Errorf("template data contains unknown key %q", key)
		}
	}
	if err := validateEmailDeliveryTemplateData(runKind, eventKey, data); err != nil {
		return emailDeliveryTemplateData{}, err
	}
	return data, nil
}

func validateEmailDeliveryTemplateData(
	runKind string,
	eventKey string,
	data emailDeliveryTemplateData,
) error {
	present := func(value *string) bool { return value != nil && strings.TrimSpace(*value) != "" }
	if runKind != EmailDeliveryRunKindLegalNotice {
		return fmt.Errorf("unsupported email delivery run kind %q", runKind)
	}
	switch eventKey {
	case "terms_update", "privacy_update":
		if !present(data.PolicyTitle) || !present(data.EffectiveDate) || !present(data.PreviewURL) ||
			data.TermsURL != nil || data.PrivacyURL != nil {
			return fmt.Errorf("%s template data requires only policy_title, effective_date and preview_url", eventKey)
		}
	case "terms_effective":
		if !present(data.TermsURL) || data.EffectiveDate != nil || data.PolicyTitle != nil || data.PreviewURL != nil || data.PrivacyURL != nil {
			return fmt.Errorf("terms_effective template data requires only terms_url")
		}
	case "privacy_effective":
		if !present(data.PrivacyURL) || data.EffectiveDate != nil || data.PolicyTitle != nil || data.PreviewURL != nil || data.TermsURL != nil {
			return fmt.Errorf("privacy_effective template data requires only privacy_url")
		}
	default:
		return fmt.Errorf("unsupported legal notice template event key %q", eventKey)
	}
	return nil
}
