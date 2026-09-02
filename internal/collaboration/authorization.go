package collaboration

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"gorm.io/gorm"
)

// ResourceAuthorizer is implemented by the runtime adapter registered for one
// owning domain. Collaboration does not infer domain tables or lifecycles.
type ResourceAuthorizer interface {
	Authorize(
		context.Context,
		string,
		intrav1.CollaborationPermission,
		auth.AccountIdentitySubject,
	) (bool, error)
	// AuthorizeInTx keeps the resource existence/lifecycle read inside an
	// authoritative mutation transaction instead of opening a second one.
	AuthorizeInTx(
		context.Context,
		*gorm.DB,
		string,
		intrav1.CollaborationPermission,
		auth.AccountIdentitySubject,
	) (bool, error)
}

type Registration struct {
	ResourceType intrav1.CollaborationResourceType
	Authorizer   ResourceAuthorizer
}

type Registry struct {
	authorizers map[intrav1.CollaborationResourceType]ResourceAuthorizer
}

func NewRegistry(registrations ...Registration) *Registry {
	authorizers := make(map[intrav1.CollaborationResourceType]ResourceAuthorizer, len(registrations))
	for _, registration := range registrations {
		if registration.ResourceType == intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_UNSPECIFIED {
			panic("collaboration registry requires a concrete resource type")
		}
		if registration.Authorizer == nil {
			panic(fmt.Sprintf("collaboration registry requires authorizer for %s", registration.ResourceType.String()))
		}
		if _, exists := authorizers[registration.ResourceType]; exists {
			panic(fmt.Sprintf("collaboration registry has duplicate resource type %s", registration.ResourceType.String()))
		}
		authorizers[registration.ResourceType] = registration.Authorizer
	}
	return &Registry{authorizers: authorizers}
}

func (r *Registry) Authorize(
	ctx context.Context,
	resourceType intrav1.CollaborationResourceType,
	resourceID string,
	permission intrav1.CollaborationPermission,
	subject auth.AccountIdentitySubject,
) (bool, error) {
	if r == nil {
		return false, errs.DependencyUnavailable("collaboration authorization registry")
	}
	authorizer, ok := r.authorizers[resourceType]
	if !ok {
		return false, errs.InvalidArgument("resource.type", "unsupported collaboration resource type")
	}
	return authorizer.Authorize(ctx, resourceID, permission, subject)
}

func (r *Registry) RequireForSubject(
	ctx context.Context,
	resourceType intrav1.CollaborationResourceType,
	resourceID string,
	permission intrav1.CollaborationPermission,
	subject auth.AccountIdentitySubject,
) error {
	if _, err := auth.NewAccountIdentitySubject(subject.ID); err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := r.Authorize(ctx, resourceType, resourceID, permission, subject)
	if err != nil {
		return err
	}
	if !allowed {
		return errs.NoPermission(permission.String(), "collaboration resource")
	}
	return nil
}

func (r *Registry) RequireForSubjectInTx(
	ctx context.Context,
	tx *gorm.DB,
	resourceType intrav1.CollaborationResourceType,
	resourceID string,
	permission intrav1.CollaborationPermission,
	subject auth.AccountIdentitySubject,
) error {
	if tx == nil {
		return errs.DependencyUnavailable("collaboration authorization transaction")
	}
	if _, err := auth.NewAccountIdentitySubject(subject.ID); err != nil {
		return errs.AuthenticationRequired()
	}
	authorizer, ok := r.authorizers[resourceType]
	if !ok {
		return errs.InvalidArgument("resource.type", "unsupported collaboration resource type")
	}
	allowed, err := authorizer.AuthorizeInTx(ctx, tx, resourceID, permission, subject)
	if err != nil {
		return err
	}
	if !allowed {
		return errs.NoPermission(permission.String(), "collaboration resource")
	}
	return nil
}

func (r *Registry) RequireEditForSubject(
	ctx context.Context,
	resourceType intrav1.CollaborationResourceType,
	resourceID string,
	subject auth.AccountIdentitySubject,
) error {
	return r.RequireForSubject(ctx, resourceType, resourceID, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT, subject)
}

func (r *Registry) RequireCurrentEdit(
	ctx context.Context,
	resourceType intrav1.CollaborationResourceType,
	resourceID string,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return errs.AuthenticationRequired()
	}
	if principal.Banned {
		return errs.AccountBanned()
	}
	if !principal.Onboarded {
		return errs.NoPermission("edit", "collaboration resource")
	}
	subject, err := auth.NewAccountIdentitySubject(principal.IdentityID)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	return r.RequireEditForSubject(ctx, resourceType, resourceID, subject)
}
