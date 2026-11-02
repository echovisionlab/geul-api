package translationadapter

import (
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
)

// defaultDomainRegistrations is the production composition root. The
// registry validates that this list contains every catalog domain exactly
// once before the server can start.
func defaultDomainRegistrations(
	emailReferences emailauthoringpostgres.DeliveryReferences,
	audit domainaudit.Appender,
) []domainRegistration {
	return []domainRegistration{
		postDomainRegistration(audit),
		pageDomainRegistration(audit),
		workDomainRegistration(audit),
		programEventDomainRegistration(audit),
		releaseDomainRegistration(audit),
		artistDomainRegistration(audit),
		labelDomainRegistration(audit),
		menuDomainRegistration(audit),
		emailTemplateDomainRegistration(emailReferences, audit),
		emailLayoutDomainRegistration(emailReferences, audit),
		privacyDomainRegistration(audit),
		termsDomainRegistration(audit),
		campaignDomainRegistration(audit),
		formDomainRegistration(audit),
		seriesDomainRegistration(audit),
	}
}
