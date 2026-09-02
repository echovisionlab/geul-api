package campaign

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

type campaignDeliveryTemplateData struct {
	EffectiveDate *string `json:"effective_date,omitempty"`
	PolicyTitle   *string `json:"policy_title,omitempty"`
	PreviewURL    *string `json:"preview_url,omitempty"`
	TermsURL      *string `json:"terms_url,omitempty"`
	PrivacyURL    *string `json:"privacy_url,omitempty"`
}

type strictCampaignDeliverySnapshotTranslation struct {
	Locale      *string `json:"locale"`
	Subject     *string `json:"subject"`
	ContentHTML *string `json:"content_html"`
}

type strictCampaignDeliverySnapshotLayout struct {
	Locale      *string `json:"locale"`
	HTMLContent *string `json:"html_content"`
}

type strictCampaignDeliverySnapshot struct {
	Subject            *string                                      `json:"subject"`
	ContentHTML        *string                                      `json:"content_html"`
	SourceLocale       *string                                      `json:"source_locale"`
	Translations       *[]strictCampaignDeliverySnapshotTranslation `json:"translations"`
	LayoutSourceLocale *string                                      `json:"layout_source_locale"`
	LayoutTranslations *[]strictCampaignDeliverySnapshotLayout      `json:"layout_translations"`
}

var campaignDeliverySnapshotKeys = map[string]struct{}{
	"subject":              {},
	"content_html":         {},
	"source_locale":        {},
	"translations":         {},
	"layout_source_locale": {},
	"layout_translations":  {},
}

func decodeCampaignDeliverySnapshotJSON(raw []byte) (CampaignDeliverySnapshot, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return CampaignDeliverySnapshot{}, fmt.Errorf("render snapshot is not valid JSON: %w", err)
	}
	for key := range object {
		if _, ok := campaignDeliverySnapshotKeys[key]; !ok {
			return CampaignDeliverySnapshot{}, fmt.Errorf("render snapshot contains unknown key %q", key)
		}
	}
	for _, key := range []string{"subject", "content_html", "source_locale", "translations"} {
		if _, ok := object[key]; !ok {
			return CampaignDeliverySnapshot{}, fmt.Errorf("render snapshot is missing required key %q", key)
		}
	}
	_, hasLayoutLocale := object["layout_source_locale"]
	_, hasLayoutTranslations := object["layout_translations"]
	if hasLayoutLocale != hasLayoutTranslations {
		return CampaignDeliverySnapshot{}, fmt.Errorf(
			"render snapshot layout source locale and translations must appear together",
		)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var strict strictCampaignDeliverySnapshot
	if err := decoder.Decode(&strict); err != nil {
		return CampaignDeliverySnapshot{}, fmt.Errorf("render snapshot is structurally invalid: %w", err)
	}
	if strict.Subject == nil || strict.ContentHTML == nil || strict.SourceLocale == nil || strict.Translations == nil {
		return CampaignDeliverySnapshot{}, fmt.Errorf("render snapshot required values must not be null")
	}
	snapshot := CampaignDeliverySnapshot{
		Subject:      *strict.Subject,
		ContentHTML:  *strict.ContentHTML,
		SourceLocale: *strict.SourceLocale,
		Translations: make([]CampaignDeliverySnapshotTranslation, 0, len(*strict.Translations)),
	}
	for index, translation := range *strict.Translations {
		if translation.Locale == nil || translation.Subject == nil || translation.ContentHTML == nil {
			return CampaignDeliverySnapshot{}, fmt.Errorf(
				"render snapshot translation %d is missing required values",
				index,
			)
		}
		snapshot.Translations = append(snapshot.Translations, CampaignDeliverySnapshotTranslation{
			Locale:      *translation.Locale,
			Subject:     *translation.Subject,
			ContentHTML: *translation.ContentHTML,
		})
	}
	if hasLayoutLocale {
		if strict.LayoutSourceLocale == nil || strict.LayoutTranslations == nil {
			return CampaignDeliverySnapshot{}, fmt.Errorf("render snapshot layout values must not be null")
		}
		layouts := make([]CampaignDeliverySnapshotLayout, 0, len(*strict.LayoutTranslations))
		for index, translation := range *strict.LayoutTranslations {
			if translation.Locale == nil || translation.HTMLContent == nil {
				return CampaignDeliverySnapshot{}, fmt.Errorf(
					"render snapshot layout translation %d is missing required values",
					index,
				)
			}
			layouts = append(layouts, CampaignDeliverySnapshotLayout{
				Locale:      *translation.Locale,
				HTMLContent: *translation.HTMLContent,
			})
		}
		snapshot.LayoutSourceLocale = strict.LayoutSourceLocale
		snapshot.LayoutTranslations = &layouts
	}
	if err := ValidateCampaignDeliverySnapshot(snapshot); err != nil {
		return CampaignDeliverySnapshot{}, err
	}
	return snapshot, nil
}

func decodeCampaignDeliverySnapshot(raw model.JSONFields) (CampaignDeliverySnapshot, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return CampaignDeliverySnapshot{}, fmt.Errorf("render snapshot is not valid JSON: %w", err)
	}
	return decodeCampaignDeliverySnapshotJSON(encoded)
}

func decodeCampaignDeliveryTemplateData(
	runKind string,
	eventKey string,
	raw model.JSONFields,
) (campaignDeliveryTemplateData, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return campaignDeliveryTemplateData{}, fmt.Errorf("template data is not valid JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var object structured.Fields
	if err := decoder.Decode(&object); err != nil {
		return campaignDeliveryTemplateData{}, fmt.Errorf("template data is not a JSON object: %w", err)
	}
	if object == nil {
		return campaignDeliveryTemplateData{}, fmt.Errorf("template data must be a JSON object")
	}
	values := make(map[string]string, len(object))
	for key, value := range object {
		stringValue, ok := value.(string)
		if !ok {
			return campaignDeliveryTemplateData{}, fmt.Errorf("template data value %q must be a string", key)
		}
		values[key] = stringValue
	}
	return newCampaignDeliveryTemplateData(runKind, eventKey, values)
}

func newCampaignDeliveryTemplateData(
	runKind string,
	eventKey string,
	values map[string]string,
) (campaignDeliveryTemplateData, error) {
	var data campaignDeliveryTemplateData
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
			return campaignDeliveryTemplateData{}, fmt.Errorf("template data contains unknown key %q", key)
		}
	}
	if err := validateCampaignDeliveryTemplateData(runKind, eventKey, data); err != nil {
		return campaignDeliveryTemplateData{}, err
	}
	return data, nil
}

func validateCampaignDeliveryTemplateData(
	runKind string,
	eventKey string,
	data campaignDeliveryTemplateData,
) error {
	present := func(value *string) bool {
		return value != nil && strings.TrimSpace(*value) != ""
	}
	noValues := data.EffectiveDate == nil && data.PolicyTitle == nil &&
		data.PreviewURL == nil && data.TermsURL == nil && data.PrivacyURL == nil
	if runKind == EmailDeliveryRunKindCampaign {
		if !noValues {
			return fmt.Errorf("campaign template data must be empty")
		}
		return nil
	}
	if runKind != EmailDeliveryRunKindLegalNotice {
		return fmt.Errorf("unsupported email delivery run kind %q", runKind)
	}

	switch eventKey {
	case "terms_update", "privacy_update":
		if !present(data.PolicyTitle) || !present(data.EffectiveDate) || !present(data.PreviewURL) ||
			data.TermsURL != nil || data.PrivacyURL != nil {
			return fmt.Errorf(
				"%s template data requires only policy_title, effective_date and preview_url",
				eventKey,
			)
		}
	case "terms_effective":
		if !present(data.TermsURL) || data.EffectiveDate != nil || data.PolicyTitle != nil ||
			data.PreviewURL != nil || data.PrivacyURL != nil {
			return fmt.Errorf("terms_effective template data requires only terms_url")
		}
	case "privacy_effective":
		if !present(data.PrivacyURL) || data.EffectiveDate != nil || data.PolicyTitle != nil ||
			data.PreviewURL != nil || data.TermsURL != nil {
			return fmt.Errorf("privacy_effective template data requires only privacy_url")
		}
	default:
		return fmt.Errorf("unsupported legal notice template event key %q", eventKey)
	}
	return nil
}

// ValidateEmailDeliveryRenderDefinition rejects mutable, unknown, or
// structurally incomplete data before a worker renders a sealed run.
func ValidateEmailDeliveryRenderDefinition(run model.CampaignDeliveryRun) error {
	if !run.DefinitionSealed {
		return fmt.Errorf("email delivery run definition is not sealed")
	}
	if run.SnapshotSchemaVersion != CampaignDeliverySnapshotSchemaVersion {
		return fmt.Errorf(
			"unsupported email delivery snapshot schema version: %d",
			run.SnapshotSchemaVersion,
		)
	}
	switch run.RunKind {
	case EmailDeliveryRunKindCampaign, EmailDeliveryRunKindLegalNotice:
	default:
		return fmt.Errorf("unsupported email delivery run kind %q", run.RunKind)
	}
	if _, err := decodeCampaignDeliverySnapshot(run.RenderSnapshot); err != nil {
		return err
	}
	eventKey := strings.TrimSpace(ptrStringValue(run.TemplateEventKey))
	data, err := decodeCampaignDeliveryTemplateData(run.RunKind, eventKey, run.TemplateData)
	if err != nil {
		return err
	}
	return validateCampaignDeliveryTemplateData(run.RunKind, eventKey, data)
}

func CampaignDeliveryRunTemplateData(run model.CampaignDeliveryRun) (map[string]string, error) {
	data, err := decodeCampaignDeliveryTemplateData(
		run.RunKind,
		strings.TrimSpace(ptrStringValue(run.TemplateEventKey)),
		run.TemplateData,
	)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, 5)
	if data.PolicyTitle != nil {
		result["policy_title"] = *data.PolicyTitle
	}
	if data.EffectiveDate != nil {
		result["effective_date"] = *data.EffectiveDate
	}
	if data.PreviewURL != nil {
		result["preview_url"] = *data.PreviewURL
	}
	if data.TermsURL != nil {
		result["terms_url"] = *data.TermsURL
	}
	if data.PrivacyURL != nil {
		result["privacy_url"] = *data.PrivacyURL
	}
	return result, nil
}

func EmailDeliveryRunTemplateType(run model.CampaignDeliveryRun) string {
	if eventKey := strings.TrimSpace(ptrStringValue(run.TemplateEventKey)); eventKey != "" {
		return eventKey
	}
	if run.RunKind == EmailDeliveryRunKindCampaign {
		if campaignID := strings.TrimSpace(ptrStringValue(run.CampaignID)); campaignID != "" {
			return fmt.Sprintf("campaign:%s", campaignID)
		}
	}
	return ""
}

func EmailDeliveryRunReferenceID(run model.CampaignDeliveryRun) string {
	switch strings.TrimSpace(run.RunKind) {
	case EmailDeliveryRunKindCampaign:
		return strings.TrimSpace(ptrStringValue(run.CampaignID))
	case EmailDeliveryRunKindLegalNotice:
		if termsID := strings.TrimSpace(ptrStringValue(run.TermsID)); termsID != "" {
			return termsID
		}
		return strings.TrimSpace(ptrStringValue(run.PrivacyID))
	default:
		return ""
	}
}
