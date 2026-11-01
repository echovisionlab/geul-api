package release

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/pathslug"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// GetReleaseQuery names a Release.
type GetReleaseQuery struct {
	ID string
}

// ListReleasesQuery pages through every Release. A Limit of zero or less
// takes the default page of 20.
type ListReleasesQuery struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Limit   int32
	Offset  int32
}

// ListPage is the page of Releases a Store lists: filtered, sorted, then
// ordered by ID, and paged.
type ListPage struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Limit   int32
	Offset  int32
}

// View is a Release with its source title, content document and revision,
// and its artwork, which is its Open Graph image as well.
type View struct {
	Release  Release
	Title    string
	Revision string
	Document *contentv1.RichTextDocument
	Artwork  *commonv1.AssetRef
}

// ReleaseWithStats is a Release with its Track and credit counts.
type ReleaseWithStats struct {
	View        View
	TrackCount  int32
	CreditCount int32
}

// ReleasePage is one page of Releases with their Track and credit counts.
type ReleasePage struct {
	Releases []ReleaseWithStats
	Total    int64
	Limit    int32
	Offset   int32
}

// CheckReleaseSlugQuery asks whether Slug is free, ignoring the Release
// ExcludeReleaseID names.
type CheckReleaseSlugQuery struct {
	Slug             string
	ExcludeReleaseID *string
}

// GetRelease retrieves a release by ID for a Site Admin.
func (s *Service) GetRelease(ctx context.Context, query GetReleaseQuery) (View, error) {
	if err := requireReleaseAction(ctx, s.spiceDB, query.ID, releaseActionView); err != nil {
		return View{}, err
	}
	var release Release
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		release, err = store.FindRelease(ctx, query.ID)
		return err
	}); err != nil {
		return View{}, releaseMissingAs(err, releaseNotFound(query.ID))
	}
	view, err := s.overlayReleaseSourceLocaleDocument(ctx, release)
	if err != nil {
		return View{}, err
	}
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		view.Artwork, err = store.ReadyArtwork(ctx, view.Release.ID)
		return err
	}); err != nil {
		return View{}, err
	}
	return view, nil
}

// ListReleasesAdmin returns a page of all releases with stats.
func (s *Service) ListReleasesAdmin(ctx context.Context, query ListReleasesQuery) (ReleasePage, error) {
	if err := requireReleaseList(ctx, s.spiceDB); err != nil {
		return ReleasePage{}, err
	}
	page := ReleasePage{Limit: 20, Offset: query.Offset}
	if query.Limit > 0 {
		page.Limit = query.Limit
	}
	var releases []Release
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		releases, page.Total, err = store.ListReleases(ctx, ListPage{
			Filters: query.Filters, Sorts: query.Sorts, Limit: page.Limit, Offset: page.Offset,
		})
		return err
	}); err != nil {
		return ReleasePage{}, err
	}
	var err error
	page.Releases, err = s.releasesWithStats(ctx, releases)
	return page, err
}

func (s *Service) releasesWithStats(ctx context.Context, releases []Release) ([]ReleaseWithStats, error) {
	views := make([]View, len(releases))
	for i := range releases {
		var err error
		if views[i], err = s.overlayReleaseSourceLocaleDocument(ctx, releases[i]); err != nil {
			return nil, err
		}
	}
	releaseIDs := collectManageReleaseIDs(views)
	var artworkAssets map[string]*commonv1.AssetRef
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		artworkAssets, err = store.ReadyArtworks(ctx, releaseIDs)
		return err
	}); err != nil {
		return nil, err
	}
	var trackCounts, creditCounts map[string]int32
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		trackCounts, err = store.CountTracksPerRelease(ctx, releaseIDs)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to get track counts: %w", err)
	}
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		creditCounts, err = store.CountCreditsPerRelease(ctx, releaseIDs)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to get credit counts: %w", err)
	}
	result := make([]ReleaseWithStats, len(views))
	for i, view := range views {
		view.Artwork = artworkAssets[view.Release.ID]
		result[i] = ReleaseWithStats{
			View: view, TrackCount: trackCounts[view.Release.ID], CreditCount: creditCounts[view.Release.ID],
		}
	}
	return result, nil
}

// CheckReleaseSlugAvailable checks if a slug is available (Site Admin only).
func (s *Service) CheckReleaseSlugAvailable(ctx context.Context, query CheckReleaseSlugQuery) (bool, error) {
	// Checking for a specific release requires edit permission on it; checking
	// for a new release requires the create permission.
	if query.ExcludeReleaseID != nil && *query.ExcludeReleaseID != "" {
		if err := requireReleaseAction(ctx, s.spiceDB, *query.ExcludeReleaseID, releaseActionEdit); err != nil {
			return false, err
		}
	} else if err := requireReleaseCreate(ctx, s.spiceDB); err != nil {
		return false, err
	}
	if err := pathslug.Validate(query.Slug); err != nil {
		return false, err
	}
	var available bool
	err := s.units.Read(ctx, func(store Store) error {
		taken, err := store.SlugTaken(ctx, query.Slug, query.ExcludeReleaseID)
		if err != nil || taken {
			return err
		}
		available, err = store.ResourceRouteAvailable(ctx, query.Slug)
		return err
	})
	return available, err
}

// overlayReleaseSourceLocaleDocument rereads release with its source title
// and revision in one snapshot that share-locks it, and returns it with its
// content document; the caller adds the artwork.
func (s *Service) overlayReleaseSourceLocaleDocument(ctx context.Context, release Release) (View, error) {
	releaseID := release.ID
	projection, err := s.loadReleaseManageContentProjection(ctx, releaseID, func(ctx context.Context, store Store) error {
		current, err := store.LockRelease(ctx, releaseID, ShareLock)
		if err != nil {
			return err
		}
		release = current
		return nil
	})
	if err != nil {
		return View{}, err
	}
	return View{
		Release: release, Title: projection.SourceTitle, Revision: projection.Revision,
		Document: projection.Document,
	}, nil
}

func collectManageReleaseIDs(views []View) []string {
	ids := make([]string, 0, len(views))
	for _, view := range views {
		ids = append(ids, view.Release.ID)
	}
	return ids
}
