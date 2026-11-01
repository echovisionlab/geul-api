package og

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

// Policy keeps the stable entity mapping used by generation lifecycle rows
// and queued jobs. LegacyOnly entities remain readable and cancellable, but
// cannot create new generation work.
type Policy struct {
	Name              string
	LocaleStrategy    LocaleStrategy
	CanonicalEntityID string
	LegacyOnly        bool
}

const (
	entitySite    = "site"
	entityRelease = "release"
)

var policies = [...]Policy{
	{"post", LocaleStrategyTranslated, "", false},
	{"page", LocaleStrategyTranslated, "", false},
	{"work", LocaleStrategyTranslated, "", false},
	{"label", LocaleStrategyBaseOnly, "", false},
	{"artist", LocaleStrategyTranslated, "", false},
	{entitySite, LocaleStrategyBaseOnly, SiteEntityID, false},
	{entityRelease, LocaleStrategyBaseOnly, "", true},
	{"series", LocaleStrategyTranslated, "", false},
	{"form", LocaleStrategyTranslated, "", false},
	{"privacy", LocaleStrategyStatic, PrivacyRouteEntityID, false},
	{"terms", LocaleStrategyStatic, TermsRouteEntityID, false},
}

func SupportsNewGeneration(policy Policy) bool {
	return !policy.LegacyOnly
}

func PolicyForEntityName(name string) (Policy, bool) {
	for _, policy := range policies {
		if policy.Name == name {
			return policy, true
		}
	}
	return Policy{}, false
}

func SupportsLocaleAware(entityType string) bool {
	policy, ok := PolicyForEntityName(entityType)
	return ok && policy.LocaleStrategy == LocaleStrategyTranslated
}
