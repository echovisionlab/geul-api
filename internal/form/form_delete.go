package form

import (
	"connectrpc.com/connect"
	"context"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *FormService) DeleteForm(
	ctx context.Context,
	req *connect.Request[managev1.DeleteFormRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	var form model.Form
	if err := s.db.WithContext(ctx).First(&form, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("form", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}

	_, err := authzmutation.Execute(
		ctx,
		s.db,
		s.spiceDB,
		func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Select("id").
				First(&form, "id = ?", req.Msg.Id).Error; err != nil {
				return err
			}
			if err := s.requireFreshFormAction(ctx, tx, form.ID, formActionDelete); err != nil {
				return err
			}
			if err := tx.
				Where("entity_id = ? AND entity_type IN ?", form.ID, []string{
					managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM.String(),
					managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD.String(),
				}).
				Delete(&model.ShareLink{}).Error; err != nil {
				return errs.Internal(err)
			}
			if err := s.og.CancelAndRelease(ctx, tx, form.ID); err != nil {
				return err
			}
			if err := s.assets.ReleaseFeaturedImage(ctx, tx, form.ID); err != nil {
				return err
			}
			if err := tx.Delete(&form).Error; err != nil {
				return err
			}
			if err := s.appendFormDeletedAudit(ctx, tx, form.ID); err != nil {
				return err
			}
			policyDelete, err := policyv1.Form.DeletePolicy(form.ID)
			if err != nil {
				return err
			}
			policyTouch, err := policyv1.Form.TouchPolicy(form.ID)
			if err != nil {
				return err
			}
			return write([]policyv1.RelationshipMutation{policyDelete}, []policyv1.RelationshipMutation{policyTouch})
		},
	)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

// SetFormFeaturedImage sets the featured image for a form.
