package emailauthoring

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
)

func emailTemplateAuthorizedAIDocumentFence(
	expectedDocumentID uuid.UUID,
	domain contentblock.DomainContext,
) contentblock.DomainFence {
	return func(_ context.Context, _ *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if documentID != expectedDocumentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Email Template content document changed; reload before saving")
		}
		return domain, nil
	}
}

func requireEmailTemplateAIDocumentAuthority(
	ctx context.Context,
	checker *auth.SpiceDBClient,
	templateID string,
) (uuid.UUID, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return uuid.Nil, errs.AuthenticationRequired()
	}
	if principal.Banned {
		return uuid.Nil, errs.AccountBanned()
	}
	if !principal.Onboarded {
		return uuid.Nil, errs.NoPermission("edit", "email template")
	}
	memberID, err := uuid.Parse(principal.MemberID.String())
	if err != nil || memberID == uuid.Nil || memberID.String() != principal.MemberID.String() {
		return uuid.Nil, errs.AuthenticationRequired()
	}
	can, err := emailTemplateEditCan(templateID)
	if err != nil {
		return uuid.Nil, errs.InvalidArgument("email_template_id", "must be a canonical UUID")
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return uuid.Nil, errs.AuthenticationRequired()
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return uuid.Nil, errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return uuid.Nil, errs.NoPermission("edit", "email template")
	}
	return memberID, nil
}

func lockEmailAIDocumentContributor(ctx context.Context, tx *gorm.DB, memberID uuid.UUID) error {
	if tx == nil || memberID == uuid.Nil {
		return errs.InvalidArgument("contributor_member_id", "is required")
	}
	var row struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	result := tx.WithContext(ctx).
		Table("member").
		Clauses(clause.Locking{Strength: "KEY SHARE"}).
		Select("id").
		Where("id = ?", memberID).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return errs.InvalidArgument("contributor_member_id", "does not identify a current Member")
	}
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	if row.ID != memberID {
		return errs.InternalMsg("Email authoring contributor lock returned inconsistent state")
	}
	return nil
}

func loadEmailTemplateAIDocumentSubject(
	ctx context.Context,
	db *gorm.DB,
	templateID string,
	locale string,
	forUpdate bool,
) (*string, bool, error) {
	var row struct{ Subject sql.NullString }
	query := db.WithContext(ctx).Table("email_template_translation").
		Select("subject").Where("entity_id = ? AND locale = ?", templateID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	result := query.Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if result.Error != nil {
		return nil, false, errs.Internal(result.Error)
	}
	if !row.Subject.Valid {
		return nil, true, nil
	}
	value := row.Subject.String
	return &value, true, nil
}

func applyEmailTemplateAIDocumentSubject(
	ctx context.Context,
	tx *gorm.DB,
	mutation AIDocumentMutation,
	now time.Time,
) (contentblock.MetadataEffect, error) {
	current, exists, err := loadEmailTemplateAIDocumentSubject(
		ctx, tx, mutation.TemplateID, mutation.Locale, true,
	)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if exists != mutation.ExpectedPresence {
		return contentblock.MetadataEffect{}, errs.FailedPrecondition("Email Template locale presence changed; reload before saving")
	}
	if !exists && mutation.Locale == mutation.ExpectedSource {
		return contentblock.MetadataEffect{}, errs.FailedPrecondition("Email Template source locale is missing")
	}
	next := cloneEmailAIDocumentString(current)
	if mutation.SetSubject {
		next = cloneEmailAIDocumentString(&mutation.Subject)
	}
	if mutation.Locale == mutation.ExpectedSource && (next == nil || strings.TrimSpace(*next) == "") {
		return contentblock.MetadataEffect{}, errs.InvalidArgument("subject", "Email Template source subject cannot be empty")
	}
	ensureLocale := !exists && mutation.Locale != mutation.ExpectedSource
	changed := ensureLocale || !emailAIDocumentStringEqual(current, next)
	if !changed {
		return contentblock.MetadataEffect{}, nil
	}
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO email_template_translation (
			entity_id, locale, subject, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			subject = EXCLUDED.subject, updated_at = EXCLUDED.updated_at`,
		mutation.TemplateID, mutation.Locale, next, now, now,
	).Error; err != nil {
		return contentblock.MetadataEffect{}, errs.Internal(err)
	}
	return contentblock.MetadataEffect{
		Changed: changed, AffectsTranslationSource: mutation.Locale == mutation.ExpectedSource,
		SourceLocale: mutation.ExpectedSource, ChangedLocales: []string{mutation.Locale},
	}, nil
}

func canonicalEmailAIDocumentID(field, value string) (string, error) {
	value = strings.TrimSpace(value)
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || id.String() != value {
		return "", errs.InvalidArgument(field, "must be a canonical UUID")
	}
	return value, nil
}

func emailAIDocumentStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
