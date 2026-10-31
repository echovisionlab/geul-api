// Package emailcontent holds the email content shared by Campaigns and Email
// Templates: a subject and an Email profile Rich Text document per locale,
// their target-locale writes, the AI document mutation seam, XLIFF
// interchange and typed translation apply. Each owner supplies an Owner that
// names its rows and wording and adds its own rules.
package emailcontent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Profile is the content document profile of every email content owner.
const Profile = "email"

// Owner is one kind of email content owner, a Campaign or an Email Template:
// its rows, the wording its clients have always seen, and the rules it adds
// to a shared write.
type Owner struct {
	// Name names the owner in client messages: "Campaign".
	Name string
	// Entity is the owner's table and content entity type; its locale rows
	// are in Entity+"_translation".
	Entity string
	// IDField names an owner ID in invalid-argument answers.
	IDField string
	// FileBlocksRejected answers a File Block in the owner's document.
	FileBlocksRejected string
	// TranslatedSubjectRequired answers a translation without a subject.
	TranslatedSubjectRequired string
	// ContentEntityType names the owner in content.updated signals.
	ContentEntityType managev1.ContentEntityType
	// Rows reads and projects the owner's own rows.
	Rows Rows
	// LocaleContentAudit is the action and record of a change to one
	// locale's content.
	LocaleContentAudit LocaleContentAudit
}

// Rows reads and projects the rows an owner keeps beside its email content.
type Rows interface {
	// DocumentID reads the owner's content document.
	DocumentID(ctx context.Context, db *gorm.DB, id string) (uuid.UUID, error)
	// SourceContext reads the owner's translation source locale, locking
	// the owner row when lock is set.
	SourceContext(ctx context.Context, db *gorm.DB, id string, lock bool) (contentblock.DomainContext, error)
	// ProjectSource rebuilds the owner's materialized content from
	// snapshot after a source locale write; sourceChanged reports that the
	// write changed the translation source every target falls back to.
	ProjectSource(ctx context.Context, tx *gorm.DB, id string, snapshot contentblock.Snapshot, sourceChanged bool, now time.Time) error
}

// LocaleContentAudit names the record of a locale content change.
type LocaleContentAudit struct {
	Action sharedtelemetry.AuditAction
	Record func(metadata sharedtelemetry.AuditMetadata, id, locale string, operation sharedtelemetry.AuditItemOperation) (sharedtelemetry.AuditRecord, error)
}

func (o Owner) translationTable() string { return o.Entity + "_translation" }

// appendLocaleContentAudit records a locale content change as memberID.
func (o Owner) appendLocaleContentAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	id string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	return domainaudit.AppendMember(ctx, tx, writer, memberID, o.LocaleContentAudit.Action, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return o.LocaleContentAudit.Record(metadata, id, locale, operation)
	})
}

// LocaleContentOperation is the audited operation of a locale content
// change: a source edit updates, a removed locale is deleted, and a created
// or previously absent locale is created.
func LocaleContentOperation(source, create, remove, previouslyExists bool) sharedtelemetry.AuditItemOperation {
	switch {
	case source:
		return sharedtelemetry.AuditItemOperationUpdated
	case remove:
		return sharedtelemetry.AuditItemOperationDeleted
	case create || !previouslyExists:
		return sharedtelemetry.AuditItemOperationCreated
	default:
		return sharedtelemetry.AuditItemOperationUpdated
	}
}

// CanonicalID returns value when it is a canonical UUID; field names it in
// the rejection.
func CanonicalID(field, value string) (string, error) {
	value = strings.TrimSpace(value)
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || id.String() != value {
		return "", errs.InvalidArgument(field, "must be a canonical UUID")
	}
	return value, nil
}

// NormalizeLocale returns the exact supported locale value names.
func NormalizeLocale(value string) (string, error) {
	locale := localization.NormalizeExactSupportedLocale(value)
	if locale == nil {
		return "", errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	return *locale, nil
}

// NormalizeContentBlockError answers a content block store failure of the
// owner's document; an already classified error passes through.
func (o Owner) NormalizeContentBlockError(err error) error {
	if err == nil || connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	var targetConflict *translation.TargetRevisionConflict
	if errors.As(err, &targetConflict) {
		return errs.CollaborationConflict(intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED, targetConflict.Error(), err)
	}
	switch {
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return errs.WithCause(errs.NotFoundMsg(o.Entity+" content document not found"), err)
	case errors.Is(err, contentblock.ErrStaleRevision):
		return errs.CollaborationConflict(intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED, o.Entity+" content revision changed; reload before saving", err)
	case errors.Is(err, contentblock.ErrCrossDocument):
		return errs.WithCause(errs.InvalidArgument("blocks", "a Block belongs to another document"), err)
	case errors.Is(err, contentblock.ErrFileReference):
		return errs.WithCause(errs.InvalidArgument("blocks", o.FileBlocksRejected), err)
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return errs.InvalidArgument("blocks", err.Error())
	default:
		return errs.Internal(fmt.Errorf("%s content document: %w", o.Entity, err))
	}
}

// touch advances the owner row's updated_at after its content changed.
func (o Owner) touch(ctx context.Context, tx *gorm.DB, id string, now time.Time) error {
	if err := tx.WithContext(ctx).Table(o.Entity).Where("id = ?", id).Update("updated_at", now).Error; err != nil {
		return errs.Internal(err)
	}
	return nil
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func stringsEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
