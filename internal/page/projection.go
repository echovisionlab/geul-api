package page

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type CheckPageSlugAvailableQuery struct {
	Slug      string
	ExcludeID *string
}

// View is a Page with the title and summary of its source locale.
type View struct {
	Page    Page
	Title   string
	Summary *string
}

func newView(page Page, source *SourceMetadata) View {
	view := View{Page: page}
	if source != nil {
		view.Title = derefString(source.Title)
		view.Summary = source.Summary
	}
	return view
}

// Detail is a Page as the manage API renders it: its source-locale document,
// ready OG image, featured image and Block media.
type Detail struct {
	View
	OgAsset       *commonv1.AssetRef
	FeaturedImage *commonv1.MediaDelivery
	Document      *contentv1.PageDocument
	Revision      string
	BlockMedia    []*contentv1.ContentBlockMediaItem
}

// pageBy reads the management projection of the Page whose locator column
// equals value.
func (s *Service) pageBy(ctx context.Context, locator Locator, value string) (Detail, error) {
	projection, err := s.loadPageContentProjection(ctx, locator, value)
	if err != nil {
		return Detail{}, err
	}
	page := projection.page
	ogAsset, err := s.readyManageOgAssetRef(ctx, projection.sourceMetadata.OgAssetID, page.OgAssetID)
	if err != nil {
		return Detail{}, err
	}
	featuredImage, err := s.featuredImageDelivery(ctx, page.ID, page.FeaturedImageFileID)
	if err != nil {
		return Detail{}, err
	}
	return Detail{
		View: newView(page, &projection.sourceMetadata), OgAsset: ogAsset, FeaturedImage: featuredImage,
		Document: projection.document, Revision: projection.snapshot.Document.Revision.String(),
		BlockMedia: projection.blockMedia,
	}, nil
}

// pageContentProjection is what one repeatable-read projection observed.
type pageContentProjection struct {
	page           Page
	sourceMetadata SourceMetadata
	snapshot       contentblock.Snapshot
	document       *contentv1.PageDocument
	blockMedia     []*contentv1.ContentBlockMediaItem
}

func (s *Service) loadPageContentProjection(ctx context.Context, locator Locator, value string) (pageContentProjection, error) {
	if locator != ByID && locator != BySlug {
		return pageContentProjection{}, errors.New("page read locator is invalid")
	}
	var projection pageContentProjection
	// The repeatable read snapshot with a share lock on the root conflicts with a
	// concurrent Page write (SQLSTATE 40001); the read is retried after it commits.
	err := dberrors.RetrySerializationFailure(ctx, func() error {
		projection = pageContentProjection{}
		return s.units.ReadLocked(ctx, func(store Store) error {
			principal, err := s.authorizePageProjection(ctx, store, &projection.page, locator, value)
			if err != nil {
				return err
			}
			return loadPageContent(ctx, store, &projection, principal)
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
func (s *Service) authorizePageProjection(ctx context.Context, store Store, page *Page, locator Locator, value string) (*auth.UserInfo, error) {
	found, err := store.FindPage(ctx, locator, value, ShareLock)
	if err != nil {
		if errors.Is(err, ErrPageNotFound) {
			return nil, domainerr.ResourceNotFound("page", value)
		}
		return nil, err
	}
	*page = found
	require := func(can policyv1.Can) error { return requireFreshCan(ctx, store, s.spiceDB, can) }
	principal, err := authorizePagePermissionAfterRootLock(ctx, require, page.ID, policyv1.Page.View)
	if err != nil {
		cause := failurecause.WithAttrs(err, slog.String("page_id", page.ID))
		if !mutation.IsUnauthenticated(err) && !mutation.IsDenied(err) && !mutation.IsNotFound(err) {
			// Every failure answers not found; a dependency failure is
			// still a server fault.
			cause = failurecause.ServerFault(cause)
		}
		return nil, domainerr.ResourceNotFound("page", value).WithCause(cause)
	}
	if err := pageaccess.Require(ctx, store, s.spiceDB, page.ID, page.AccessPolicy); err != nil {
		return nil, err
	}
	return principal, nil
}

// loadPageContent loads the source locale document of the authorized Page
// and its Block media delivered to principal.
func loadPageContent(ctx context.Context, store Store, projection *pageContentProjection, principal *auth.UserInfo) error {
	pageID := projection.page.ID
	documentID, err := loadPageContentDocumentID(ctx, store, pageID, ShareLock)
	if err != nil {
		return err
	}
	source, err := loadRequiredPageSourceLocaleMetadata(ctx, store, pageID)
	if err != nil {
		return err
	}
	projection.sourceMetadata = source
	if projection.snapshot, err = store.LockedContentSnapshot(ctx, documentID, source.Locale); err != nil {
		return fmt.Errorf("load Page content document: %w", err)
	}
	if projection.document, err = contentblock.SnapshotToPageDocument(projection.snapshot); err != nil {
		return fmt.Errorf("materialize Page content document: %w", err)
	}
	blockMedia, err := store.MediaReferences(ctx, documentID)
	if err != nil {
		return fmt.Errorf("load Page Block media: %w", err)
	}
	projection.blockMedia, err = store.HydrateBlockMedia(auth.WithUser(ctx, principal), pageID, documentID, principal, blockMedia)
	if err != nil {
		return fmt.Errorf("hydrate Page Block media: %w", err)
	}
	return nil
}

// readyManageOgAssetRef resolves the first ready OG image of the candidate
// asset IDs.
func (s *Service) readyManageOgAssetRef(ctx context.Context, candidateIDs ...*string) (*commonv1.AssetRef, error) {
	ids := make([]string, 0, len(candidateIDs))
	seen := make(map[string]struct{}, len(candidateIDs))
	for _, candidateID := range candidateIDs {
		if candidateID == nil || strings.TrimSpace(*candidateID) == "" {
			continue
		}
		id := strings.TrimSpace(*candidateID)
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var ready map[string]*commonv1.AssetRef
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		ready, err = store.ReadyAssets(ctx, ids)
		return err
	}); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if ref := ready[id]; ref != nil {
			return ref, nil
		}
	}
	return nil, nil
}

// featuredImageDelivery delivers the Page's featured image, which must
// still be fileID.
func (s *Service) featuredImageDelivery(ctx context.Context, pageID string, fileID *string) (*commonv1.MediaDelivery, error) {
	expectedFileID := ""
	if fileID != nil {
		expectedFileID = strings.TrimSpace(*fileID)
	}
	return s.files.ResolveAuthorizedPageFeaturedImage(ctx, pageID, expectedFileID)
}

// CheckPageSlugAvailable reports whether a slug is available. With
// ExcludeID the caller must be able to manage that Page.
func (s *Service) CheckPageSlugAvailable(ctx context.Context, query CheckPageSlugAvailableQuery) (bool, error) {
	excludeID := ""
	if query.ExcludeID != nil {
		excludeID = *query.ExcludeID
		if err := requirePagePermission(ctx, s.spiceDB, excludeID, policyv1.Page.Manage); err != nil {
			return false, err
		}
	} else if err := requirePageCreate(ctx, s.spiceDB); err != nil {
		return false, err
	}
	slug := strings.TrimSpace(query.Slug)
	if err := routeregistry.ValidatePagePath(slug); err != nil {
		return false, nil //nolint:nilerr // an invalid path is an unavailable slug, not a failed check
	}
	var available bool
	err := s.units.Read(ctx, func(store Store) error {
		taken, err := slugTaken(ctx, store, slug, excludeID)
		if err != nil || taken {
			return err
		}
		occupied, err := store.RouteOccupied(ctx, slug)
		available = !occupied
		return err
	})
	return available, err
}

// slugTaken reports whether a Page other than excludeID holds slug; no
// Page holds the empty slug.
func slugTaken(ctx context.Context, store Store, slug, excludeID string) (bool, error) {
	if slug == "" {
		return false, nil
	}
	return store.SlugTaken(ctx, slug, excludeID)
}

// checkSlugAvailable checks if a page slug is available for use
// excludeID can be empty for new pages, or set to the current page ID when updating
func (s *Service) checkSlugAvailable(ctx context.Context, slug string, excludeID string) error {
	if err := routeregistry.ValidatePagePath(slug); err != nil {
		return err
	}
	return s.units.Read(ctx, func(store Store) error {
		return requireSlugAvailable(ctx, store, slug, excludeID)
	})
}

// requireSlugAvailable rejects a slug another Page or resource route holds.
func requireSlugAvailable(ctx context.Context, store Store, slug, excludeID string) error {
	taken, err := slugTaken(ctx, store, slug, excludeID)
	if err != nil {
		return err
	}
	if taken {
		return domainerr.Duplicate("page", "slug", slug)
	}
	occupied, err := store.RouteOccupied(ctx, slug)
	if err != nil {
		return err
	}
	if occupied {
		return domainerr.Duplicate("page", "slug", slug)
	}
	return nil
}
