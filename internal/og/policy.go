package og

import managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"

const (
	SiteEntityID         = "default"
	PrivacyRouteEntityID = "00000000-0000-0000-0000-000000000101"
	TermsRouteEntityID   = "00000000-0000-0000-0000-000000000102"
)

type LocaleStrategy int

const (
	LocaleStrategyBaseOnly LocaleStrategy = iota + 1
	LocaleStrategyTranslated
	LocaleStrategyStatic
)

// ogGenerationPolicy keeps the stable entity mapping used by generation
// lifecycle rows and queued jobs. legacyOnly entities remain readable and
// cancellable, but cannot create new generation work.
type Policy struct {
	EntityType        managev1.OgEntityType
	Name              string
	LocaleStrategy    LocaleStrategy
	CanonicalEntityID string
	LegacyOnly        bool
}

var policies = [...]Policy{
	{managev1.OgEntityType_OG_ENTITY_TYPE_POST, "post", LocaleStrategyTranslated, "", false},
	{managev1.OgEntityType_OG_ENTITY_TYPE_PAGE, "page", LocaleStrategyTranslated, "", false},
	{managev1.OgEntityType_OG_ENTITY_TYPE_WORK, "work", LocaleStrategyTranslated, "", false},
	{managev1.OgEntityType_OG_ENTITY_TYPE_SITE, "site", LocaleStrategyBaseOnly, SiteEntityID, false},
	{managev1.OgEntityType_OG_ENTITY_TYPE_SERIES, "series", LocaleStrategyTranslated, "", false},
	{managev1.OgEntityType_OG_ENTITY_TYPE_FORM, "form", LocaleStrategyTranslated, "", false},
	{managev1.OgEntityType_OG_ENTITY_TYPE_PRIVACY, "privacy", LocaleStrategyStatic, PrivacyRouteEntityID, false},
	{managev1.OgEntityType_OG_ENTITY_TYPE_TERMS, "terms", LocaleStrategyStatic, TermsRouteEntityID, false},
}

func SupportsNewGeneration(policy Policy) bool {
	return !policy.LegacyOnly
}

func PolicyForEntityType(entityType managev1.OgEntityType) (Policy, bool) {
	for _, policy := range policies {
		if policy.EntityType == entityType {
			return policy, true
		}
	}
	return Policy{}, false
}

func PolicyForEntityName(name string) (Policy, bool) {
	for _, policy := range policies {
		if policy.Name == name {
			return policy, true
		}
	}
	return Policy{}, false
}

func EntityTypeForName(entityType string) managev1.OgEntityType {
	policy, ok := PolicyForEntityName(entityType)
	if !ok {
		return managev1.OgEntityType_OG_ENTITY_TYPE_UNSPECIFIED
	}
	return policy.EntityType
}

func SupportsLocaleAware(entityType string) bool {
	policy, ok := PolicyForEntityName(entityType)
	return ok && policy.LocaleStrategy == LocaleStrategyTranslated
}
