package form

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
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
	var form model.Form
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return lockFormRoot(ctx, tx, command.ID, &form, domainerr.ResourceNotFound("form", command.ID), "id")
		},
		Access: s.freshFormAction(command.ID, formActionDelete),
		Change: func(ctx context.Context, tx *gorm.DB) (string, error) {
			if err := s.shareLinks.DeleteFormShareLinks(ctx, tx, form.ID); err != nil {
				return "", err
			}
			if err := s.og.CancelAndRelease(ctx, tx, form.ID); err != nil {
				return "", err
			}
			if err := s.assets.ReleaseFeaturedImage(ctx, tx, form.ID); err != nil {
				return "", err
			}
			return form.ID, tx.WithContext(ctx).Delete(&form).Error
		},
		Audit: formDeletedAudit,
		Relationships: func(formID string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return formPolicyRelationships(formID, false)
		},
	})
	return err
}
