package page

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/documentlayout"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type CheckPageSlugAvailableQuery struct {
	Slug      string
	ExcludeID *string
}

func (s *Service) overlayPageSourceLocaleMetadataForPages(
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
			return domainerr.ResourceNotFound("page_translation", pages[i].ID)
		}
		overlayPageSourceLocaleMetadata(&pages[i], state)
	}
	return nil
}

// pageBy reads the management projection of the Page whose column equals
// value.
func (s *Service) pageBy(ctx context.Context, column string, value string) (*managev1.Page, error) {
	projection, err := s.loadPageContentProjection(ctx, column, value)
	if err != nil {
		return nil, err
	}
	page := &projection.page
	overlayPageSourceLocaleMetadata(page, projection.sourceMetadata)

	ogAsset, err := readyManageOgAssetRef(ctx, s.runtime, s.db, page.SourceLocaleOgAssetID, page.OgAssetID)
	if err != nil {
		return nil, err
	}
	featuredDeliveries, err := s.loadPageFeaturedImageDeliveries(ctx, []model.Page{*page})
	if err != nil {
		return nil, err
	}
	protoPage, err := toProtoPage(page, ogAsset, featuredDeliveries[page.ID])
	if err != nil {
		return nil, err
	}
	protoPage.Document = projection.document
	protoPage.Revision = projection.snapshot.Document.Revision.String()
	protoPage.BlockMedia = projection.blockMedia
	return protoPage, nil
}

// pageContentProjection is what one repeatable-read projection observed.
type pageContentProjection struct {
	page           model.Page
	sourceMetadata *PageSourceLocaleMetadata
	snapshot       contentblock.Snapshot
	document       *contentv1.PageDocument
	blockMedia     []*contentv1.ContentBlockMediaItem
}

func (s *Service) loadPageContentProjection(ctx context.Context, column string, value string) (pageContentProjection, error) {
	if column != "id" && column != "slug" {
		return pageContentProjection{}, errors.New("page read locator is invalid")
	}
	var projection pageContentProjection
	// The repeatable read snapshot with a share lock on the root conflicts with a
	// concurrent Page write (SQLSTATE 40001); the read is retried after it commits.
	err := dberrors.RetrySerializationFailure(ctx, func() error {
		projection = pageContentProjection{}
		return mutation.ReadLocked(ctx, s.db, func(tx *gorm.DB) error {
			principal, err := s.authorizePageProjection(ctx, tx, &projection.page, column, value)
			if err != nil {
				return err
			}
			return s.loadPageContent(ctx, tx, &projection, principal)
		})
	})
	if err != nil {
		return pageContentProjection{}, err
	}
	return projection, nil
}

// authorizePageProjection share-locks the Page into page and requires the
// caller to view it under its access policy. Every authorization failure
// answers not found.
func (s *Service) authorizePageProjection(ctx context.Context, tx *gorm.DB, page *model.Page, column, value string) (*auth.UserInfo, error) {
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where(column+" = ?", value).
		Take(page).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("page", value)
		}
		return nil, err
	}
	principal, err := authorizePagePermissionAfterRootLock(ctx, tx, s.permissions, page.ID, policyv1.Page.View)
	if err != nil {
		cause := failurecause.WithAttrs(err, slog.String("page_id", page.ID))
		if !mutation.IsUnauthenticated(err) && !mutation.IsDenied(err) && !mutation.IsNotFound(err) {
			// Every failure answers not found; a dependency failure is
			// still a server fault.
			cause = failurecause.ServerFault(cause)
		}
		return nil, domainerr.ResourceNotFound("page", value).WithCause(cause)
	}
	if err := pageaccess.Require(ctx, tx, s.permissions, page.ID, page.AccessPolicy); err != nil {
		return nil, err
	}
	return principal, nil
}

// loadPageContent loads the source locale document of the authorized Page
// and its Block media delivered to principal.
func (s *Service) loadPageContent(ctx context.Context, tx *gorm.DB, projection *pageContentProjection, principal *auth.UserInfo) error {
	pageID := projection.page.ID
	documentID, err := loadPageContentDocumentIDForRead(ctx, tx, pageID)
	if err != nil {
		return err
	}
	if projection.sourceMetadata, err = loadRequiredPageSourceLocaleMetadata(ctx, tx, pageID); err != nil {
		return err
	}
	if projection.snapshot, err = s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, documentID, projection.sourceMetadata.Locale); err != nil {
		return fmt.Errorf("load Page content document: %w", err)
	}
	if projection.document, err = MaterializePageContentDocument(projection.snapshot); err != nil {
		return fmt.Errorf("materialize Page content document: %w", err)
	}
	blockMedia, err := contentblock.LoadMediaReferences(ctx, tx, documentID)
	if err != nil {
		return fmt.Errorf("load Page Block media: %w", err)
	}
	projection.blockMedia, err = s.mediaHydrator.HydrateAuthorizedPageBlockMediaWithDB(
		auth.WithUser(ctx, principal), tx, pageID, documentID, principal, blockMedia,
	)
	if err != nil {
		return fmt.Errorf("hydrate Page Block media: %w", err)
	}
	return nil
}

func toProtoPage(
	p *model.Page,
	ogAsset *commonv1.AssetRef,
	featuredImage *commonv1.MediaDelivery,
) (*managev1.Page, error) {
	accessPolicy, err := pageaccess.Decode(p.AccessPolicy)
	if err != nil {
		return nil, err
	}
	documentLayout, err := documentlayout.ToProto(p.DocumentLayout)
	if err != nil {
		return nil, err
	}
	page := &managev1.Page{
		AccessPolicy:          accessPolicy,
		Id:                    p.ID,
		Title:                 p.Title,
		SourceLocale:          p.SourceLocale,
		Status:                pageStatusToProto(p.Status),
		ShowTitle:             p.ShowTitle,
		DocumentLayout:        documentLayout,
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
	return page, nil
}

func toProtoPageSummary(p *model.Page) (*managev1.PageSummary, error) {
	accessPolicy, err := pageaccess.Decode(p.AccessPolicy)
	if err != nil {
		return nil, err
	}
	summary := &managev1.PageSummary{
		AccessPolicy: accessPolicy,
		Id:           p.ID,
		Title:        p.Title,
		SourceLocale: p.SourceLocale,
		Status:       pageStatusToProto(p.Status),
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

	return summary, nil
}

// CheckPageSlugAvailable reports whether a slug is available. With
// ExcludeID the caller must be able to manage that Page.
func (s *Service) CheckPageSlugAvailable(ctx context.Context, query CheckPageSlugAvailableQuery) (*managev1.CheckPageSlugAvailableResponse, error) {
	excludeID := ""
	if query.ExcludeID != nil {
		excludeID = *query.ExcludeID
		if err := requirePagePermission(ctx, s.permissions, excludeID, policyv1.Page.Manage); err != nil {
			return nil, err
		}
	} else if err := requirePageCreate(ctx, s.permissions); err != nil {
		return nil, err
	}
	slug := strings.TrimSpace(query.Slug)
	if err := routeregistry.ValidatePagePath(slug); err != nil {
		return &managev1.CheckPageSlugAvailableResponse{Available: false}, nil //nolint:nilerr // an invalid path is an unavailable slug, not a failed check
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
	return &managev1.CheckPageSlugAvailableResponse{Available: available}, nil
}

// checkSlugAvailable checks if a page slug is available for use
// excludeID can be empty for new pages, or set to the current page ID when updating
func (s *Service) checkSlugAvailable(ctx context.Context, slug string, excludeID string) error {
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
		return domainerr.Duplicate("page", "slug", slug)
	}
	return nil
}
