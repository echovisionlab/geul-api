package artist

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Authorizer is the SpiceDB port of the Artist use cases: the fresh checks
// and relationship writes of the mutation pipeline, a participant's
// effective authority, the Artists the caller manages and the relationship
// snapshot a deletion removes. *auth.SpiceDBClient implements it.
type Authorizer interface {
	mutation.Authorizer
	entityparticipant.ActorChecker
	LookupResources(ctx context.Context, lookup policyv1.ResourceLookup, actor policyv1.Actor) ([]string, error)
	SnapshotResourceRelationshipDescriptors(ctx context.Context, plan policyv1.RelationshipSnapshotPlan) ([]auth.RelationshipSnapshot, auth.ZedToken, error)
}

// MemberProjection supplies the Member-owned summaries needed by Artist
// participant reads and mutation responses.
type MemberProjection = entityparticipant.Members

// Deps are the collaborators of the Artist use cases; all are required.
type Deps struct {
	Units   mutation.UnitOfWork[Store] // artistpostgres.NewUnits
	SpiceDB Authorizer
	Fence   identitystate.Fence
	Audit   domainaudit.Appender
	Members MemberProjection
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
		"Members": d.Members == nil,
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
	units        mutation.UnitOfWork[Store]
	spiceDB      Authorizer
	members      MemberProjection
	mutations    *mutation.StoreRunner[Store]
	participants entityparticipant.Mutations[Store]
}

// NewService validates deps and returns the Artist use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		units: deps.Units, spiceDB: deps.SpiceDB, members: deps.Members, mutations: mutations,
		participants: entityparticipant.NewMutations(artistEntity, mutations, deps.SpiceDB),
	}, nil
}

// Action is an Artist action the caller may take.
type Action uint8

const (
	ActionEdit Action = iota + 1
	ActionManageShareLinks
	ActionDelete
	ActionManageParticipants
	ActionPublish
	ActionUnpublish
	ActionRemoveOwner
)

// GetArtistEditorDataQuery reads one Artist for its editor.
type GetArtistEditorDataQuery struct {
	ID string
}

// EditorData is an Artist with its non-draft Labels, image revision and the
// actions the caller may take on it.
type EditorData struct {
	Artist         View
	LabelIDs       []string
	AllowedActions []Action
	ImageRevision  string
}

// GetArtistEditorData returns an Artist the caller may edit. A caller
// without Edit learns nothing about whether the Artist exists.
func (s *Service) GetArtistEditorData(ctx context.Context, query GetArtistEditorDataQuery) (EditorData, error) {
	var artist Artist
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		artist, err = store.FindArtist(ctx, query.ID)
		return err
	}); err != nil {
		if errors.Is(err, ErrArtistNotFound) {
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
	if !slices.Contains(actions, ActionEdit) {
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
	images, err := s.loadArtistImages(ctx, artist.ID)
	if err != nil {
		return EditorData{}, err
	}
	ogAsset, err := s.readyManageOgAssetRef(ctx, document.OgAssetID)
	if err != nil {
		return EditorData{}, err
	}
	view := artistView(artist, document, ogAsset, images)
	return EditorData{Artist: view, LabelIDs: labelIDs, AllowedActions: actions, ImageRevision: artistImageRevision(images)}, nil
}

// ListArtistsQuery pages through Artists. A label_id filter keeps the
// Artists linked to that Label; a zero Limit is the default page.
type ListArtistsQuery struct {
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// Pagination is where a page sits among every matching Artist.
type Pagination struct {
	Total   int32
	Limit   int32
	Offset  int32
	HasMore bool
}

// ArtistPage is one page of Artists.
type ArtistPage struct {
	Artists    []View
	Pagination Pagination
}

// ListArtists returns one page of published Artists.
func (s *Service) ListArtists(ctx context.Context, query ListArtistsQuery) (ArtistPage, error) {
	artists, err := s.listArtists(ctx, ListPage{PublishedOnly: true}, query)
	if err != nil {
		return ArtistPage{}, err
	}
	return ArtistPage{Artists: artists.project(), Pagination: artists.pagination}, nil
}

// AdminView is an Artist with its Release count.
type AdminView struct {
	Artist       View
	ReleaseCount int32
}

// AdminArtistPage is one page of Artists with their Release counts.
type AdminArtistPage struct {
	Artists    []AdminView
	Pagination Pagination
}

// ListArtistsAdmin returns one page of every Artist to a caller with the
// platform-wide list action.
func (s *Service) ListArtistsAdmin(ctx context.Context, query ListArtistsQuery) (AdminArtistPage, error) {
	if err := artistEntity.RequireList(ctx, s.spiceDB); err != nil {
		return AdminArtistPage{}, err
	}
	artists, err := s.listArtists(ctx, ListPage{TieByID: true}, query)
	if err != nil {
		return AdminArtistPage{}, err
	}
	artistIDs := make([]string, len(artists.rows))
	for i, artist := range artists.rows {
		artistIDs[i] = artist.ID
	}
	var releaseCounts map[string]int32
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		releaseCounts, err = store.CountReleasesByArtist(ctx, artistIDs)
		return err
	}); err != nil {
		return AdminArtistPage{}, err
	}
	projected := artists.project()
	withStats := make([]AdminView, len(projected))
	for i, artist := range projected {
		withStats[i] = AdminView{Artist: artist, ReleaseCount: releaseCounts[artist.Artist.ID]}
	}
	return AdminArtistPage{Artists: withStats, Pagination: artists.pagination}, nil
}

// artistListing is one page of Artist rows with what their projection
// needs.
type artistListing struct {
	rows       []Artist
	documents  []artistDocument
	ogAssets   map[string]*mediaasset.Reference
	images     map[string][]ArtistImageProjection
	pagination Pagination
}

func (l artistListing) project() []View {
	projected := make([]View, len(l.rows))
	for i := range l.rows {
		projected[i] = artistView(l.rows[i], l.documents[i], manageOgAssetFromReadyMap(l.ogAssets, l.documents[i].OgAssetID), l.images[l.rows[i].ID])
	}
	return projected
}

// listArtists lists one page of base, then loads each Artist's
// source-locale document, OG image and images.
func (s *Service) listArtists(ctx context.Context, base ListPage, query ListArtistsQuery) (artistListing, error) {
	limit := int32(20)
	if query.Limit > 0 {
		limit = query.Limit
	}
	base.Filters, base.Sorts, base.Limit, base.Offset = query.Filters, query.Sorts, limit, query.Offset
	var listing artistListing
	var total int64
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		listing.rows, total, err = store.ListArtists(ctx, base)
		return err
	}); err != nil {
		return artistListing{}, err
	}
	listing.pagination = Pagination{Total: int32(total), Limit: limit, Offset: query.Offset, HasMore: query.Offset+limit < int32(total)}
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
	return s.units.Read(ctx, func(store Store) error {
		var err error
		listing.images, err = store.Images(ctx, collectArtistIDs(listing.rows))
		return err
	})
}

func collectArtistIDs(artists []Artist) []string {
	ids := make([]string, 0, len(artists))
	for _, artist := range artists {
		ids = append(ids, artist.ID)
	}
	return ids
}
