package form

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
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

// FeaturedImageChange is what a featured image change produced: the bound
// image and the OG generation it started.
type FeaturedImageChange struct {
	ImageAsset        *Asset
	OgGenerationRunID *string
}

// formFeaturedImage is what a featured image change wrote: the File it added
// or removed, empty when the image did not change.
type formFeaturedImage struct {
	changedFileID string
	change        FeaturedImageChange
}

// featuredImageMutation locks the Form's featured image column, checks Edit
// and runs change; the audit names the File change records.
func (s *Service) featuredImageMutation(
	formID string,
	operation sharedtelemetry.AuditCollectionOperation,
	current **string,
	change func(context.Context, Store) (formFeaturedImage, error),
) mutation.StoreSpec[formFeaturedImage, Store] {
	return mutation.StoreSpec[formFeaturedImage, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			*current, err = store.LockFeaturedImage(ctx, formID)
			return formNotFound(err, domainerr.ResourceNotFound("form", formID))
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
func (s *Service) SetFormFeaturedImage(ctx context.Context, command SetFormFeaturedImageCommand) (FeaturedImageChange, error) {
	var current *string
	image, err := mutation.Run(ctx, s.mutations, s.featuredImageMutation(command.FormID, sharedtelemetry.AuditCollectionOperationAdded, &current,
		func(ctx context.Context, store Store) (formFeaturedImage, error) {
			if err := store.LockAttachableFiles(ctx, []string{command.FileID}); err != nil {
				return formFeaturedImage{}, err
			}
			fileID, err := store.FileID(ctx, command.FileID)
			if err != nil {
				return formFeaturedImage{}, err
			}
			if err := store.SetFeaturedImage(ctx, command.FormID, &command.FileID, time.Now()); err != nil {
				return formFeaturedImage{}, err
			}
			var image formFeaturedImage
			if current == nil || *current != command.FileID {
				image.changedFileID = command.FileID
			}
			if image.change.ImageAsset, err = store.BindFeaturedImage(ctx, command.FormID, fileID); err != nil {
				return formFeaturedImage{}, err
			}
			runID, err := store.RequestOG(ctx, command.FormID, "", true, "form_featured_image_updated")
			if err != nil {
				return formFeaturedImage{}, err
			}
			image.change.OgGenerationRunID = &runID
			return image, nil
		},
	))
	if err != nil {
		if errors.Is(err, ErrFileNotFound) {
			return FeaturedImageChange{}, domainerr.ResourceNotFound("file", command.FileID).WithCause(err)
		}
		return FeaturedImageChange{}, err
	}
	return image.change, nil
}

// DeleteFormFeaturedImage removes the featured image, when the Form has one,
// and regenerates every locale's OG image. It returns the OG generation it
// started.
func (s *Service) DeleteFormFeaturedImage(ctx context.Context, command DeleteFormFeaturedImageCommand) (*string, error) {
	var current *string
	image, err := mutation.Run(ctx, s.mutations, s.featuredImageMutation(command.FormID, sharedtelemetry.AuditCollectionOperationRemoved, &current,
		func(ctx context.Context, store Store) (formFeaturedImage, error) {
			if current == nil {
				return formFeaturedImage{}, nil
			}
			if err := store.SetFeaturedImage(ctx, command.FormID, nil, time.Now()); err != nil {
				return formFeaturedImage{}, err
			}
			if err := store.ReleaseFeaturedImage(ctx, command.FormID); err != nil {
				return formFeaturedImage{}, err
			}
			runID, err := store.RequestOG(ctx, command.FormID, "", true, "form_featured_image_removed")
			if err != nil {
				return formFeaturedImage{}, err
			}
			return formFeaturedImage{changedFileID: *current, change: FeaturedImageChange{OgGenerationRunID: afterMutationRun(runID)}}, nil
		},
	))
	if err != nil {
		return nil, err
	}
	return image.change.OgGenerationRunID, nil
}

// afterMutationRun is the run ID a mutation response names: none when no
// generation started.
func afterMutationRun(runID string) *string {
	if strings.TrimSpace(runID) == "" {
		return nil
	}
	return &runID
}
