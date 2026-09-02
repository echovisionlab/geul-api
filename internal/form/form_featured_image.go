package form

import (
	"connectrpc.com/connect"
	"context"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func (s *FormService) SetFormFeaturedImage(
	ctx context.Context,
	req *connect.Request[managev1.SetFormFeaturedImageRequest],
) (*connect.Response[managev1.SetFormFeaturedImageResponse], error) {
	var (
		current    model.Form
		file       model.File
		imageAsset *commonv1.AssetRef
		ogRunID    *string
	)

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "featured_image_file_id").
			First(&current, "id = ?", req.Msg.FormId).Error; err != nil {
			return err
		}
		if err := s.requireFreshFormAction(ctx, tx, req.Msg.FormId, formActionEdit); err != nil {
			return err
		}
		if err := s.assets.LockAttachableFiles(ctx, tx, []string{req.Msg.FileId}); err != nil {
			return err
		}

		if err := tx.First(&file, "id = ?", req.Msg.FileId).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Form{}).
			Where("id = ?", req.Msg.FormId).
			Updates(structured.Fields{
				"featured_image_file_id": req.Msg.FileId,
				"updated_at":             time.Now(),
			}).Error; err != nil {
			return err
		}
		if current.FeaturedImageFileID == nil || *current.FeaturedImageFileID != req.Msg.FileId {
			if err := s.appendFormFeaturedImageAudit(
				ctx,
				tx,
				req.Msg.FormId,
				req.Msg.FileId,
				sharedtelemetry.AuditCollectionOperationAdded,
			); err != nil {
				return err
			}
		}

		imageRef, err := s.assets.BindFeaturedImage(ctx, tx, file.ID, req.Msg.FormId)
		if err != nil {
			return err
		}
		imageAsset = imageRef
		title, err := s.og.BaseTitle(ctx, tx, req.Msg.FormId)
		if err != nil {
			return err
		}
		runID, err := s.og.Request(ctx, tx, req.Msg.FormId, title, "", true, "form_featured_image_updated")
		if err != nil {
			return err
		}
		ogRunID = &runID
		return nil
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			var count int64
			lookupErr := s.db.WithContext(ctx).
				Model(&model.Form{}).
				Where("id = ?", req.Msg.FormId).
				Count(&count).Error
			if lookupErr == nil && count == 0 {
				return nil, errs.NotFound("form", req.Msg.FormId)
			}
			return nil, errs.NotFound("file", req.Msg.FileId)
		}
		return nil, errs.Wrap(err)
	}

	return connect.NewResponse(&managev1.SetFormFeaturedImageResponse{
		ImageAsset: imageAsset, OgGenerationRunId: ogRunID,
	}), nil
}

// DeleteFormFeaturedImage removes the featured image from a form.
func (s *FormService) DeleteFormFeaturedImage(
	ctx context.Context,
	req *connect.Request[managev1.DeleteFormFeaturedImageRequest],
) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	var current model.Form
	var ogRunID *string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "featured_image_file_id").
			First(&current, "id = ?", req.Msg.FormId).Error; err != nil {
			return err
		}
		if err := s.requireFreshFormAction(ctx, tx, req.Msg.FormId, formActionEdit); err != nil {
			return err
		}

		if current.FeaturedImageFileID == nil {
			return nil
		}
		previousFileID := *current.FeaturedImageFileID

		if err := tx.Model(&model.Form{}).
			Where("id = ?", req.Msg.FormId).
			Updates(structured.Fields{
				"featured_image_file_id": nil,
				"updated_at":             time.Now(),
			}).Error; err != nil {
			return err
		}
		if err := s.assets.ReleaseFeaturedImage(ctx, tx, req.Msg.FormId); err != nil {
			return err
		}
		if err := s.appendFormFeaturedImageAudit(
			ctx,
			tx,
			req.Msg.FormId,
			previousFileID,
			sharedtelemetry.AuditCollectionOperationRemoved,
		); err != nil {
			return err
		}
		var err error
		ogRunID, err = s.og.RequestAfterMutation(ctx, tx, req.Msg.FormId, "", true, "form_featured_image_removed")
		return err
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("form", req.Msg.FormId)
		}
		return nil, errs.Wrap(err)
	}

	if current.FeaturedImageFileID == nil {
		return connect.NewResponse(&managev1.OgAssetDeleteResponse{Success: true}), nil
	}

	return connect.NewResponse(&managev1.OgAssetDeleteResponse{Success: true, OgGenerationRunId: ogRunID}), nil
}

// SubmitForm submits a form
// ListFormSubmissions lists submissions for a form (admin only)
