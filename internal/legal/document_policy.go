package legal

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
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
	checker authz.AuthorizationDecisionChecker,
	kind *policyKind,
	entityID string,
	action legalAction,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned {
		return domainerr.AuthenticationRequired()
	}
	return requireLegalPermissionForPrincipal(ctx, checker, kind.kind, entityID, action, principal)
}

func requireActiveLegalPrincipal(
	ctx context.Context,
	lockActive LockActive,
	actionName string,
	maskNotFound bool,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return domainerr.AuthenticationRequired()
	}
	active, err := lockActive(principal)
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
	checker authz.AuthorizationDecisionChecker,
	lockActive LockActive,
	kind *policyKind,
	entityID string,
	actionName string,
	maskNotFound bool,
	action legalAction,
) error {
	if err := requireActiveLegalPrincipal(ctx, lockActive, actionName, maskNotFound); err != nil {
		return err
	}
	return requireLegalPermission(ctx, checker, kind, entityID, action)
}

func legalViewAction(kind *policyKind, status Status) legalAction {
	if status == kind.archived {
		return legalActionViewArchived
	}
	return legalActionView
}

func legalMutationAction(kind *policyKind, status Status, normal legalAction) legalAction {
	if status == kind.archived {
		return legalActionEditArchived
	}
	return normal
}

// requireLegalVersionViewOrNotFound selects the exact object read permission
// from the already loaded lifecycle. Denials intentionally appear absent.
func requireLegalVersionViewOrNotFound(
	ctx context.Context,
	checker authz.AuthorizationDecisionChecker,
	lockActive LockActive,
	kind *policyKind,
	entityID string,
	status Status,
) error {
	if err := authorizeLegal(ctx, checker, lockActive, kind, entityID, "view", true, legalViewAction(kind, status)); err != nil {
		if mutation.IsDenied(err) {
			return domainerr.ResourceNotFound(kind.entity(), entityID).WithCause(err)
		}
		return err
	}
	return nil
}

// lockLegalStatus locks the version and returns its stored status.
func lockLegalStatus(ctx context.Context, store Store, kind *policyKind, entityID string) (Status, error) {
	status, err := store.LockStatus(ctx, kind.kind, entityID)
	if errors.Is(err, ErrPolicyNotFound) {
		return "", domainerr.ResourceNotFound(kind.entity(), entityID)
	}
	return status, err
}

// RequireEditableTranslationMutation authorizes explicit target-locale
// translation requests in every lifecycle, in the Translation application's
// transaction. Archived histories use edit_archived; every other lifecycle
// uses edit.
func RequireEditableTranslationMutation(
	ctx context.Context,
	store Store,
	checker authz.AuthorizationDecisionChecker,
	lockActive LockActive,
	entityType string,
	entityID string,
) error {
	if entityType != "privacy" && entityType != "terms" {
		return nil
	}
	return RequireLockedSourceLocaleEdit(ctx, store, checker, lockActive, entityType, entityID)
}

// RequireTranslationInterchangeView authorizes a read-only XLIFF projection
// from the locked Legal history lifecycle, in the Translation application's
// transaction. Archived histories use view_archived, allowing Authors to
// open/export them without granting the edit_archived permission required
// by import.
func RequireTranslationInterchangeView(
	ctx context.Context,
	store Store,
	checker authz.AuthorizationDecisionChecker,
	lockActive LockActive,
	entityType string,
	entityID string,
) error {
	if entityType != "privacy" && entityType != "terms" {
		return nil
	}
	kind, err := legalKind(entityType)
	if err != nil {
		return err
	}
	root, err := loadLegalContentDocumentRoot(ctx, store, kind, entityID, false)
	if err != nil {
		return err
	}
	return requireLegalVersionViewOrNotFound(ctx, checker, lockActive, kind, entityID, root.Status)
}

// RequireLockedSourceLocaleEdit locks the version and authorizes an edit of
// it in the caller's transaction: edit_archived for an archived version,
// edit otherwise.
func RequireLockedSourceLocaleEdit(
	ctx context.Context,
	store Store,
	checker authz.AuthorizationDecisionChecker,
	lockActive LockActive,
	entityType string,
	entityID string,
) error {
	kind, err := legalKind(entityType)
	if err != nil {
		return err
	}
	status, err := lockLegalStatus(ctx, store, kind, entityID)
	if err != nil {
		return err
	}
	return authorizeLegal(ctx, checker, lockActive, kind, entityID, "edit", false, legalMutationAction(kind, status, legalActionEdit))
}
