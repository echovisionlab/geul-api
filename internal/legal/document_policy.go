package legal

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// legalKind returns the descriptor of entityType, which names a policy kind.
func legalKind(entityType string) (*policyKind, error) {
	kind, ok := policyKinds[PolicyKind(entityType)]
	if !ok {
		return nil, errors.New("unsupported legal document type")
	}
	return kind, nil
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
		return domainerr.AuthenticationRequired()
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
		return domainerr.AuthenticationRequired()
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return err
	}
	if active {
		return nil
	}
	if maskNotFound {
		return domainerr.New(domainerr.NotFound, "", "legal policy not found")
	}
	return domainerr.NoPermission(actionName, "legal policy")
}

// authorizeLegal fences the request's principal and checks action on the
// version. An inactive principal is denied actionName, or finds no policy
// when maskNotFound.
func authorizeLegal(
	ctx context.Context,
	tx *gorm.DB,
	checker CollaborationPermissionChecker,
	entityType string,
	entityID string,
	actionName string,
	maskNotFound bool,
	action legalAction,
) error {
	if err := requireActiveLegalPrincipal(ctx, tx, actionName, maskNotFound); err != nil {
		return err
	}
	return requireLegalPermission(ctx, checker, entityType, entityID, action)
}

func legalViewAction(kind *policyKind, status string) legalAction {
	if status == string(kind.archived) {
		return legalActionViewArchived
	}
	return legalActionView
}

func legalMutationAction(kind *policyKind, status string, normal legalAction) legalAction {
	if status == string(kind.archived) {
		return legalActionEditArchived
	}
	return normal
}

// requireLegalVersionViewOrNotFound selects the exact object read permission
// from the already loaded lifecycle. Denials intentionally appear absent.
func requireLegalVersionViewOrNotFound(
	ctx context.Context,
	tx *gorm.DB,
	checker CollaborationPermissionChecker,
	entityType string,
	entityID string,
	status string,
) error {
	kind, err := legalKind(entityType)
	if err != nil {
		return err
	}
	if err := authorizeLegal(ctx, tx, checker, entityType, entityID, "view", true, legalViewAction(kind, status)); err != nil {
		if mutation.IsDenied(err) {
			return domainerr.ResourceNotFound(entityType, entityID).WithCause(err)
		}
		return err
	}
	return nil
}

// lockLegalStatus locks the version and returns its stored status.
func lockLegalStatus(ctx context.Context, tx *gorm.DB, kind *policyKind, entityID string) (string, error) {
	var row struct {
		Status string `gorm:"column:status"`
	}
	if err := tx.WithContext(ctx).Table(kind.table).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("status").Where("id = ?", entityID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", domainerr.ResourceNotFound(kind.entity(), entityID)
		}
		return "", err
	}
	return row.Status, nil
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
	return RequireLockedSourceLocaleEdit(ctx, db, checker, entityType, entityID)
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
	return requireLegalVersionViewOrNotFound(ctx, db, checker, entityType, entityID, root.Status)
}

// RequireLockedSourceLocaleEdit locks the version and authorizes an edit of
// it in the caller's transaction: edit_archived for an archived version,
// edit otherwise.
func RequireLockedSourceLocaleEdit(
	ctx context.Context,
	tx *gorm.DB,
	checker CollaborationPermissionChecker,
	entityType string,
	entityID string,
) error {
	kind, err := legalKind(entityType)
	if err != nil {
		return err
	}
	status, err := lockLegalStatus(ctx, tx, kind, entityID)
	if err != nil {
		return err
	}
	return authorizeLegal(ctx, tx, checker, entityType, entityID, "edit", false, legalMutationAction(kind, status, legalActionEdit))
}
