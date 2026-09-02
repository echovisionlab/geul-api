package translationadapter

import (
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
)

// defaultDomainRegistrations is the production composition root. The
// registry validates that this list contains every catalog domain exactly
// once before the server can start.
func defaultDomainRegistrations(
	emailReferences emailauthoring.CampaignDeliveryReferences,
	auditWriter domainaudit.Appender,
) []domainRegistration {
	return []domainRegistration{
		postDomainRegistration(auditWriter),
		pageDomainRegistration(auditWriter),
		workDomainRegistration(auditWriter),
		programEventDomainRegistration(auditWriter),
		menuDomainRegistration(auditWriter),
		emailTemplateDomainRegistration(emailReferences, auditWriter),
		emailLayoutDomainRegistration(emailReferences, auditWriter),
		privacyDomainRegistration(auditWriter),
		termsDomainRegistration(auditWriter),
		campaignDomainRegistration(auditWriter),
		formDomainRegistration(auditWriter),
		seriesDomainRegistration(auditWriter),
	}
}
