package emailauthoring

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
)

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
		return uuid.Nil, errs.WithCause(errs.AuthenticationRequired(), err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return uuid.Nil, errs.WithCause(errs.DependencyUnavailable("SpiceDB"), err)
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
