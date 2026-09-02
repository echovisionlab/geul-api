package public

import "github.com/echovisionlab/geul-api/internal/publiccontent"

var privacyLocalizationSpec = publiccontent.Spec{
	EntityType:   "privacy",
	TableName:    "privacy_translation",
	SelectClause: "locale, title",
}

var termsLocalizationSpec = publiccontent.Spec{
	EntityType:   "terms",
	TableName:    "terms_translation",
	SelectClause: "locale, title",
}

func legalLocalizationSpec(entityType string) publiccontent.Spec {
	if entityType == "privacy" {
		return privacyLocalizationSpec
	}
	return termsLocalizationSpec
}
