package artist

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func appendArtistMemberTargetLocaleAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	artistID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	return domainaudit.AppendMember(
		ctx,
		tx,
		writer,
		memberID,
		sharedtelemetry.AuditArtistUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewArtistLocaleContentAuditRecord(metadata, artistID, locale, operation)
		},
	)
}
