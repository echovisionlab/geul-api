package page

import (
	"context"
	"errors"
	"log/slog"
	"time"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"

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

func featuredImageMutation(
	pageID string,
	operation sharedtelemetry.AuditCollectionOperation,
	change func(context.Context, Store, *Page) (pageFeaturedImage, error),
) mutation.StoreSpec[pageFeaturedImage, Store] {
	return mutation.StoreSpec[pageFeaturedImage, Store]{
		Access: lockedPage(pageID, policyv1.Page.Edit),
		Change: func(ctx context.Context, store Store) (pageFeaturedImage, error) {
			page, err := store.FindPage(ctx, ByID, pageID, UpdateLock)
			if err != nil {
				if errors.Is(err, ErrPageNotFound) {
					return pageFeaturedImage{}, domainerr.ResourceNotFound("page", pageID)
				}
				return pageFeaturedImage{}, err
			}
			return change(ctx, store, &page)
		},
		Audit: func(image pageFeaturedImage) mutation.Audit {
			if image.fileID == "" {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPageFeaturedImageAuditRecord(metadata, pageID, image.fileID, operation)
			})
		},
		Outbox: func(ctx context.Context, store Store, image pageFeaturedImage) error {
			if image.fileID == "" {
				return nil
			}
			return store.PublishContentUpdate(ctx, ContentUpdate{PageID: pageID, Kind: FeaturedImageUpdate})
		},
	}
}

// FeaturedImageResult is the delivery of a Page's new featured image and the
// OG generation it started.
type FeaturedImageResult struct {
	Delivery          *fileref.Delivery
	OgGenerationRunID *string
}

// SetPageFeaturedImage sets an attachable File as the Page featured image.
func (s *Service) SetPageFeaturedImage(ctx context.Context, command SetPageFeaturedImageCommand) (_ FeaturedImageResult, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.PageID))

	image, err := mutation.Run(ctx, s.mutations, featuredImageMutation(command.PageID, sharedtelemetry.AuditCollectionOperationAdded,
		func(ctx context.Context, store Store, page *Page) (pageFeaturedImage, error) {
			if page.FeaturedImageFileID != nil && *page.FeaturedImageFileID == command.FileID {
				return pageFeaturedImage{}, nil
			}
			if err := store.LockAttachableFiles(ctx, []string{command.FileID}); err != nil {
				return pageFeaturedImage{}, err
			}
			if err := store.FileExists(ctx, command.FileID); err != nil {
				if errors.Is(err, ErrFileNotFound) {
					return pageFeaturedImage{}, domainerr.ResourceNotFound("file", command.FileID)
				}
				return pageFeaturedImage{}, err
			}
			fileID := command.FileID
			if err := store.SetFeaturedImage(ctx, command.PageID, &fileID, time.Now()); err != nil {
				return pageFeaturedImage{}, err
			}
			return requestFeaturedImageOG(ctx, store, command.PageID, command.FileID, "page_featured_image_updated")
		},
	))
	if err != nil {
		return FeaturedImageResult{}, err
	}
	featuredImageFileID := command.FileID
	delivery, err := s.featuredImageDelivery(ctx, command.PageID, &featuredImageFileID)
	if err != nil {
		return FeaturedImageResult{}, err
	}
	return FeaturedImageResult{Delivery: delivery, OgGenerationRunID: image.ogGenerationRunID}, nil
}

// DeletePageFeaturedImage removes the Page featured image and returns the
// OG generation it started.
func (s *Service) DeletePageFeaturedImage(ctx context.Context, command DeletePageFeaturedImageCommand) (_ *string, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.PageID))

	image, err := mutation.Run(ctx, s.mutations, featuredImageMutation(command.PageID, sharedtelemetry.AuditCollectionOperationRemoved,
		func(ctx context.Context, store Store, page *Page) (pageFeaturedImage, error) {
			if page.FeaturedImageFileID == nil {
				return pageFeaturedImage{}, nil
			}
			oldFileID := *page.FeaturedImageFileID
			if err := store.SetFeaturedImage(ctx, command.PageID, nil, time.Now()); err != nil {
				return pageFeaturedImage{}, err
			}
			if err := store.ReleaseFeaturedImageBindings(ctx, command.PageID); err != nil {
				return pageFeaturedImage{}, err
			}
			return requestFeaturedImageOG(ctx, store, command.PageID, oldFileID, "page_featured_image_removed")
		},
	))
	if err != nil {
		return nil, err
	}
	return image.ogGenerationRunID, nil
}

// requestFeaturedImageOG regenerates every locale's OG image after the
// featured image of the Page changed to or from fileID.
func requestFeaturedImageOG(ctx context.Context, store Store, pageID, fileID, reason string) (pageFeaturedImage, error) {
	runID, err := store.RequestOG(ctx, pageID, "", true, reason)
	if err != nil {
		return pageFeaturedImage{}, err
	}
	image := pageFeaturedImage{fileID: fileID}
	if runID != "" {
		image.ogGenerationRunID = &runID
	}
	return image, nil
}
