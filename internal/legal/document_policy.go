package legal

import (
	"context"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type legalDocumentPolicy struct {
	table           string
	draftStatus     string
	scheduledStatus string
	archivedStatus  string
}

func legalDocumentPolicyForType(entityType string) (legalDocumentPolicy, error) {
	switch entityType {
	case "terms":
		return legalDocumentPolicy{
			table:           "terms_history",
			draftStatus:     managev1.TermsStatus_TERMS_STATUS_DRAFT.String(),
			scheduledStatus: managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(),
			archivedStatus:  managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String(),
		}, nil
	case "privacy":
		return legalDocumentPolicy{
			table:           "privacy_history",
			draftStatus:     managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT.String(),
			scheduledStatus: managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
			archivedStatus:  managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String(),
		}, nil
	default:
		return legalDocumentPolicy{}, errs.InternalMsg("unsupported legal document type")
	}
}

func requireLegalPermission(
	ctx context.Context,
	checker CollaborationPermissionChecker,
	entityType string,
	entityID string,
	action legalAction,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned {
		return errs.AuthenticationRequired()
	}
	return requireLegalPermissionForPrincipal(
		ctx, checker, legalCollaborationResourceType(entityType), entityID, action, principal,
	)
}

func requireActiveLegalPrincipal(
	ctx context.Context,
	tx *gorm.DB,
	actionName string,
	maskNotFound bool,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return errs.AuthenticationRequired()
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return errs.Internal(err)
	}
	if active {
		return nil
	}
	if maskNotFound {
		return errs.NotFoundMsg("legal policy not found")
	}
	return errs.NoPermission(actionName, "legal policy")
}

func legalViewAction(policy legalDocumentPolicy, status string) legalAction {
	if status == policy.archivedStatus {
		return legalActionViewArchived
	}
	return legalActionView
}

func legalMutationAction(
	policy legalDocumentPolicy,
	status string,
	normal legalAction,
) legalAction {
	if status == policy.archivedStatus {
		return legalActionEditArchived
	}
	return normal
}

// requireLegalVersionViewOrNotFound selects the exact object read permission
// from the already loaded lifecycle. Denials intentionally appear absent.
func requireLegalVersionViewOrNotFound(
	ctx context.Context,
	tx *gorm.DB,
	checker *auth.SpiceDBClient,
	entityType string,
	entityID string,
	status string,
) error {
	policy, err := legalDocumentPolicyForType(entityType)
	if err != nil {
		return err
	}
	if err := requireActiveLegalPrincipal(ctx, tx, "view", true); err != nil {
		return err
	}
	if err := requireLegalPermission(ctx, checker, entityType, entityID, legalViewAction(policy, status)); err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return errs.NotFound(entityType, entityID)
		}
		return err
	}
	return nil
}

// RequireEditableTranslationMutationWithDB authorizes explicit target-locale
// translation requests in every lifecycle. Archived histories use
// edit_archived; every other lifecycle uses edit.
func RequireEditableTranslationMutationWithDB(
	ctx context.Context,
	db *gorm.DB,
	checker CollaborationPermissionChecker,
	entityType string,
	entityID string,
) error {
	if entityType != "privacy" && entityType != "terms" {
		return nil
	}
	policy, err := legalDocumentPolicyForType(entityType)
	if err != nil {
		return err
	}
	var row struct {
		Status string `gorm:"column:status"`
	}
	if err := db.WithContext(ctx).Table(policy.table).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("status").Where("id = ?", entityID).Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.NotFound(entityType, entityID)
		}
		return errs.Internal(err)
	}
	if err := requireActiveLegalPrincipal(ctx, db, "edit", false); err != nil {
		return err
	}
	if err := requireLegalPermission(
		ctx, checker, entityType, entityID,
		legalMutationAction(policy, row.Status, legalActionEdit),
	); err != nil {
		return err
	}
	return nil
}

// RequireTranslationInterchangeViewWithDB authorizes a read-only XLIFF
// projection from the locked Legal history lifecycle. Archived histories use
// view_archived, allowing Authors to open/export them without granting the
// edit_archived permission required by import.
func RequireTranslationInterchangeViewWithDB(
	ctx context.Context,
	db *gorm.DB,
	checker CollaborationPermissionChecker,
	entityType string,
	entityID string,
) error {
	if entityType != "privacy" && entityType != "terms" {
		return nil
	}
	root, err := loadLegalContentDocumentRoot(ctx, db, entityType, entityID, false)
	if err != nil {
		return err
	}
	if err := requireActiveLegalPrincipal(ctx, db, "view", true); err != nil {
		return err
	}
	policy, err := legalDocumentPolicyForType(entityType)
	if err != nil {
		return err
	}
	if err := requireLegalPermission(
		ctx, checker, entityType, entityID, legalViewAction(policy, root.Status),
	); err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return errs.NotFound(entityType, entityID)
		}
		return err
	}
	return nil
}

func RequireLockedSourceLocaleEdit(
	ctx context.Context,
	tx *gorm.DB,
	checker CollaborationPermissionChecker,
	entityType string,
	entityID string,
) error {
	policy, err := legalDocumentPolicyForType(entityType)
	if err != nil {
		return err
	}
	var row struct {
		Status string `gorm:"column:status"`
	}
	if err := tx.WithContext(ctx).Table(policy.table).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("status").Where("id = ?", entityID).Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.NotFound(entityType, entityID)
		}
		return errs.Internal(err)
	}
	if row.Status != policy.draftStatus && row.Status != policy.archivedStatus {
		return errs.FailedPrecondition("scheduled or active legal source documents are read-only")
	}
	if err := requireActiveLegalPrincipal(ctx, tx, "edit", false); err != nil {
		return err
	}
	return requireLegalPermission(
		ctx, checker, entityType, entityID,
		legalMutationAction(policy, row.Status, legalActionEdit),
	)
}
