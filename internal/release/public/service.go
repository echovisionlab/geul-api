package public

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/publiccontent"

	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/release"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Reads is the read-only unit of work of the public Release reads;
// mutation.UnitOfWork[Store] satisfies it.
type Reads interface {
	Read(ctx context.Context, read func(Store) error) error
	ReadLocked(ctx context.Context, read func(Store) error) error
}

// Deps are the collaborators of the public Release reads; all are
// required.
type Deps struct {
	Units     Reads // releasepublicpostgres.NewUnits
	SpiceDB   authz.AuthorizationDecisionChecker
	Signer    DownloadSigner
	Downloads DownloadAccessResolver
	Members   release.MemberSummaryLoader
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Signer": d.Signer == nil,
		"Downloads": d.Downloads == nil, "Members": d.Members == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public release dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the public Release reads.
type Service struct {
	units     Reads
	spiceDB   authz.AuthorizationDecisionChecker
	signer    DownloadSigner
	downloads DownloadAccessResolver
	members   release.MemberSummaryLoader
}

// NewService validates deps and returns the public Release reads.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{
		units: deps.Units, spiceDB: deps.SpiceDB, signer: deps.Signer, downloads: deps.Downloads, members: deps.Members,
	}, nil
}

// GetReleaseQuery names a Release by slug or ID. A draft is visible to a
// caller that can view it or that holds a share link to it.
type GetReleaseQuery struct {
	SlugOrID       string
	ShareToken     string
	SharePassword  string
	AcceptLanguage string
}

// ListReleasesQuery pages through published Releases. A Limit of zero takes
// the default page of 20.
type ListReleasesQuery struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Limit   int32
	Offset  int32
}

// ReleasePage is one page of published Release summaries.
type ReleasePage struct {
	Releases []Summary
	Total    int64
	Limit    int32
	Offset   int32
}

// Summary is a Release in a list, without its tracks. Its artwork is its
// Open Graph image as well.
type Summary struct {
	Release release.Release
	Title   string
	Artwork *mediaasset.Reference
	Artists []Artist
}

// View is a Release as the public reads it in the displayed locale.
type View struct {
	Release      release.Release
	Title        string
	Document     *contentv1.LocalizedRichTextDocument
	Revision     string
	Localization publiccontent.Selection
	Artwork      *mediaasset.Reference
	Artists      []Artist
	Labels       []Label
	Genres       []release.Reference
	Styles       []release.Reference
	Formats      []release.FormatReference
	Credits      []Credit
	Tracks       []Track
}

// Artist is an Artist of a Release or of a Track credit; Role is "primary"
// or "credit".
type Artist struct {
	ID    string
	Name  string
	Slug  *string
	Role  string
	Image *mediaasset.Reference
}

// Label is a Label of a Release.
type Label struct {
	ID            string
	Name          string
	Slug          *string
	CatalogNumber *string
}

// Credit is a credit of a Release with its display name, note and image.
type Credit struct {
	ID         string
	Name       string
	Slug       *string
	CreditRole *string
	ArtistID   *string
	MemberID   *string
	Member     *memberref.Summary
	Note       *string
	Image      *mediaasset.Reference
}

// Track is a Track of a Release with its credits, media and download
// access.
type Track struct {
	ID              string
	Title           string
	TrackNumber     int
	DurationSeconds *int
	Credits         []TrackCredit
	Media           TrackMedia
	Download        DownloadAccess
}

// TrackMedia is the delivery of a Track's original audio.
type TrackMedia struct {
	Source      MediaFile
	Playback    *fileref.HLSReference
	Download    *mediaauth.Reference
	Waveform    *mediaasset.Reference
	Spectrogram *mediaasset.Reference
}

// TrackCredit is a credit of a Track; Artist is set for an Artist credit.
type TrackCredit struct {
	ID         string
	Name       *string
	CreditRole *string
	Artist     *Artist
}

// GetRelease retrieves a release by slug or ID
// - UUID-first: if input is valid UUID, query by ID; otherwise query by slug
// - share_token allows access to draft releases
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *Service) GetRelease(ctx context.Context, query GetReleaseQuery) (View, error) {
	var found release.Release
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		found, err = store.FindRelease(ctx, query.SlugOrID, release.IsValidUUID(query.SlugOrID))
		return err
	})
	if errors.Is(err, release.ErrReleaseNotFound) {
		return View{}, releaseNotFound()
	}
	if err != nil {
		return View{}, err
	}
	mediaAuthorization, err := s.authorizeReleaseMedia(ctx, found, query)
	if err != nil {
		return View{}, err
	}
	return s.buildRelease(ctx, query.AcceptLanguage, found, mediaAuthorization)
}

// authorizeReleaseMedia decides how the caller reaches the Release and its
// media: a published Release is public, and a draft needs the view
// permission or a share link.
func (s *Service) authorizeReleaseMedia(
	ctx context.Context,
	found release.Release,
	query GetReleaseQuery,
) (mediaasset.ContentDownloadOwnerAuthorization, error) {
	authorization := mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "release",
		ResourceID:   found.ID,
		Status:       string(found.Status),
		Mode:         mediaasset.ContentDownloadOwnerAccessPublic,
	}
	if found.ContentDocumentID != nil {
		authorization.DocumentID = *found.ContentDocumentID
	}
	if found.Status == release.StatusPublished {
		return authorization, nil
	}
	allowed, err := s.hasDraftReleaseView(ctx, found.ID)
	if err != nil {
		return authorization, fmt.Errorf("check release draft view permission: %w", err)
	}
	if allowed {
		authorization.Mode = mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft
		if user := auth.GetUser(ctx); user != nil {
			authorization.IdentityID = user.IdentityID.String()
			authorization.MemberID = user.MemberID.String()
		}
		return authorization, nil
	}
	link, err := s.requireDraftShareLinkAccess(ctx, query.ShareToken, query.SharePassword, found.ID, "release")
	if err != nil {
		return authorization, err
	}
	authorization.Mode = mediaasset.ContentDownloadOwnerAccessShare
	authorization.ShareLink = &mediaasset.ContentDownloadShareLinkWitness{
		ID: link.ID, EntityType: link.EntityType, EntityID: link.EntityID, ExpiresAt: link.ExpiresAt,
	}
	return authorization, nil
}

func (s *Service) hasDraftReleaseView(ctx context.Context, releaseID string) (bool, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.Banned {
		return false, nil
	}
	can, err := policyv1.Release.View(releaseID)
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, err
	}
	return s.spiceDB.Can(ctx, decision)
}

func (s *Service) requireDraftShareLinkAccess(
	ctx context.Context,
	shareToken string,
	sharePassword string,
	entityID string,
	entityName string,
) (ShareLink, error) {
	notFound := domainerr.New(domainerr.NotFound, "", entityName+" not found")
	if shareToken == "" {
		return ShareLink{}, notFound
	}
	link, err := s.validateShareLinkForEntity(ctx, shareToken, sharePassword, entityID)
	if errors.Is(err, ErrShareLinkNotFound) {
		return ShareLink{}, notFound
	}
	if err != nil {
		return ShareLink{}, fmt.Errorf("validate %s share link: %w", entityName, err)
	}
	return link, nil
}

func (s *Service) validateShareLinkForEntity(ctx context.Context, token, password, entityID string) (ShareLink, error) {
	var link ShareLink
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		link, err = store.FindShareLink(ctx, token, entityID, time.Now())
		return err
	}); err != nil {
		return ShareLink{}, err
	}
	if link.PasswordHash == nil {
		return link, nil
	}
	if password == "" {
		return ShareLink{}, ErrShareLinkNotFound
	}
	match, err := crypto.NewPasswordHasher(nil).Verify(password, *link.PasswordHash)
	if err != nil {
		return ShareLink{}, fmt.Errorf("verify share link password: %w", err)
	}
	if !match {
		return ShareLink{}, ErrShareLinkNotFound
	}
	return link, nil
}

// releaseRelationFilters are the list filters on a Release relation, which
// FilterConfig cannot express.
var releaseRelationFilters = map[string]Relation{
	"label_id":    RelationLabel,
	"artist_id":   RelationArtist,
	"category_id": RelationCategory,
}

// ListReleases returns a page of published releases.
func (s *Service) ListReleases(ctx context.Context, query ListReleasesQuery) (ReleasePage, error) {
	pagination := queryutil.GetPaginationParams(query.Limit, query.Offset, 20)
	page := ListPage{Sorts: query.Sorts, Limit: pagination.Limit, Offset: pagination.Offset}
	for _, filter := range query.Filters {
		relation, ok := releaseRelationFilters[filter.GetField()]
		if !ok {
			page.Filters = append(page.Filters, filter)
			continue
		}
		switch filter.GetOp() {
		case commonv1.FilterOp_FILTER_OP_EQ:
			page.RelationFilters = append(page.RelationFilters, RelationFilter{Relation: relation, IDs: []string{filter.GetValue()}, One: true})
		case commonv1.FilterOp_FILTER_OP_IN:
			page.RelationFilters = append(page.RelationFilters, RelationFilter{Relation: relation, IDs: filter.GetValues()})
		default:
			return ReleasePage{}, domainerr.New(domainerr.Invalid, filter.GetField(), fmt.Sprintf(
				"invalid filter operation '%s' for field '%s'", filter.GetOp().String(), filter.GetField(),
			))
		}
	}
	result := ReleasePage{Limit: pagination.Limit, Offset: pagination.Offset}
	var releases []release.Release
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		releases, result.Total, err = store.ListPublishedReleases(ctx, page)
		return err
	}); err != nil {
		return ReleasePage{}, err
	}
	var err error
	result.Releases, err = s.releaseSummaries(ctx, releases)
	return result, err
}

func (s *Service) releaseSummaries(ctx context.Context, releases []release.Release) ([]Summary, error) {
	releaseIDs := collectReleaseIDs(releases)
	var summaries []Summary
	err := s.units.Read(ctx, func(store Store) error {
		sourceTitles, err := store.SourceTitles(ctx, releaseIDs)
		if err != nil {
			return err
		}
		for _, id := range releaseIDs {
			if _, ok := sourceTitles[id]; !ok {
				return domainerr.ResourceNotFound("release_translation", id)
			}
		}
		artworkAssets, err := loadReleaseArtworkAssets(ctx, store, releaseIDs)
		if err != nil {
			return err
		}
		artistsByRelease, err := loadReleaseArtists(ctx, store, releaseIDs)
		if err != nil {
			return err
		}
		summaries = make([]Summary, 0, len(releases))
		for _, found := range releases {
			summaries = append(summaries, Summary{
				Release: found, Title: sourceTitles[found.ID], Artwork: artworkAssets[found.ID], Artists: artistsByRelease[found.ID],
			})
		}
		return nil
	})
	return summaries, err
}

func releaseNotFound() *domainerr.Error {
	return domainerr.New(domainerr.NotFound, "", "release not found")
}

// buildRelease projects the Release in one snapshot. A Release that was
// public when it was found must still be published.
func (s *Service) buildRelease(
	ctx context.Context,
	acceptLanguage string,
	found release.Release,
	mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization,
) (View, error) {
	wasPublic := found.Status == release.StatusPublished
	var projected View
	err := s.units.ReadLocked(ctx, func(store Store) error {
		var err error
		projected, err = s.buildReleaseInSnapshot(ctx, store, acceptLanguage, found.ID, mediaAuthorization, wasPublic)
		return err
	})
	return projected, err
}

func (s *Service) buildReleaseInSnapshot(
	ctx context.Context,
	store Store,
	acceptLanguage string,
	releaseID string,
	mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization,
	wasPublic bool,
) (View, error) {
	current, err := store.FindRelease(ctx, releaseID, true)
	if errors.Is(err, release.ErrReleaseNotFound) {
		return View{}, releaseNotFound()
	}
	if err != nil {
		return View{}, err
	}
	if wasPublic && current.Status != release.StatusPublished {
		return View{}, releaseNotFound()
	}
	localization, err := store.ResolveLocalization(ctx, releaseID, acceptLanguage)
	if err != nil {
		slog.ErrorContext(ctx, "failed to resolve release localization", "releaseId", releaseID, failurecause.Attr(err))
	}
	content, err := store.ContentDocument(ctx, releaseID, localization.DisplayedLocale)
	if err != nil {
		return View{}, err
	}
	view := View{
		Release: current, Title: content.SourceTitle, Document: content.Document, Revision: content.Revision,
		Localization: localization,
	}
	if localization.Title != nil {
		view.Title = *localization.Title
	}
	if err := s.projectReleaseRelations(ctx, store, &view, mediaAuthorization); err != nil {
		return View{}, err
	}
	return view, nil
}

// projectReleaseRelations adds the artwork, relations, credits and tracks
// of the Release to its view.
func (s *Service) projectReleaseRelations(
	ctx context.Context,
	store Store,
	view *View,
	mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization,
) error {
	releaseID := view.Release.ID
	artworkAssets, err := loadReleaseArtworkAssets(ctx, store, []string{releaseID})
	if err != nil {
		return err
	}
	view.Artwork = artworkAssets[releaseID]
	artistsByRelease, err := loadReleaseArtists(ctx, store, []string{releaseID})
	if err != nil {
		return err
	}
	view.Artists = artistsByRelease[releaseID]
	if view.Labels, err = loadReleaseLabels(ctx, store, releaseID); err != nil {
		return err
	}
	if view.Genres, err = store.Genres(ctx, releaseID); err != nil {
		return err
	}
	if view.Styles, err = store.Styles(ctx, releaseID); err != nil {
		return err
	}
	if view.Formats, err = store.Formats(ctx, releaseID); err != nil {
		return err
	}
	view.Credits, err = s.loadReleaseCredits(ctx, store, releaseID, view.Localization.SourceLocale, view.Localization.DisplayedLocale)
	if err != nil {
		return err
	}
	view.Tracks, err = s.loadReleaseTracks(ctx, store, releaseID, string(view.Release.Status), mediaAuthorization)
	return err
}

func collectReleaseIDs(releases []release.Release) []string {
	ids := make([]string, 0, len(releases))
	for _, found := range releases {
		ids = append(ids, found.ID)
	}
	return ids
}

func loadReleaseArtworkAssets(ctx context.Context, store Store, releaseIDs []string) (map[string]*mediaasset.Reference, error) {
	result := make(map[string]*mediaasset.Reference, len(releaseIDs))
	if len(releaseIDs) == 0 {
		return result, nil
	}
	rows, err := store.ArtworkFiles(ctx, releaseIDs)
	if err != nil {
		return nil, err
	}
	fileIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		fileIDs = append(fileIDs, row.FileID)
	}
	assetsByFile, err := store.ReadySourceAssets(ctx, fileIDs, "artwork", "image")
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		asset, ok := assetsByFile[row.FileID]
		if !ok {
			continue
		}
		result[row.ReleaseID] = asset
	}
	return result, nil
}
