package page

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type SetPageFeaturedImageCommand struct{ PageID, FileID string }
type DeletePageFeaturedImageCommand struct{ PageID string }

// pageFeaturedImage is the outcome of a featured image change. fileID is
// the File the change added or removed; empty means nothing changed.
type pageFeaturedImage struct {
	fileID            string
	ogGenerationRunID *string
}

func (s *Service) featuredImageMutation(
	pageID string,
	operation sharedtelemetry.AuditCollectionOperation,
	change func(context.Context, *gorm.DB, *model.Page) (pageFeaturedImage, error),
) mutation.Spec[pageFeaturedImage] {
	return mutation.Spec[pageFeaturedImage]{
		Access: s.lockedPage(pageID, policyv1.Page.Edit),
		Change: func(ctx context.Context, tx *gorm.DB) (pageFeaturedImage, error) {
			var page model.Page
			if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
				First(&page, "id = ?", pageID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return pageFeaturedImage{}, domainerr.ResourceNotFound("page", pageID)
				}
				return pageFeaturedImage{}, err
			}
			return change(ctx, tx, &page)
		},
		Audit: func(image pageFeaturedImage) mutation.Audit {
			if image.fileID == "" {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPageFeaturedImageAuditRecord(metadata, pageID, image.fileID, operation)
			})
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, image pageFeaturedImage) error {
			if image.fileID == "" {
				return nil
			}
			return s.publish(ctx, tx, buildManageMediaMutationContentUpdatedEvent(
				managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE, pageID, "media.featured_image",
			))
		},
	}
}

// SetPageFeaturedImage sets an attachable File as the Page featured image.
func (s *Service) SetPageFeaturedImage(ctx context.Context, command SetPageFeaturedImageCommand) (_ *managev1.SetPageFeaturedImageResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.PageID))

	image, err := mutation.Run(ctx, s.mutations, s.featuredImageMutation(command.PageID, sharedtelemetry.AuditCollectionOperationAdded,
		func(ctx context.Context, tx *gorm.DB, page *model.Page) (pageFeaturedImage, error) {
			if page.FeaturedImageFileID != nil && *page.FeaturedImageFileID == command.FileID {
				return pageFeaturedImage{}, nil
			}
			if err := s.runtime.LockAttachableFilesForUpdate(ctx, tx, []string{command.FileID}); err != nil {
				return pageFeaturedImage{}, err
			}
			var file model.File
			if err := tx.First(&file, "id = ?", command.FileID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return pageFeaturedImage{}, domainerr.ResourceNotFound("file", command.FileID)
				}
				return pageFeaturedImage{}, err
			}
			if err := tx.Model(page).Updates(structured.Fields{
				"featured_image_file_id": command.FileID,
				"updated_at":             time.Now(),
			}).Error; err != nil {
				return pageFeaturedImage{}, err
			}
			return s.requestFeaturedImageOG(ctx, tx, command.PageID, command.FileID, "page_featured_image_updated")
		},
	))
	if err != nil {
		return nil, err
	}
	featuredImageFileID := command.FileID
	featuredDeliveries, err := s.loadPageFeaturedImageDeliveries(ctx, []model.Page{{
		ID:                  command.PageID,
		FeaturedImageFileID: &featuredImageFileID,
	}})
	if err != nil {
		return nil, err
	}
	return &managev1.SetPageFeaturedImageResponse{
		ImageDelivery: featuredDeliveries[command.PageID], OgGenerationRunId: image.ogGenerationRunID,
	}, nil
}

// DeletePageFeaturedImage removes the Page featured image.
func (s *Service) DeletePageFeaturedImage(ctx context.Context, command DeletePageFeaturedImageCommand) (_ *managev1.OgAssetDeleteResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.PageID))

	image, err := mutation.Run(ctx, s.mutations, s.featuredImageMutation(command.PageID, sharedtelemetry.AuditCollectionOperationRemoved,
		func(ctx context.Context, tx *gorm.DB, page *model.Page) (pageFeaturedImage, error) {
			if page.FeaturedImageFileID == nil {
				return pageFeaturedImage{}, nil
			}
			oldFileID := *page.FeaturedImageFileID
			if err := tx.Model(page).Updates(structured.Fields{
				"featured_image_file_id": nil,
				"updated_at":             time.Now(),
			}).Error; err != nil {
				return pageFeaturedImage{}, err
			}
			if err := s.runtime.ReleasePublicAssetBindings(ctx, tx, "page", command.PageID, "featured_image"); err != nil {
				return pageFeaturedImage{}, err
			}
			return s.requestFeaturedImageOG(ctx, tx, command.PageID, oldFileID, "page_featured_image_removed")
		},
	))
	if err != nil {
		return nil, err
	}
	return &managev1.OgAssetDeleteResponse{Success: true, OgGenerationRunId: image.ogGenerationRunID}, nil
}

// requestFeaturedImageOG regenerates every locale's OG image after the
// featured image of the Page changed to or from fileID.
func (s *Service) requestFeaturedImageOG(ctx context.Context, tx *gorm.DB, pageID, fileID, reason string) (pageFeaturedImage, error) {
	runID, err := s.runtime.RequestCurrentWithDB(ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_PAGE, pageID, "", true, reason)
	if err != nil {
		return pageFeaturedImage{}, err
	}
	image := pageFeaturedImage{fileID: fileID}
	if runID != "" {
		image.ogGenerationRunID = &runID
	}
	return image, nil
}
