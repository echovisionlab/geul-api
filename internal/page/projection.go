package page

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *PageService) overlayPageSourceLocaleMetadataForPages(
	ctx context.Context,
	pages []model.Page,
) error {
	sourceStates, err := loadPageSourceLocaleMetadataByPageID(ctx, s.db, collectManagePageIDs(pages))
	if err != nil {
		return err
	}
	for i := range pages {
		state := sourceStates[pages[i].ID]
		if state == nil {
			return errs.NotFound("page_translation", pages[i].ID)
		}
		overlayPageSourceLocaleMetadata(&pages[i], state)
	}
	return nil
}

func (s *PageService) pageResponseWithReadyOg(
	ctx context.Context,
	page *model.Page,
) (*connect.Response[managev1.Page], error) {
	if page == nil {
		return nil, errs.InternalMsg("Page is required")
	}
	return s.pageResponseByWithReadyOg(ctx, "id", page.ID)
}

func (s *PageService) pageResponseByWithReadyOg(
	ctx context.Context,
	column string,
	value string,
) (*connect.Response[managev1.Page], error) {
	page, document, snapshot, sourceMetadata, blockMedia, err := s.loadPageContentProjection(ctx, column, value)
	if err != nil {
		return nil, err
	}
	if s.mediaHydrator == nil {
		return nil, errs.InternalMsg("Page Block media hydrator is not configured")
	}
	overlayPageSourceLocaleMetadata(page, sourceMetadata)

	ogAsset, err := readyManageOgAssetRef(
		ctx,
		s.runtime,
		s.db,
		page.SourceLocaleOgAssetID,
		page.OgAssetID,
	)
	if err != nil {
		return nil, err
	}
	featuredDeliveries, err := s.loadPageFeaturedImageDeliveries(ctx, []model.Page{*page})
	if err != nil {
		return nil, err
	}
	protoPage := s.toProtoPage(page, ogAsset, featuredDeliveries[page.ID])
	protoPage.Document = document
	protoPage.Revision = snapshot.Document.Revision.String()
	protoPage.BlockMedia = blockMedia
	return connect.NewResponse(protoPage), nil
}

func (s *PageService) loadPageContentProjection(
	ctx context.Context,
	column string,
	value string,
) (*model.Page, *contentv1.PageDocument, contentblock.Snapshot, *PageSourceLocaleMetadata, []*contentv1.ContentBlockMediaItem, error) {
	if column != "id" && column != "slug" {
		return nil, nil, contentblock.Snapshot{}, nil, nil, errs.InternalMsg("Page read locator is invalid")
	}
	if s.contentBlocks == nil {
		return nil, nil, contentblock.Snapshot{}, nil, nil, errs.InternalMsg("Page content Block store is not configured")
	}
	var page model.Page
	var sourceMetadata *PageSourceLocaleMetadata
	var snapshot contentblock.Snapshot
	var document *contentv1.PageDocument
	var blockMedia []*contentv1.ContentBlockMediaItem
	var principal *auth.UserInfo
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "SHARE"}).
			Where(column+" = ?", value).
			Take(&page).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("page", value)
			}
			return errs.Internal(err)
		}
		principal, err = authorizePagePermissionAfterRootLock(
			ctx, tx, s.spiceDB, page.ID, policyv1.Page.View,
		)
		if err != nil {
			return errs.NotFound("page", value)
		}
		documentID, err := loadPageContentDocumentIDForRead(ctx, tx, page.ID)
		if err != nil {
			return err
		}
		sourceMetadata, err = loadRequiredPageSourceLocaleMetadata(ctx, tx, page.ID)
		if err != nil {
			return err
		}
		snapshot, err = s.contentBlocks.LoadSnapshotInTransaction(
			ctx,
			tx,
			documentID,
			sourceMetadata.Locale,
		)
		if err != nil {
			return errs.Internal(fmt.Errorf("load Page content document: %w", err))
		}
		document, err = MaterializePageContentDocument(snapshot)
		if err != nil {
			return errs.Internal(fmt.Errorf("materialize Page content document: %w", err))
		}
		blockMedia, err = LoadContentBlockMediaReferences(ctx, tx, documentID)
		if err != nil {
			return errs.Internal(fmt.Errorf("load Page Block media: %w", err))
		}
		blockMedia, err = s.mediaHydrator.HydrateAuthorizedPageBlockMediaWithDB(
			auth.WithUser(ctx, principal), tx, page.ID, documentID, principal, blockMedia,
		)
		if err != nil {
			return errs.Internal(fmt.Errorf("hydrate Page Block media: %w", err))
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, nil, contentblock.Snapshot{}, nil, nil, err
	}
	return &page, document, snapshot, sourceMetadata, blockMedia, nil
}

func (s *PageService) toProtoPage(
	p *model.Page,
	ogAsset *commonv1.AssetRef,
	featuredImage *commonv1.MediaDelivery,
) *managev1.Page {
	page := &managev1.Page{
		Id:                    p.ID,
		Title:                 p.Title,
		SourceLocale:          p.SourceLocale,
		Status:                managev1.PageStatus(managev1.PageStatus_value[string(p.Status)]),
		ShowTitle:             p.ShowTitle,
		DocumentLayout:        p.DocumentLayout.Proto(),
		CreatedAt:             timestamppb.New(p.CreatedAt),
		UpdatedAt:             timestamppb.New(p.UpdatedAt),
		OgAsset:               ogAsset,
		FeaturedImageDelivery: featuredImage,
	}

	if p.Slug != nil {
		page.Slug = p.Slug
	}
	if p.Summary != nil {
		page.Summary = p.Summary
	}
	if p.PublishedAt != nil {
		page.PublishedAt = timestamppb.New(*p.PublishedAt)
	}
	return page
}

func (s *PageService) toProtoPageSummary(p *model.Page) *managev1.PageSummary {
	summary := &managev1.PageSummary{
		Id:           p.ID,
		Title:        p.Title,
		SourceLocale: p.SourceLocale,
		Status:       managev1.PageStatus(managev1.PageStatus_value[string(p.Status)]),
		ShowTitle:    p.ShowTitle,
		CreatedAt:    timestamppb.New(p.CreatedAt),
		UpdatedAt:    timestamppb.New(p.UpdatedAt),
	}

	if p.Slug != nil {
		summary.Slug = p.Slug
	}
	if p.PublishedAt != nil {
		summary.PublishedAt = timestamppb.New(*p.PublishedAt)
	}

	return summary
}

// CheckPageSlugAvailable checks if a slug is available.
func (s *PageService) CheckPageSlugAvailable(
	ctx context.Context,
	req *connect.Request[managev1.CheckPageSlugAvailableRequest],
) (*connect.Response[managev1.CheckPageSlugAvailableResponse], error) {
	excludeID := ""
	if req.Msg.ExcludeId != nil {
		excludeID = *req.Msg.ExcludeId
		if err := requirePagePermission(ctx, s.spiceDB, excludeID, policyv1.Page.Manage); err != nil {
			return nil, err
		}
	} else if err := requirePageCreate(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	slug := strings.TrimSpace(req.Msg.Slug)
	if err := routeregistry.ValidatePagePath(slug); err != nil {
		return connect.NewResponse(&managev1.CheckPageSlugAvailableResponse{Available: false}), nil
	}

	available, err := isSlugAvailable(ctx, s.db, &model.Page{}, slug, excludeID)
	if err != nil {
		return nil, err
	}
	if available {
		occupied, err := routeregistry.IsPageRouteOccupiedByResource(ctx, s.db, slug)
		if err != nil {
			return nil, err
		}
		available = !occupied
	}

	return connect.NewResponse(&managev1.CheckPageSlugAvailableResponse{
		Available: available,
	}), nil
}

// checkSlugAvailable checks if a page slug is available for use
// excludeID can be empty for new pages, or set to the current page ID when updating
func (s *PageService) checkSlugAvailable(ctx context.Context, slug string, excludeID string) error {
	if err := routeregistry.ValidatePagePath(slug); err != nil {
		return err
	}
	if err := ensureSlugAvailable(ctx, s.db, &model.Page{}, "page", slug, excludeID); err != nil {
		return err
	}
	occupied, err := routeregistry.IsPageRouteOccupiedByResource(ctx, s.db, slug)
	if err != nil {
		return err
	}
	if occupied {
		return errs.SlugAlreadyExists("page", slug)
	}
	return nil
}

// ============================================================================
// Version History
// ============================================================================

// ListPageVersions returns a paginated list of version snapshots for a page.
