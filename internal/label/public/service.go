package public

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/label"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/publiccontent"

	queryutil "github.com/echovisionlab/geul-api/internal/query"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

// ContentDocument is a Label's content document in the displayed locale,
// with its source title.
type ContentDocument struct {
	Document    *contentv1.LocalizedRichTextDocument
	Revision    string
	SourceTitle string
}

// DraftAccess admits a draft Label read through a share token (and its
// password) or the caller's exact view permission.
type DraftAccess interface {
	Require(context.Context, string, string, string) error
}

// Deps are the collaborators of the public Label reads; all are required.
type Deps struct {
	Units mutation.UnitOfWork[Store] // labelpublicpostgres.NewUnits
	Draft DraftAccess
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{"Units": d.Units == nil, "Draft": d.Draft == nil} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public label dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the public Label reads.
type Service struct {
	units mutation.UnitOfWork[Store]
	draft DraftAccess
}

// NewService validates deps and returns the public Label reads.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{units: deps.Units, draft: deps.Draft}, nil
}

var errLabelNotFound = domainerr.New(domainerr.NotFound, "", "label not found")

// ListLabelsQuery pages through published Labels.
type ListLabelsQuery struct {
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

// Summary is a published Label as a list or a parent shows it.
type Summary struct {
	ID          string
	Name        string
	Slug        *string
	CountryCode *string
	Website     *string
	PublishedAt *time.Time
	OgImage     *mediaasset.Reference
	LightLogo   *mediaasset.Reference
	DarkLogo    *mediaasset.Reference
}

// LabelPage is one page of published Label summaries.
type LabelPage struct {
	Labels     []Summary
	Pagination Pagination
}

// List returns published labels with filtering and sorting
func (s *Service) List(ctx context.Context, query ListLabelsQuery) (LabelPage, error) {
	pagination := queryutil.GetPaginationParams(query.Limit, query.Offset, 20)
	var rows []label.Label
	var total int64
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		rows, total, err = store.ListPublishedLabels(ctx, label.ListPage{
			PublishedOnly: true, Filters: query.Filters, Sorts: query.Sorts, Limit: pagination.Limit, Offset: pagination.Offset,
		})
		return err
	}); err != nil {
		return LabelPage{}, err
	}
	summaries, err := s.labelSummaries(ctx, rows)
	if err != nil {
		return LabelPage{}, err
	}
	return LabelPage{Labels: summaries, Pagination: Pagination{
		Total:   int32(total),
		Limit:   pagination.Limit,
		Offset:  pagination.Offset,
		HasMore: pagination.Offset+int32(len(rows)) < int32(total),
	}}, nil
}

// labelSummaries renders labels with their source titles, ready logos and
// OG images.
func (s *Service) labelSummaries(ctx context.Context, labels []label.Label) ([]Summary, error) {
	logoFileIDs := make([]string, 0, len(labels)*2)
	ogAssetIDs := make([]string, 0, len(labels))
	for _, value := range labels {
		for _, candidate := range []*string{value.LogoLightFileID, value.LogoDarkFileID} {
			if candidate != nil {
				logoFileIDs = append(logoFileIDs, *candidate)
			}
		}
		if labelHasEffectiveLogo(value.LogoLightFileID, value.LogoDarkFileID) && value.OgAssetID != nil {
			ogAssetIDs = append(ogAssetIDs, *value.OgAssetID)
		}
	}
	var sourceTitles map[string]string
	var logoRows, ogRows map[string]*mediaasset.Reference
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		if sourceTitles, err = store.SourceTitles(ctx, collectLabelIDs(labels)); err != nil {
			return err
		}
		if logoRows, err = store.ReadySourceFiles(ctx, logoFileIDs, "logo"); err != nil {
			return err
		}
		ogRows, err = store.ReadyAssets(ctx, ogAssetIDs)
		return err
	})
	if err != nil {
		return nil, err
	}
	summaries := make([]Summary, 0, len(labels))
	for _, value := range labels {
		summaries = append(summaries, labelSummary(value, sourceTitles[value.ID], logoRows, ogRows))
	}
	return summaries, nil
}

func labelSummary(value label.Label, name string, logoRows, ogRows map[string]*mediaasset.Reference) Summary {
	summary := Summary{
		ID: value.ID, Name: name, Slug: value.Slug, CountryCode: value.CountryCode, Website: value.Website,
		PublishedAt: value.PublishedAt,
	}
	if labelHasEffectiveLogo(value.LogoLightFileID, value.LogoDarkFileID) && value.OgAssetID != nil {
		summary.OgImage = ogRows[*value.OgAssetID]
	}
	if value.LogoLightFileID != nil {
		summary.LightLogo = logoRows[*value.LogoLightFileID]
	}
	if value.LogoDarkFileID != nil {
		summary.DarkLogo = logoRows[*value.LogoDarkFileID]
	}
	return summary
}

// GetLabelQuery reads one Label by ID or slug. A draft Label needs a share
// token (and its password) or the caller's exact view permission.
// AcceptLanguage picks the displayed locale.
type GetLabelQuery struct {
	SlugOrID       string
	ShareToken     string
	SharePassword  string
	AcceptLanguage string
}

// Detail is one Label as the public API renders it.
type Detail struct {
	Label        label.Label
	Name         string
	Document     *contentv1.LocalizedRichTextDocument
	Revision     string
	OgImage      *mediaasset.Reference
	LightLogo    *mediaasset.Reference
	DarkLogo     *mediaasset.Reference
	ReleaseCount int32
	ArtistCount  int32
	Parent       *Summary
	Artists      []Artist
	Releases     []Release
	Localization publiccontent.Selection
}

// Artist is a published Artist of a Label.
type Artist struct {
	ID    string
	Name  string
	Slug  *string
	Image *mediaasset.Reference
}

// Release is a published Release of a Label with its Artists.
type Release struct {
	ID          string
	Title       string
	Slug        *string
	Type        string
	ReleaseDate *time.Time
	PublishedAt *time.Time
	Artwork     *mediaasset.Reference
	Artists     []ReleaseArtist
}

// ReleaseArtist is an Artist credited on a Release.
type ReleaseArtist struct {
	ID   string
	Name string
	Slug *string
}

// Get retrieves a label by slug or ID
// - UUID-first: if input is valid UUID, query by ID; otherwise query by slug
// - share_token allows access to draft labels
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *Service) Get(ctx context.Context, query GetLabelQuery) (Detail, error) {
	var value label.Label
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		value, err = store.FindLabel(ctx, query.SlugOrID, isValidUUID(query.SlugOrID))
		return err
	})
	if err != nil {
		if errors.Is(err, label.ErrLabelNotFound) {
			return Detail{}, errLabelNotFound
		}
		return Detail{}, err
	}
	if value.Status != label.StatusPublished {
		if err := s.draft.Require(ctx, value.ID, query.ShareToken, query.SharePassword); err != nil {
			return Detail{}, err
		}
	}
	return s.buildLabelResponse(ctx, query.AcceptLanguage, value)
}

// buildLabelResponse projects the Label in one snapshot that waits out a
// concurrent writer of its row.
func (s *Service) buildLabelResponse(ctx context.Context, acceptLanguage string, value label.Label) (Detail, error) {
	wasPublic := value.Status == label.StatusPublished
	var detail Detail
	err := s.units.ReadLocked(ctx, func(store Store) error {
		var err error
		detail, err = buildLabelResponseInTransaction(ctx, store, acceptLanguage, value.ID, wasPublic)
		return err
	})
	return detail, err
}

func buildLabelResponseInTransaction(ctx context.Context, store Store, acceptLanguage, labelID string, wasPublic bool) (Detail, error) {
	current, err := store.ShareLockLabel(ctx, labelID)
	if err != nil {
		if errors.Is(err, label.ErrLabelNotFound) {
			return Detail{}, errLabelNotFound
		}
		return Detail{}, err
	}
	if wasPublic && current.Status != label.StatusPublished {
		return Detail{}, errLabelNotFound
	}
	localization, err := store.ResolveLocalization(ctx, labelID, acceptLanguage)
	if err != nil {
		slog.ErrorContext(ctx, "Label localization lookup failed; serving the unlocalized Label", "domain", "label", "operation", "resolve_label_localization", failurecause.Attr(err), "label_id", labelID)
	}
	content, err := store.Document(ctx, labelID, localization.DisplayedLocale)
	if err != nil {
		return Detail{}, err
	}
	detail := Detail{
		Label: current, Name: localizedLabelName(content.SourceTitle, localization.Title),
		Document: content.Document, Revision: content.Revision, Localization: localization,
	}
	if labelHasEffectiveLogo(current.LogoLightFileID, current.LogoDarkFileID) {
		// Label OG is one locale-independent, logo-only derivative. Localized
		// description selection must never participate in image resolution.
		detail.OgImage, err = store.ResolveOg(ctx, current.OgAssetID)
		if err != nil {
			return Detail{}, err
		}
	}
	detail.LightLogo = optionalLabelImageAsset(ctx, store, current.LogoLightFileID)
	detail.DarkLogo = optionalLabelImageAsset(ctx, store, current.LogoDarkFileID)
	if err := applyLabelRelations(ctx, store, &detail, current.ParentLabelID); err != nil {
		return Detail{}, err
	}
	return detail, nil
}

// applyLabelRelations adds the relation counts, the published parent, and
// the first 12 published Artists and Releases.
func applyLabelRelations(ctx context.Context, store Store, detail *Detail, parentLabelID *string) error {
	var err error
	detail.ReleaseCount, detail.ArtistCount, err = store.RelationCounts(ctx, detail.Label.ID)
	if err != nil {
		return err
	}
	if parentLabelID != nil {
		detail.Parent = parentLabel(ctx, store, *parentLabelID)
	}
	if detail.Artists, err = labelArtistsPreview(ctx, store, detail.Label.ID); err != nil {
		return err
	}
	detail.Releases, err = labelReleasesPreview(ctx, store, detail.Label.ID)
	return err
}

// labelArtistsPreview gets first 12 published artists for a label
func labelArtistsPreview(ctx context.Context, store Store, labelID string) ([]Artist, error) {
	rows, err := store.PublishedArtists(ctx, labelID)
	if err != nil {
		return nil, err
	}
	imageFileIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.ImageFileID != nil {
			imageFileIDs = append(imageFileIDs, *row.ImageFileID)
		}
	}
	imageRows, err := store.ReadySourceFiles(ctx, imageFileIDs, "image")
	if err != nil {
		return nil, err
	}
	artists := make([]Artist, 0, len(rows))
	for _, row := range rows {
		artist := Artist{ID: row.ID, Name: row.Name, Slug: row.Slug}
		if row.ImageFileID != nil {
			artist.Image = imageRows[*row.ImageFileID]
		}
		artists = append(artists, artist)
	}
	return artists, nil
}

// labelReleasesPreview gets first 12 published releases for a label
func labelReleasesPreview(ctx context.Context, store Store, labelID string) ([]Release, error) {
	rows, err := store.PublishedReleases(ctx, labelID)
	if err != nil {
		return nil, err
	}
	releaseIDs := make([]string, 0, len(rows))
	artworkFileIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		releaseIDs = append(releaseIDs, row.ID)
		if row.ArtworkFileID != nil {
			artworkFileIDs = append(artworkFileIDs, *row.ArtworkFileID)
		}
	}
	artworkRows, err := store.ReadySourceFiles(ctx, artworkFileIDs, "artwork", "image")
	if err != nil {
		return nil, err
	}
	var artistRows []ReleaseArtistRow
	if len(releaseIDs) > 0 {
		if artistRows, err = store.ReleaseArtists(ctx, releaseIDs); err != nil {
			return nil, err
		}
	}
	artistsByRelease := make(map[string][]ReleaseArtist, len(releaseIDs))
	for _, row := range artistRows {
		artistsByRelease[row.ReleaseID] = append(artistsByRelease[row.ReleaseID], ReleaseArtist{ID: row.ArtistID, Name: row.ArtistName, Slug: row.ArtistSlug})
	}
	releases := make([]Release, 0, len(rows))
	for _, row := range rows {
		release := Release{
			ID: row.ID, Title: row.Title, Slug: row.Slug, Type: row.Type,
			ReleaseDate: row.ReleaseDate, PublishedAt: row.PublishedAt, Artists: artistsByRelease[row.ID],
		}
		if row.ArtworkFileID != nil {
			release.Artwork = artworkRows[*row.ArtworkFileID]
		}
		releases = append(releases, release)
	}
	return releases, nil
}

func labelHasEffectiveLogo(lightFileID, darkFileID *string) bool {
	return (lightFileID != nil && strings.TrimSpace(*lightFileID) != "") ||
		(darkFileID != nil && strings.TrimSpace(*darkFileID) != "")
}

func optionalLabelImageAsset(ctx context.Context, store Store, fileID *string) *mediaasset.Reference {
	if fileID == nil {
		return nil
	}
	assets, err := store.ReadySourceFiles(ctx, []string{*fileID}, "logo")
	if err != nil {
		slog.ErrorContext(ctx, "Label logo asset lookup failed; serving the Label without it", "domain", "label", "operation", "resolve_logo_asset", "outcome", "failed", failurecause.Attr(err), "file_id", *fileID)
		return nil
	}
	return assets[*fileID]
}

// parentLabel gets the published parent's summary; a failed lookup serves
// the Label without it.
func parentLabel(ctx context.Context, store Store, parentLabelID string) *Summary {
	parent, found, err := store.PublishedParent(ctx, parentLabelID)
	if err != nil || !found {
		if err != nil {
			slog.ErrorContext(ctx, "Parent Label lookup failed; serving the Label without its parent", "domain", "label", "operation", "load_parent_label", "outcome", "failed", failurecause.Attr(err), "label_id", parentLabelID)
		}
		return nil
	}
	summary := &Summary{ID: parent.ID, Name: parent.Name, Slug: parent.Slug}
	summary.LightLogo = optionalLabelImageAsset(ctx, store, parent.LogoLightFileID)
	summary.DarkLogo = optionalLabelImageAsset(ctx, store, parent.LogoDarkFileID)
	if labelHasEffectiveLogo(parent.LogoLightFileID, parent.LogoDarkFileID) {
		var err error
		summary.OgImage, err = store.ResolveOg(ctx, parent.OgAssetID)
		if err != nil {
			slog.ErrorContext(ctx, "Parent Label OG asset lookup failed; serving the parent without it", "domain", "label", "operation", "resolve_parent_og_asset", "outcome", "failed", failurecause.Attr(err), "label_id", parentLabelID)
		}
	}
	return summary
}

func collectLabelIDs(labels []label.Label) []string {
	ids := make([]string, 0, len(labels))
	for _, value := range labels {
		ids = append(ids, value.ID)
	}
	return ids
}

func localizedLabelName(sourceTitle string, localizedTitle *string) string {
	if localizedTitle != nil {
		return *localizedTitle
	}
	return sourceTitle
}

func isValidUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}
