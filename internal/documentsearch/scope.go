package documentsearch

import (
	"context"
	"errors"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

// Scope is the documents a caller may find, resolved once for a whole search. A
// document that leaves it during the search stays found until the next one.
type Scope struct {
	// All means every document, for a caller who may read them all.
	All bool
	// Documents are the only documents a caller without All may find.
	Documents map[core.DocumentReference]struct{}
}

// Allows reports whether the scope covers document.
func (scope Scope) Allows(document core.DocumentReference) bool {
	if scope.All {
		return true
	}
	_, found := scope.Documents[document]
	return found
}

func (scope Scope) empty() bool { return !scope.All && len(scope.Documents) == 0 }

// Scopes resolves the current caller's Scope among documentTypes.
type Scopes interface {
	Resolve(ctx context.Context, documentTypes []core.Domain) (Scope, error)
}

// CallerRoles answers what a search's scope depends on, about the current caller and
// as of now.
type CallerRoles interface {
	IsActive(ctx context.Context) (bool, error)
	IsAdmin(ctx context.Context) (bool, error)
	IsAuthor(ctx context.Context) (bool, error)
}

// OwnedDocuments lists the documents of one type the current caller owns.
type OwnedDocuments interface {
	OwnedDocumentIDs(ctx context.Context) ([]string, error)
}

// OwnershipScopes resolves a Scope from the caller's role and what they own: a Site
// Admin finds every document, and anyone else what they write or manage, in any
// lifecycle. Only an Author finds a Post: a collaborator without the Author role may
// view a live Post, but a Scope cannot tell when that Post is archived.
type OwnershipScopes struct {
	roles CallerRoles
	owned map[core.Domain]OwnedDocuments
}

// NewOwnershipScopes takes the owned documents of each type a caller can own. A
// caller finds nothing of any other type.
func NewOwnershipScopes(roles CallerRoles, owned map[core.Domain]OwnedDocuments) (*OwnershipScopes, error) {
	if roles == nil {
		return nil, errors.New("document search scopes need the caller's roles")
	}
	return &OwnershipScopes{roles: roles, owned: owned}, nil
}

// Resolve implements Scopes.
func (scopes *OwnershipScopes) Resolve(ctx context.Context, documentTypes []core.Domain) (Scope, error) {
	active, err := scopes.roles.IsActive(ctx)
	if err != nil || !active {
		return Scope{}, err
	}
	admin, err := scopes.roles.IsAdmin(ctx)
	if err != nil {
		return Scope{}, err
	}
	if admin {
		return Scope{All: true}, nil
	}
	return scopes.ownedAmong(ctx, documentTypes)
}

func (scopes *OwnershipScopes) ownedAmong(ctx context.Context, documentTypes []core.Domain) (Scope, error) {
	scope := Scope{Documents: map[core.DocumentReference]struct{}{}}
	for _, documentType := range documentTypes {
		owned, found := scopes.owned[documentType]
		if !found {
			continue
		}
		if documentType == core.DomainPost {
			author, err := scopes.roles.IsAuthor(ctx)
			if err != nil {
				return Scope{}, err
			}
			if !author {
				continue
			}
		}
		ids, err := owned.OwnedDocumentIDs(ctx)
		if err != nil {
			return Scope{}, err
		}
		for _, id := range ids {
			scope.Documents[core.DocumentReference(id)] = struct{}{}
		}
	}
	return scope, nil
}
