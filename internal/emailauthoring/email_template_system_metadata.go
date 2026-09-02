package emailauthoring

import (
	"fmt"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
)

var (
	emailTemplateSiteVariables = []string{
		"site_name",
		"site_origin",
		"logo_email_url",
	}
	emailTemplateNameAliasVariables = []string{
		"name",
		"recipient_name",
		"identity_name",
	}
	emailTemplateEmailAliasVariables = []string{
		"recipient_email",
		"identity_email",
		"to",
	}
)

// verifiedSystemEmailTemplateVariableNames returns the variables that are
// actually available to runtime rendering for built-in system templates.
//
// The source of truth is the backend send path:
//   - direct events publish concrete TemplateData keys
//   - bulk events add recipient_email before enqueue
//   - BuildEmailRenderData adds site_* / logo_email_url and alias normalization
//
// We intentionally do not include speculative variables here.
func verifiedSystemEmailTemplateVariableNames(eventKey interface{ String() string }) []string {
	names := append([]string{}, emailTemplateSiteVariables...)
	key := eventKey.String()

	switch key {
	case eventAccountDeletionConfirm.String():
		names = append(names, emailTemplateNameAliasVariables...)
		names = append(names, "confirm_url", "expires_in")
	case eventAccountDeletionScheduled.String():
		names = append(names, emailTemplateNameAliasVariables...)
		names = append(names, "scheduled_date", "grace_period", "cancel_url", "recover_url")
	case eventAccountDeletionCancelled.String():
		names = append(names, emailTemplateNameAliasVariables...)
		names = append(names, "login_url")
	case eventAccountDeletionComplete.String():
		names = append(names, emailTemplateNameAliasVariables...)
	case eventAccountRecoveryConfirm.String():
		names = append(names, emailTemplateNameAliasVariables...)
		names = append(names, "confirm_url", "expires_in")
	case eventAccountRecoveryComplete.String():
		names = append(names, emailTemplateNameAliasVariables...)
		names = append(names, "login_url")
	case eventPrimaryEmailChanged.String():
		names = append(names, "old_email", "new_email")
	case eventEmailAdded.String(), eventEmailRemoved.String():
		names = append(names, "email")
	case eventSocialLoginAdded.String(), eventSocialLoginRemoved.String():
		names = append(names, "provider")
	case eventPasskeyAdded.String(), eventPasskeyRemoved.String():
	case eventWelcome.String():
		names = append(names, emailTemplateNameAliasVariables...)
		names = append(names, "login_url")
	case eventTermsUpdate.String():
		names = append(names, emailTemplateEmailAliasVariables...)
		names = append(names, "policy_title", "effective_date", "preview_url")
	case eventTermsEffective.String():
		names = append(names, emailTemplateEmailAliasVariables...)
		names = append(names, "terms_url")
	case eventPrivacyUpdate.String():
		names = append(names, emailTemplateEmailAliasVariables...)
		names = append(names, "policy_title", "effective_date", "preview_url")
	case eventPrivacyEffective.String():
		names = append(names, emailTemplateEmailAliasVariables...)
		names = append(names, "privacy_url")
	case eventVerificationCode.String():
		names = append(names, emailTemplateEmailAliasVariables...)
		names = append(names, "verification_code", "verification_url", "expires_in_minutes")
	case eventLoginCode.String():
		names = append(names, emailTemplateEmailAliasVariables...)
		names = append(names, "login_code", "expires_in_minutes")
	case eventRegistrationCode.String():
		names = append(names, emailTemplateEmailAliasVariables...)
		names = append(names, "registration_code", "expires_in_minutes")
	}

	return uniqueEmailTemplateVariableNames(names)
}

func ValidateAutomaticEmailTemplateData(eventKey string, data map[string]string) error {
	eventKey = strings.TrimSpace(eventKey)
	var matched automaticEmailEventKey
	found := false
	for _, candidate := range automaticEmailEventKeys() {
		if candidate.String() == eventKey {
			matched = candidate
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	allowed := map[string]struct{}{}
	for _, name := range verifiedSystemEmailTemplateVariableNames(matched) {
		allowed[name] = struct{}{}
	}
	unknown := make([]string, 0)
	for name := range data {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if _, ok := allowed[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	slices.Sort(unknown)
	return fmt.Errorf("automatic email %s has unknown template variables: %s", eventKey, strings.Join(unknown, ", "))
}

func verifiedSystemEmailTemplateVariables(eventKey interface{ String() string }) model.EmailTemplateVariables {
	names := verifiedSystemEmailTemplateVariableNames(eventKey)
	vars := make(model.EmailTemplateVariables, 0, len(names))
	for _, name := range names {
		vars = append(vars, model.EmailTemplateVariable{Name: name})
	}
	return vars
}

func verifiedSystemEmailTemplateMetadata(templateKey string) (*string, model.EmailTemplateVariables, bool) {
	templateKey = strings.TrimSpace(templateKey)
	for _, eventKey := range automaticEmailEventKeys() {
		if eventKey.String() != templateKey {
			continue
		}
		eventKeyValue := eventKey.String()
		return &eventKeyValue, verifiedSystemEmailTemplateVariables(eventKey), true
	}

	return nil, nil, false
}

func uniqueEmailTemplateVariableNames(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	result := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(strings.ToLower(name))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	return result
}
