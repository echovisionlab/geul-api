package artist

import (
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// artistMemberLocaleAudit attributes a locale content change to memberID.
func artistMemberLocaleAudit(memberID, artistID, locale string, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
	return mutation.MemberAudit(memberID, sharedtelemetry.AuditArtistUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewArtistLocaleContentAuditRecord(metadata, artistID, locale, operation)
	})
}
