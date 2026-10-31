package post

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func postLocaleContentRecord(postID, locale string, operation sharedtelemetry.AuditItemOperation) domainaudit.Builder {
	return func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostLocaleContentAuditRecord(metadata, postID, locale, operation)
	}
}

// postLocaleContentAudit attributes a locale content change to the Member
// whose collaboration or AI edit made it.
func postLocaleContentAudit(memberID, postID, locale string, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
	return mutation.MemberAudit(memberID, sharedtelemetry.AuditPostUpdated, postLocaleContentRecord(postID, locale, operation))
}

// appendPostMemberLocaleContentAudit appends the record of a translation
// the requesting Member's job delivered, in the translation application's
// transaction.
func appendPostMemberLocaleContentAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	postID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	return domainaudit.AppendMember(ctx, tx, writer, memberID, sharedtelemetry.AuditPostUpdated, postLocaleContentRecord(postID, locale, operation))
}
