package translationadapter

import sharedtelemetry "github.com/echovisionlab/geul-telemetry"

// LocaleContentAuditBuilder constructs the shared audit record for a localized
// content mutation. It is kept at the translation boundary so every remaining
// document domain uses the same audit contract.
type LocaleContentAuditBuilder func(
	sharedtelemetry.AuditMetadata,
	string,
	string,
	sharedtelemetry.AuditItemOperation,
) (sharedtelemetry.AuditRecord, error)
