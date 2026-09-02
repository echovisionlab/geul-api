package page

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// pageSortConfig defines allowed sort fields for pages
func (s *PageService) SetPageFeaturedImage(
	ctx context.Context,
	req *connect.Request[managev1.SetPageFeaturedImageRequest],
) (*connect.Response[managev1.SetPageFeaturedImageResponse], error) {
	var ogRunID *string
	changed := false

	// Use transaction with row-level lock to prevent race conditions
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Lock and get page with current featured_image_file_id
		var page model.Page
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&page, "id = ?", req.Msg.PageId).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("page", req.Msg.PageId)
			}
			return errs.Internal(err)
		}
		if err := requireLockedPagePermission(ctx, tx, s.spiceDB, page.ID, policyv1.Page.Edit); err != nil {
			return err
		}
		if page.FeaturedImageFileID != nil && *page.FeaturedImageFileID == req.Msg.FileId {
			return nil
		}
		if err := s.runtime.LockAttachableFilesForUpdate(ctx, tx, []string{req.Msg.FileId}); err != nil {
			return err
		}
		// 2. Verify file exists
		var file model.File
		if err := tx.First(&file, "id = ?", req.Msg.FileId).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("file", req.Msg.FileId)
			}
			return errs.Internal(err)
		}

		// 3. Update page with new file_id
		if err := tx.Model(&page).Updates(structured.Fields{
			"featured_image_file_id": req.Msg.FileId,
			"updated_at":             time.Now(),
		}).Error; err != nil {
			return errs.Internal(err)
		}
		changed = true

		runID, err := s.runtime.RequestCurrentWithDB(
			ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_PAGE,
			req.Msg.PageId, "", true, "page_featured_image_updated",
		)
		if err != nil {
			return err
		}
		if runID != "" {
			ogRunID = &runID
		}
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPageFeaturedImageAuditRecord(metadata, page.ID, req.Msg.FileId, sharedtelemetry.AuditCollectionOperationAdded)
		})
	})

	if err != nil {
		return nil, err
	}
	featuredImageFileID := req.Msg.FileId
	featuredDeliveries, err := s.loadPageFeaturedImageDeliveries(ctx, []model.Page{{
		ID:                  req.Msg.PageId,
		FeaturedImageFileID: &featuredImageFileID,
	}})
	if err != nil {
		return nil, err
	}
	if changed {
		publishContentUpdatedEvent(
			ctx,
			s.asyncPublisher,
			buildManageMediaMutationContentUpdatedEvent(
				managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE,
				req.Msg.PageId,
				"media.featured_image",
			),
		)
	}

	return connect.NewResponse(&managev1.SetPageFeaturedImageResponse{
		ImageDelivery: featuredDeliveries[req.Msg.PageId], OgGenerationRunId: ogRunID,
	}), nil
}

// DeletePageFeaturedImage removes the featured image from a page (admin only)
func (s *PageService) DeletePageFeaturedImage(
	ctx context.Context,
	req *connect.Request[managev1.DeletePageFeaturedImageRequest],
) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	var oldFileID *string
	var ogRunID *string
	changed := false

	// Use transaction with row-level lock to prevent race conditions
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Lock and get page with current featured_image_file_id
		var page model.Page
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&page, "id = ?", req.Msg.PageId).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("page", req.Msg.PageId)
			}
			return errs.Internal(err)
		}
		if err := requireLockedPagePermission(ctx, tx, s.spiceDB, page.ID, policyv1.Page.Edit); err != nil {
			return err
		}
		// Store old file ID for cleanup after transaction
		oldFileID = page.FeaturedImageFileID
		if oldFileID == nil {
			return nil
		}
		// 2. Clear featured image file_id
		if err := tx.Model(&page).Updates(structured.Fields{
			"featured_image_file_id": nil,
			"updated_at":             time.Now(),
		}).Error; err != nil {
			return errs.Internal(err)
		}
		changed = true
		if err := s.runtime.ReleasePublicAssetBindings(
			ctx, tx, "page", req.Msg.PageId, "featured_image",
		); err != nil {
			return err
		}
		runID, err := s.runtime.RequestCurrentWithDB(
			ctx, tx,
			managev1.OgEntityType_OG_ENTITY_TYPE_PAGE,
			req.Msg.PageId, "", true, "page_featured_image_removed",
		)
		if err != nil {
			return err
		}
		if runID != "" {
			ogRunID = &runID
		}
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPageFeaturedImageAuditRecord(metadata, page.ID, *oldFileID, sharedtelemetry.AuditCollectionOperationRemoved)
		})
	})

	if err != nil {
		return nil, err
	}
	if changed {
		publishContentUpdatedEvent(
			ctx,
			s.asyncPublisher,
			buildManageMediaMutationContentUpdatedEvent(
				managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE,
				req.Msg.PageId,
				"media.featured_image",
			),
		)
	}

	return connect.NewResponse(&managev1.OgAssetDeleteResponse{Success: true, OgGenerationRunId: ogRunID}), nil
}

// Helper methods
