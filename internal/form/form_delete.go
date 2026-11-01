package form

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// DeleteFormCommand deletes a Form.
type DeleteFormCommand struct {
	ID string
}

// DeleteForm deletes a Form with its share links, OG generations, featured
// image binding and authorization policy.
func (s *Service) DeleteForm(ctx context.Context, command DeleteFormCommand) error {
	var formID string
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			formID, err = lockFormRoot(ctx, store, command.ID, domainerr.ResourceNotFound("form", command.ID))
			return err
		},
		Access: s.freshFormAction(command.ID, formActionDelete),
		Change: func(ctx context.Context, store Store) (string, error) {
			if err := store.DeleteShareLinks(ctx, formID); err != nil {
				return "", err
			}
			if err := store.CancelOG(ctx, formID); err != nil {
				return "", err
			}
			if err := store.ReleaseFeaturedImage(ctx, formID); err != nil {
				return "", err
			}
			return formID, store.DeleteForm(ctx, formID)
		},
		Audit: formDeletedAudit,
		Relationships: func(formID string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return formPolicyRelationships(formID, false)
		},
	})
	return err
}
