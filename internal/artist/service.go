package artist

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Deps are the collaborators of the Artist use cases; all are required.
type Deps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	Audit         domainaudit.Appender
	Publisher     contentsignal.Publisher
	ContentBlocks *contentblock.Store
	Checkpoints   persistencecheckpoint.ContributorFence
	Translation   Translation
	Members       MemberProjection
	Runtime       Runtime
	Labels        Labels
	Releases      ReleaseRelations
	ProgramEvents ProgramEventRelations
	Works         WorkRelations
	ShareLinks    ShareLinks
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Audit": d.Audit == nil, "Publisher": d.Publisher == nil,
		"ContentBlocks": d.ContentBlocks == nil, "Checkpoints": d.Checkpoints == nil,
		"Translation": d.Translation == nil, "Members": d.Members == nil, "Runtime": d.Runtime == nil,
		"Labels": d.Labels == nil, "Releases": d.Releases == nil, "ProgramEvents": d.ProgramEvents == nil,
		"Works": d.Works == nil, "ShareLinks": d.ShareLinks == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("artist dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Artist use cases of the manage API, the collaboration
// service, AI documents and translation delivery. Connect, MCP and AI
// document adapters translate their requests into its commands and queries.
type Service struct {
	db            *gorm.DB
	spiceDB       *auth.SpiceDBClient
	publisher     contentsignal.Publisher
	contentBlocks *contentblock.Store
	checkpoints   persistencecheckpoint.ContributorFence
	translation   Translation
	members       MemberProjection
	runtime       Runtime
	labels        Labels
	releases      ReleaseRelations
	programEvents ProgramEventRelations
	works         WorkRelations
	shareLinks    ShareLinks
	mutations     *mutation.Runner
	participants  entityparticipant.Mutations
}

// NewService validates deps and returns the Artist use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, publisher: deps.Publisher, contentBlocks: deps.ContentBlocks,
		checkpoints: deps.Checkpoints, translation: deps.Translation, members: deps.Members, runtime: deps.Runtime,
		labels: deps.Labels, releases: deps.Releases, programEvents: deps.ProgramEvents, works: deps.Works,
		shareLinks: deps.ShareLinks, mutations: mutations, participants: artistEntity.Mutations(mutations, deps.SpiceDB),
	}, nil
}

// GetArtistEditorDataQuery reads one Artist for its editor.
type GetArtistEditorDataQuery struct {
	ID string
}

// EditorData is an Artist with its non-draft Labels, image revision and the
// actions the caller may take on it.
type EditorData struct {
	Artist         *managev1.Artist
	LabelIDs       []string
	AllowedActions []managev1.ArtistAction
	ImageRevision  string
}

// GetArtistEditorData returns an Artist the caller may edit. A caller
// without Edit learns nothing about whether the Artist exists.
func (s *Service) GetArtistEditorData(ctx context.Context, query GetArtistEditorDataQuery) (EditorData, error) {
	var artist model.Artist
	if err := s.db.WithContext(ctx).
		Where("id = ?", query.ID).
		First(&artist).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return EditorData{}, domainerr.ResourceNotFound("artist", query.ID)
		}
		return EditorData{}, err
	}
	actions, err := artistAllowedActions(ctx, s.spiceDB, artist.ID, artist.Status)
	if err != nil {
		return EditorData{}, err
	}
	if auth.GetUser(ctx) == nil {
		return EditorData{}, errAuthenticationRequired
	}
	if !hasArtistAction(actions, managev1.ArtistAction_ARTIST_ACTION_EDIT) {
		return EditorData{}, domainerr.ResourceNotFound("artist", artist.ID)
	}
	labelIDs, err := s.getArtistLabelIDs(ctx, artist.ID)
	if err != nil {
		return EditorData{}, err
	}
	document, err := s.overlayArtistSourceLocaleDocument(ctx, &artist)
	if err != nil {
		return EditorData{}, err
	}
	images, err := loadArtistImages(ctx, s.runtime, s.db, artist.ID)
	if err != nil {
		return EditorData{}, err
	}
	ogAsset, err := readyManageOgAssetRef(ctx, s.runtime, s.db, document.OgAssetID)
	if err != nil {
		return EditorData{}, err
	}
	protoArtist := toProtoArtist(&artist, document, ogAsset)
	applyManageArtistImages(protoArtist, images)
	return EditorData{Artist: protoArtist, LabelIDs: labelIDs, AllowedActions: actions, ImageRevision: artistImageRevision(images)}, nil
}

// ListArtistsQuery pages through Artists. A label_id filter keeps the
// Artists linked to that Label; a zero Limit is the default page.
type ListArtistsQuery struct {
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// ArtistPage is one page of Artists.
type ArtistPage struct {
	Artists    []*managev1.Artist
	Pagination *commonv1.PaginationResponse
}

// ListArtists returns one page of published Artists.
func (s *Service) ListArtists(ctx context.Context, query ListArtistsQuery) (ArtistPage, error) {
	artists, err := s.listArtists(ctx, s.db.WithContext(ctx).
		Model(&model.Artist{}).
		Where("status = ?", model.ArtistStatusPublished), query, false)
	if err != nil {
		return ArtistPage{}, err
	}
	return ArtistPage{Artists: artists.project(), Pagination: artists.pagination}, nil
}

// AdminArtistPage is one page of Artists with their Release counts.
type AdminArtistPage struct {
	Artists    []*managev1.ArtistWithStats
	Pagination *commonv1.PaginationResponse
}

// ListArtistsAdmin returns one page of every Artist to a caller with the
// platform-wide list action.
func (s *Service) ListArtistsAdmin(ctx context.Context, query ListArtistsQuery) (AdminArtistPage, error) {
	if err := artistEntity.RequireList(ctx, s.spiceDB); err != nil {
		return AdminArtistPage{}, err
	}
	artists, err := s.listArtists(ctx, s.db.WithContext(ctx).Model(&model.Artist{}), query, true)
	if err != nil {
		return AdminArtistPage{}, err
	}
	artistIDs := make([]string, len(artists.rows))
	for i, artist := range artists.rows {
		artistIDs[i] = artist.ID
	}
	releaseCounts, err := s.releases.CountReleasesByArtist(ctx, s.db, artistIDs)
	if err != nil {
		return AdminArtistPage{}, err
	}
	projected := artists.project()
	withStats := make([]*managev1.ArtistWithStats, len(projected))
	for i, artist := range projected {
		withStats[i] = &managev1.ArtistWithStats{Artist: artist, ReleaseCount: releaseCounts[artist.Id]}
	}
	return AdminArtistPage{Artists: withStats, Pagination: artists.pagination}, nil
}

// artistListing is one page of Artist rows with what their projection
// needs.
type artistListing struct {
	rows       []model.Artist
	documents  []artistDocument
	ogAssets   map[string]*commonv1.AssetRef
	images     map[string][]ArtistImageProjection
	pagination *commonv1.PaginationResponse
}

func (l artistListing) project() []*managev1.Artist {
	projected := make([]*managev1.Artist, len(l.rows))
	for i := range l.rows {
		projected[i] = toProtoArtist(&l.rows[i], l.documents[i], manageOgAssetFromReadyMap(l.ogAssets, l.documents[i].OgAssetID))
		applyManageArtistImages(projected[i], l.images[l.rows[i].ID])
	}
	return projected
}

// listArtists filters, counts, sorts and pages base, then loads each
// Artist's source-locale document, OG image and images. byID breaks sort
// ties by ID.
func (s *Service) listArtists(ctx context.Context, base *gorm.DB, query ListArtistsQuery, byID bool) (artistListing, error) {
	var remainingFilters []*commonv1.FilterSpec
	for _, f := range query.Filters {
		if f == nil {
			continue
		}
		if f.GetField() == "label_id" && f.GetValue() != "" {
			base = base.Where("id IN (SELECT artist_id FROM artist_label WHERE label_id = ?)", f.GetValue())
		} else {
			remainingFilters = append(remainingFilters, f)
		}
	}
	filtered, err := ArtistFilterConfig.ApplyFilters(base, remainingFilters)
	if err != nil {
		return artistListing{}, err
	}
	var total int64
	if err := filtered.Count(&total).Error; err != nil {
		return artistListing{}, err
	}
	limit := int32(20)
	if query.Limit > 0 {
		limit = query.Limit
	}
	sorted, err := artistSortConfig.ApplySort(filtered, query.Sorts)
	if err != nil {
		return artistListing{}, err
	}
	if byID {
		sorted = sorted.Order("artist.id ASC")
	}
	listing := artistListing{pagination: &commonv1.PaginationResponse{
		Total: int32(total), Limit: limit, Offset: query.Offset, HasMore: query.Offset+limit < int32(total),
	}}
	if err := sorted.Limit(int(limit)).Offset(int(query.Offset)).Find(&listing.rows).Error; err != nil {
		return artistListing{}, err
	}
	return listing, s.loadArtistListing(ctx, &listing)
}

func (s *Service) loadArtistListing(ctx context.Context, listing *artistListing) error {
	var err error
	if listing.documents, err = s.overlayArtistSourceLocaleDocuments(ctx, listing.rows); err != nil {
		return err
	}
	if listing.ogAssets, err = s.loadReadyArtistOgAssets(ctx, listing.documents); err != nil {
		return err
	}
	listing.images, err = s.runtime.LoadArtistImages(ctx, s.db, collectManageArtistIDs(listing.rows))
	return err
}
