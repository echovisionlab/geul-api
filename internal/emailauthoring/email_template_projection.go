package emailauthoring

import (
	"strings"
)

// populateSystemEmailTemplateMetadata fills a system template's event key
// and variables from the automatic email catalog when the row has none.
func populateSystemEmailTemplateMetadata(template *Template) {
	if !template.IsSystem {
		return
	}
	eventKey, variables, ok := verifiedSystemEmailTemplateMetadata(
		strings.TrimSpace(firstNonEmptyString(ptrStringValue(template.EventKey), template.Key)),
	)
	if !ok {
		return
	}
	if template.EventKey == nil || strings.TrimSpace(*template.EventKey) == "" {
		template.EventKey = eventKey
	}
	if len(template.Variables) == 0 {
		template.Variables = variables
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func ptrStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
