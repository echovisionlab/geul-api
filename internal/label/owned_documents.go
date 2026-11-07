package label

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// OwnedDocumentIDs is the Labels the caller may view, which are those they own or
// manage, directly or through a parent Label.
func (s *Service) OwnedDocumentIDs(ctx context.Context) ([]string, error) {
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, domainerr.AuthenticationRequired()
	}
	subject, err := auth.NewAccountIdentitySubject(user.IdentityID)
	if err != nil {
		return nil, domainerr.AuthenticationRequired().WithCause(err)
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return nil, domainerr.AuthenticationRequired().WithCause(err)
	}
	labelIDs, err := s.spiceDB.LookupResources(ctx, policyv1.Label.LookupView(), actor)
	if err != nil {
		return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	return labelIDs, nil
}
