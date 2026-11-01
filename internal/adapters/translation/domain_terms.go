package translationadapter

import (
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func termsDomainRegistration(audit domainaudit.Appender) domainRegistration {
	return legalDomainRegistration(
		core.KindTerms,
		audit,
		policyv1.TermsHistory.Edit,
		legalSourceLocaleAudit(
			"terms_history",
			"terms",
			sharedtelemetry.NewTermsSourceLocaleAuditRecord,
		),
	)
}
