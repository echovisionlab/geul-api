package public

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
)

// artistReleasesSortFields are the sort fields of the Artist Releases.
var artistReleasesSortFields = map[string]bool{"release_date": true, "published_at": true, "title": true}

// artistWorksSortAllowedFields defines allowed sort fields for artist works
var artistWorksSortAllowedFields = map[string]bool{
	"title":        true,
	"published_at": true,
}

// MediaProjection resolves the public media of Artists and what they link.
type MediaProjection interface {
	LoadArtistImages(context.Context, []string) (map[string][]artistdomain.ArtistImageProjection, error)
	ResolveReadyAssetRefs(context.Context, []*string) (map[string]*commonv1.AssetRef, error)
	ResolveArtistOGAsset(context.Context, *string, *string) (*commonv1.AssetRef, error)
	IsReadyAsset(context.Context, string) (bool, error)
	LoadReleaseArtworkAssets(context.Context, []string) (map[string]*commonv1.AssetRef, error)
	LoadWorkImageAssets(context.Context, map[string]*string) (map[string]*commonv1.AssetRef, error)
	LoadReadySourceFileAssets(context.Context, string, []string) (map[string]*commonv1.AssetRef, error)
}

// Deps are the collaborators of the public Artist reads; all are required.
type Deps struct {
	Units   mutation.UnitOfWork[Store] // artistpublicpostgres.NewUnits
	SpiceDB authz.AuthorizationDecisionChecker
	Media   MediaProjection
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Media": d.Media == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public artist dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the public Artist reads.
type Service struct {
	units   mutation.UnitOfWork[Store]
	spiceDB authz.AuthorizationDecisionChecker
	media   MediaProjection
}

// NewService validates deps and returns the public Artist reads.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{units: deps.Units, spiceDB: deps.SpiceDB, media: deps.Media}, nil
}

var errArtistNotFound = domainerr.New(domainerr.NotFound, "", "artist not found")

// ListArtistsQuery pages through published Artists.
type ListArtistsQuery struct {
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// Pagination is where a page sits among every published match.
type Pagination struct {
	Total   int32
	Limit   int32
	Offset  int32
	HasMore bool
}

// Summary is a published Artist as a list shows it.
type Summary struct {
	ID          string
	Name        string
	Slug        *string
	CountryCode *string
	PublishedAt *time.Time
	SocialLinks map[string]string
	Image       *commonv1.AssetRef
	OgImage     *commonv1.AssetRef
}

// ArtistPage is one page of published Artist summaries.
type ArtistPage struct {
	Artists    []Summary
	Pagination Pagination
}

// List returns published artists with filtering and sorting
func (s *Service) List(ctx context.Context, query ListArtistsQuery) (ArtistPage, error) {
	pagination := queryutil.GetPaginationParams(query.Limit, query.Offset, 20)
	var rows []artistdomain.Artist
	var total int64
	var titles map[string]string
	var ogAssetIDs map[string]*string
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		if rows, total, err = store.ListPublishedArtists(ctx, ListPage{
			Filters: query.Filters, Sorts: query.Sorts, Limit: pagination.Limit, Offset: pagination.Offset,
		}); err != nil {
			return err
		}
		ids := collectArtistIDs(rows)
		if titles, err = sourceTitles(ctx, store, ids); err != nil {
			return err
		}
		ogAssetIDs, err = store.SourceOgAssetIDs(ctx, ids)
		return err
	}); err != nil {
		return ArtistPage{}, err
	}
	summaries, err := s.artistSummaries(ctx, rows, titles, ogAssetIDs)
	if err != nil {
		return ArtistPage{}, err
	}
	return ArtistPage{Artists: summaries, Pagination: Pagination{
		Total:   int32(total),
		Limit:   pagination.Limit,
		Offset:  pagination.Offset,
		HasMore: pagination.Offset+int32(len(rows)) < int32(total),
	}}, nil
}

// sourceTitles maps each Artist to its source-locale title; one without
// answers not found.
func sourceTitles(ctx context.Context, store Store, artistIDs []string) (map[string]string, error) {
	titles := make(map[string]string, len(artistIDs))
	if len(artistIDs) == 0 {
		return titles, nil
	}
	titles, err := store.SourceTitles(ctx, artistIDs)
	if err != nil {
		return nil, err
	}
	for _, id := range artistIDs {
		if _, ok := titles[id]; !ok {
			return nil, domainerr.ResourceNotFound("artist_translation", id)
		}
	}
	return titles, nil
}

// artistSummaries renders artists with their source titles, primary images
// and OG images.
func (s *Service) artistSummaries(ctx context.Context, artists []artistdomain.Artist, titles map[string]string, ogAssetIDs map[string]*string) ([]Summary, error) {
	readyImages, err := s.media.LoadArtistImages(ctx, collectArtistIDs(artists))
	if err != nil {
		return nil, err
	}
	ogCandidates := make([]*string, 0, len(artists))
	for _, value := range artists {
		ogCandidates = append(ogCandidates, ogAssetIDs[value.ID])
	}
	readyOgAssets, err := s.media.ResolveReadyAssetRefs(ctx, ogCandidates)
	if err != nil {
		return nil, err
	}
	summaries := make([]Summary, 0, len(artists))
	for _, value := range artists {
		summary := buildArtistSummary(value, titles[value.ID])
		if images := readyImages[value.ID]; len(images) > 0 {
			summary.Image = images[0].Asset
		}
		if ogAssetID := ogAssetIDs[value.ID]; ogAssetID != nil {
			summary.OgImage = readyOgAssets[*ogAssetID]
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func buildArtistSummary(value artistdomain.Artist, name string) Summary {
	summary := Summary{ID: value.ID, Name: name, Slug: value.Slug, CountryCode: value.CountryCode, PublishedAt: value.PublishedAt}
	if len(value.SocialLinks) > 0 {
		summary.SocialLinks = value.SocialLinks
	}
	return summary
}

// GetArtistQuery reads one Artist by ID or slug. A draft Artist needs a
// share token (and its password) or the caller's exact view permission.
// AcceptLanguage picks the displayed locale.
type GetArtistQuery struct {
	SlugOrID       string
	ShareToken     string
	SharePassword  string
	AcceptLanguage string
}

// Get retrieves an artist by slug or ID
// - UUID-first: if input is valid UUID, query by ID; otherwise query by slug
// - share_token allows access to draft artists
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *Service) Get(ctx context.Context, query GetArtistQuery) (Detail, error) {
	var value artistdomain.Artist
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		value, err = store.FindArtist(ctx, query.SlugOrID, isValidUUID(query.SlugOrID))
		return err
	}); err != nil {
		if errors.Is(err, artistdomain.ErrArtistNotFound) {
			return Detail{}, errArtistNotFound
		}
		return Detail{}, err
	}
	if value.Status != artistdomain.StatusPublished {
		if err := s.requireDraftArtistAccess(ctx, value.ID, query.ShareToken, query.SharePassword); err != nil {
			return Detail{}, err
		}
	}
	return s.buildArtistResponse(ctx, query.AcceptLanguage, value)
}

func (s *Service) requireDraftArtistAccess(ctx context.Context, artistID, shareToken, sharePassword string) error {
	allowed, err := hasDraftResourceView(ctx, s.spiceDB, artistID)
	if err != nil {
		return fmt.Errorf("check artist draft view permission: %w", err)
	}
	if allowed {
		return nil
	}
	return s.requireOpaqueDraftShareLinkAccess(ctx, shareToken, sharePassword, artistID, "artist")
}

func hasDraftResourceView(ctx context.Context, checker authz.AuthorizationDecisionChecker, resourceID string) (bool, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.Banned || strings.TrimSpace(user.IdentityID.String()) == "" {
		return false, nil
	}
	can, err := policyv1.Artist.View(resourceID)
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, err
	}
	return checker.Can(ctx, decision)
}

func (s *Service) requireOpaqueDraftShareLinkAccess(ctx context.Context, token, password, entityID, entityName string) error {
	if token == "" {
		return domainerr.New(domainerr.NotFound, "", entityName+" not found")
	}
	var passwordHash *string
	var found bool
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		passwordHash, found, err = store.ShareLink(ctx, token, entityID, time.Now())
		return err
	}); err != nil {
		return domainerr.New(domainerr.NotFound, "", entityName+" not found").WithCause(failurecause.ServerFault(failurecause.WithPrivate(err, token, password)))
	}
	if !found {
		return domainerr.New(domainerr.NotFound, "", entityName+" not found")
	}
	if passwordHash == nil {
		return nil
	}
	if password == "" {
		return domainerr.New(domainerr.NotFound, "", entityName+" not found")
	}
	match, err := crypto.NewPasswordHasher(nil).Verify(password, *passwordHash)
	if err != nil {
		return domainerr.New(domainerr.NotFound, "", entityName+" not found").WithCause(failurecause.ServerFault(failurecause.WithPrivate(err, token, password, *passwordHash)))
	}
	if !match {
		return domainerr.New(domainerr.NotFound, "", entityName+" not found")
	}
	return nil
}

// ArtistWorksQuery pages through the published Releases or Works of an
// Artist. A zero Limit is the default page.
type ArtistWorksQuery struct {
	ArtistID       string
	Limit          int32
	Offset         *int32
	Sorts          []*commonv1.SortSpec
	AcceptLanguage string
}

func (q ArtistWorksQuery) page() (int32, int32) {
	limit := int32(20)
	if q.Limit > 0 {
		limit = q.Limit
	}
	offset := int32(0)
	if q.Offset != nil {
		offset = *q.Offset
	}
	return limit, offset
}

// Release is a published Release of an Artist with its other Artists.
type Release struct {
	ID          string
	Title       string
	Slug        *string
	Type        string
	ReleaseDate *time.Time
	PublishedAt *time.Time
	Artwork     *commonv1.AssetRef
	Artists     []ReleaseArtist
}

// ReleaseArtist is another Artist credited on a Release.
type ReleaseArtist struct {
	ID   string
	Name string
	Slug *string
}

// ReleasesPage is one page of an Artist's published Releases.
type ReleasesPage struct {
	Releases []Release
	Total    int32
}

// GetReleases returns releases by an artist
func (s *Service) GetReleases(ctx context.Context, query ArtistWorksQuery) (ReleasesPage, error) {
	artistID := query.ArtistID
	limit, offset := query.page()
	sorts := make([]Sort, 0, len(query.Sorts))
	for _, sortSpec := range query.Sorts {
		if !artistReleasesSortFields[sortSpec.GetField()] {
			return ReleasesPage{}, domainerr.New(domainerr.Invalid, "", "invalid sort field: "+sortSpec.GetField())
		}
		sorts = append(sorts, Sort{Field: sortSpec.GetField(), Descending: sortSpec.GetOrder() == commonv1.SortOrder_SORT_ORDER_DESC})
	}
	var releases []ReleaseRow
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		releases, err = store.PublishedReleases(ctx, artistID, sorts, limit, offset)
		return err
	}); err != nil {
		return ReleasesPage{}, err
	}
	var total int64
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		total, err = store.PublishedReleaseCount(ctx, artistID)
		return err
	}); err != nil {
		slog.ErrorContext(ctx, "Artist release count query failed; reporting a zero total", "domain", "artist", "operation", "count_artist_releases", failurecause.Attr(err), "artist_id", artistID)
	}
	releaseIDs := make([]string, 0, len(releases))
	for _, release := range releases {
		releaseIDs = append(releaseIDs, release.ID)
	}
	artworkAssets, err := s.media.LoadReleaseArtworkAssets(ctx, releaseIDs)
	if err != nil {
		return ReleasesPage{}, err
	}
	otherArtists := make(map[string][]ReleaseArtist, len(releaseIDs))
	if len(releaseIDs) > 0 {
		var rows []ReleaseArtistRow
		if err := s.units.Read(ctx, func(store Store) error {
			var err error
			rows, err = store.ReleaseArtists(ctx, releaseIDs, artistID)
			return err
		}); err != nil {
			return ReleasesPage{}, err
		}
		for _, row := range rows {
			otherArtists[row.ReleaseID] = append(otherArtists[row.ReleaseID], ReleaseArtist{ID: row.ArtistID, Name: row.ArtistName, Slug: row.ArtistSlug})
		}
	}
	page := ReleasesPage{Releases: make([]Release, 0, len(releases)), Total: int32(total)}
	for _, row := range releases {
		page.Releases = append(page.Releases, Release{
			ID: row.ID, Title: row.Title, Slug: row.Slug, Type: row.Type, ReleaseDate: row.ReleaseDate, PublishedAt: row.PublishedAt,
			Artwork: artworkAssets[row.ID], Artists: otherArtists[row.ID],
		})
	}
	return page, nil
}

// Work is a published or archived Work of an Artist in the displayed
// locale. Type is the stored Work type.
type Work struct {
	ID          string
	Title       string
	Slug        *string
	Summary     *string
	Type        string
	PublishedAt *time.Time
	Image       *commonv1.AssetRef
}

// WorksPage is one page of an Artist's visible Works.
type WorksPage struct {
	Works []Work
	Total int32
}

// GetWorks returns works by an artist
func (s *Service) GetWorks(ctx context.Context, query ArtistWorksQuery) (WorksPage, error) {
	artistID := query.ArtistID
	limit, offset := query.page()
	sorts := make([]Sort, 0, len(query.Sorts))
	for _, sortSpec := range query.Sorts {
		if !artistWorksSortAllowedFields[sortSpec.GetField()] {
			return WorksPage{}, domainerr.New(domainerr.Invalid, "", "invalid sort field: "+sortSpec.GetField())
		}
		sorts = append(sorts, Sort{Field: sortSpec.GetField(), Descending: sortSpec.GetOrder() == commonv1.SortOrder_SORT_ORDER_DESC})
	}
	var works []WorkRow
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		works, err = store.VisibleWorks(ctx, artistID, sorts, limit, offset)
		return err
	}); err != nil {
		return WorksPage{}, err
	}
	workIDs := make([]string, 0, len(works))
	for _, work := range works {
		workIDs = append(workIDs, work.ID)
	}
	var localizedSelections map[string]publiccontent.Selection
	// Resolving a localization stores the default translation settings
	// when none exist, which a read-only snapshot refuses.
	if err := s.units.ReadLocked(ctx, func(store Store) error {
		var err error
		localizedSelections, err = store.WorkLocalizations(ctx, workIDs, query.AcceptLanguage)
		return err
	}); err != nil {
		slog.ErrorContext(ctx, "Artist work localization lookup failed; serving unlocalized Works", "domain", "artist", "operation", "resolve_work_localizations", failurecause.Attr(err), "artist_id", artistID)
	}
	var total int64
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		total, err = store.VisibleWorkCount(ctx, artistID)
		return err
	}); err != nil {
		slog.ErrorContext(ctx, "Artist work count query failed; reporting a zero total", "domain", "artist", "operation", "count_artist_works", failurecause.Attr(err), "artist_id", artistID)
	}
	workFileIDs := make(map[string]*string, len(works))
	for _, work := range works {
		workFileIDs[work.ID] = work.ImageFileID
	}
	workImageAssets, err := s.media.LoadWorkImageAssets(ctx, workFileIDs)
	if err != nil {
		return WorksPage{}, err
	}
	page := WorksPage{Works: make([]Work, 0, len(works)), Total: int32(total)}
	for _, row := range works {
		work := Work{ID: row.ID, Title: row.Title, Slug: row.Slug, Summary: row.Summary, Type: row.Type, PublishedAt: row.PublishedAt, Image: workImageAssets[row.ID]}
		if localized, ok := localizedSelections[row.ID]; ok {
			if localized.Title != nil {
				work.Title = *localized.Title
			}
			if localized.Summary != nil {
				work.Summary = localized.Summary
			}
		}
		page.Works = append(page.Works, work)
	}
	return page, nil
}

// Detail is one Artist as the public API renders it.
type Detail struct {
	Artist       artistdomain.Artist
	Name         string
	Document     *contentv1.LocalizedRichTextDocument
	Revision     string
	IsGroup      bool
	Localization publiccontent.Selection
	OgImage      *commonv1.AssetRef
	Images       []artistdomain.ArtistImageProjection
	ReleaseCount int32
	WorkCount    int32
	Labels       []Label
}

// Label is a published Label of an Artist.
type Label struct {
	ID    string
	Name  string
	Slug  *string
	Image *commonv1.AssetRef
}

// buildArtistResponse projects the Artist in one snapshot that waits out a
// concurrent writer of its row.
func (s *Service) buildArtistResponse(ctx context.Context, acceptLanguage string, value artistdomain.Artist) (Detail, error) {
	wasPublic := value.Status == artistdomain.StatusPublished
	var detail Detail
	err := s.units.ReadLocked(ctx, func(store Store) error {
		var err error
		detail, err = s.buildArtistResponseInTransaction(ctx, store, acceptLanguage, value.ID, wasPublic)
		return err
	})
	return detail, err
}

func (s *Service) buildArtistResponseInTransaction(ctx context.Context, store Store, acceptLanguage, artistID string, wasPublic bool) (Detail, error) {
	current, err := store.ShareLockArtist(ctx, artistID)
	if err != nil {
		if errors.Is(err, artistdomain.ErrArtistNotFound) {
			return Detail{}, errArtistNotFound
		}
		return Detail{}, err
	}
	if wasPublic && current.Status != artistdomain.StatusPublished {
		return Detail{}, errArtistNotFound
	}
	localization, err := store.ResolveLocalization(ctx, artistID, acceptLanguage)
	if err != nil {
		slog.ErrorContext(ctx, "Artist localization lookup failed; serving the unlocalized Artist", "domain", "artist", "operation", "resolve_artist_localization", failurecause.Attr(err), "artist_id", artistID)
	}
	localization, err = store.ResolveOGConsistency(ctx, artistID, localization, s.media.IsReadyAsset)
	if err != nil {
		return Detail{}, err
	}
	content, err := store.Document(ctx, artistID, localization.DisplayedLocale)
	if err != nil {
		return Detail{}, err
	}
	detail := Detail{
		Artist: current, Name: content.SourceTitle, Document: content.Document, Revision: content.Revision,
		IsGroup: isArtistGroup(ctx, store, artistID), Localization: localization,
	}
	if localization.Title != nil {
		detail.Name = *localization.Title
	}
	sourceOgAssetIDs, err := store.SourceOgAssetIDs(ctx, []string{artistID})
	if err != nil {
		return Detail{}, err
	}
	artistSourceOgAssetID := sourceOgAssetIDs[artistID]
	if localization.OmitSourceOgFallback {
		artistSourceOgAssetID = nil
	}
	if detail.OgImage, err = s.media.ResolveArtistOGAsset(ctx, artistSourceOgAssetID, localization.OgAssetID); err != nil {
		return Detail{}, err
	}
	images, err := s.media.LoadArtistImages(ctx, []string{artistID})
	if err != nil {
		return Detail{}, err
	}
	detail.Images = images[artistID]
	detail.ReleaseCount = artistReleaseCount(ctx, store, artistID)
	detail.WorkCount = artistWorkCount(ctx, store, artistID)
	if detail.Labels, err = s.artistLabels(ctx, store, artistID); err != nil {
		return Detail{}, err
	}
	return detail, nil
}

func isArtistGroup(ctx context.Context, store Store, artistID string) bool {
	group, err := store.IsGroup(ctx, artistID)
	if err != nil {
		slog.ErrorContext(ctx, "Artist group lookup failed; treating the Artist as solo", "domain", "artist", "operation", "check_artist_group", failurecause.Attr(err), "artist_id", artistID)
		return false
	}
	return group
}

// artistReleaseCount gets the count of published releases for an artist
func artistReleaseCount(ctx context.Context, store Store, artistID string) int32 {
	count, err := store.PublishedReleaseCount(ctx, artistID)
	if err != nil {
		slog.ErrorContext(ctx, "Artist release count query failed; reporting zero", "domain", "artist", "operation", "count_artist_releases", failurecause.Attr(err), "artist_id", artistID)
	}
	return int32(count)
}

// artistWorkCount gets the count of publicly visible works for an artist.
func artistWorkCount(ctx context.Context, store Store, artistID string) int32 {
	count, err := store.VisibleWorkCount(ctx, artistID)
	if err != nil {
		slog.ErrorContext(ctx, "Artist work count query failed; reporting zero", "domain", "artist", "operation", "count_artist_works", failurecause.Attr(err), "artist_id", artistID)
	}
	return int32(count)
}

// artistLabels gets published labels associated with an artist
func (s *Service) artistLabels(ctx context.Context, store Store, artistID string) ([]Label, error) {
	rows, err := store.PublishedLabels(ctx, artistID)
	if err != nil {
		return nil, err
	}
	fileIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.ImageFileID != nil {
			fileIDs = append(fileIDs, *row.ImageFileID)
		}
	}
	imageAssets, err := s.media.LoadReadySourceFileAssets(ctx, "logo", fileIDs)
	if err != nil {
		return nil, err
	}
	labels := make([]Label, 0, len(rows))
	for _, row := range rows {
		label := Label{ID: row.ID, Name: row.Name, Slug: row.Slug}
		if row.ImageFileID != nil {
			label.Image = imageAssets[*row.ImageFileID]
		}
		labels = append(labels, label)
	}
	return labels, nil
}

func collectArtistIDs(artists []artistdomain.Artist) []string {
	ids := make([]string, 0, len(artists))
	for _, value := range artists {
		ids = append(ids, value.ID)
	}
	return ids
}

func isValidUUID(value string) bool { _, err := uuid.Parse(value); return err == nil }
