package form

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// SetFormFeaturedImageCommand sets an attachable File as a Form's featured
// image.
type SetFormFeaturedImageCommand struct {
	FormID string
	FileID string
}

// DeleteFormFeaturedImageCommand removes a Form's featured image.
type DeleteFormFeaturedImageCommand struct {
	FormID string
}

// formFeaturedImage is what a featured image change wrote: the File it added
// or removed, empty when the image did not change.
type formFeaturedImage struct {
	changedFileID string
	imageAsset    *commonv1.AssetRef
	ogRunID       *string
}

// featuredImageMutation locks the Form's featured image column, checks Edit
// and runs change; the audit names the File change records.
func (s *Service) featuredImageMutation(
	formID string,
	operation sharedtelemetry.AuditCollectionOperation,
	current *model.Form,
	change func(context.Context, *gorm.DB) (formFeaturedImage, error),
) mutation.Spec[formFeaturedImage] {
	return mutation.Spec[formFeaturedImage]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return lockFormRoot(ctx, tx, formID, current, domainerr.ResourceNotFound("form", formID), "id", "featured_image_file_id")
		},
		Access: s.freshFormAction(formID, formActionEdit),
		Change: change,
		Audit: func(image formFeaturedImage) mutation.Audit {
			if image.changedFileID == "" {
				return mutation.Unchanged()
			}
			return formFeaturedImageAudit(formID, image.changedFileID, operation)
		},
	}
}

// SetFormFeaturedImage binds the File as the featured image and regenerates
// every locale's OG image.
func (s *Service) SetFormFeaturedImage(ctx context.Context, command SetFormFeaturedImageCommand) (*managev1.SetFormFeaturedImageResponse, error) {
	var current model.Form
	image, err := mutation.Run(ctx, s.mutations, s.featuredImageMutation(command.FormID, sharedtelemetry.AuditCollectionOperationAdded, &current,
		func(ctx context.Context, tx *gorm.DB) (formFeaturedImage, error) {
			if err := s.assets.LockAttachableFiles(ctx, tx, []string{command.FileID}); err != nil {
				return formFeaturedImage{}, err
			}
			var file model.File
			if err := tx.First(&file, "id = ?", command.FileID).Error; err != nil {
				return formFeaturedImage{}, err
			}
			if err := tx.Model(&model.Form{}).Where("id = ?", command.FormID).Updates(structured.Fields{
				"featured_image_file_id": command.FileID,
				"updated_at":             time.Now(),
			}).Error; err != nil {
				return formFeaturedImage{}, err
			}
			var image formFeaturedImage
			if current.FeaturedImageFileID == nil || *current.FeaturedImageFileID != command.FileID {
				image.changedFileID = command.FileID
			}
			var err error
			if image.imageAsset, err = s.assets.BindFeaturedImage(ctx, tx, file.ID, command.FormID); err != nil {
				return formFeaturedImage{}, err
			}
			title, err := s.og.BaseTitle(ctx, tx, command.FormID)
			if err != nil {
				return formFeaturedImage{}, err
			}
			runID, err := s.og.Request(ctx, tx, command.FormID, title, "", true, "form_featured_image_updated")
			if err != nil {
				return formFeaturedImage{}, err
			}
			image.ogRunID = &runID
			return image, nil
		},
	))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("file", command.FileID).WithCause(err)
		}
		return nil, err
	}
	return &managev1.SetFormFeaturedImageResponse{ImageAsset: image.imageAsset, OgGenerationRunId: image.ogRunID}, nil
}

// DeleteFormFeaturedImage removes the featured image, when the Form has one,
// and regenerates every locale's OG image.
func (s *Service) DeleteFormFeaturedImage(ctx context.Context, command DeleteFormFeaturedImageCommand) (*managev1.OgAssetDeleteResponse, error) {
	var current model.Form
	image, err := mutation.Run(ctx, s.mutations, s.featuredImageMutation(command.FormID, sharedtelemetry.AuditCollectionOperationRemoved, &current,
		func(ctx context.Context, tx *gorm.DB) (formFeaturedImage, error) {
			if current.FeaturedImageFileID == nil {
				return formFeaturedImage{}, nil
			}
			if err := tx.Model(&model.Form{}).Where("id = ?", command.FormID).Updates(structured.Fields{
				"featured_image_file_id": nil,
				"updated_at":             time.Now(),
			}).Error; err != nil {
				return formFeaturedImage{}, err
			}
			if err := s.assets.ReleaseFeaturedImage(ctx, tx, command.FormID); err != nil {
				return formFeaturedImage{}, err
			}
			runID, err := s.og.RequestAfterMutation(ctx, tx, command.FormID, "", true, "form_featured_image_removed")
			if err != nil {
				return formFeaturedImage{}, err
			}
			return formFeaturedImage{changedFileID: *current.FeaturedImageFileID, ogRunID: runID}, nil
		},
	))
	if err != nil {
		return nil, err
	}
	return &managev1.OgAssetDeleteResponse{Success: true, OgGenerationRunId: image.ogRunID}, nil
}
