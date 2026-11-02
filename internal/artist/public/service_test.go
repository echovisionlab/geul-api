package public_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/artist"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	optional "github.com/echovisionlab/geul-api/internal/optional"

	artistpublic "github.com/echovisionlab/geul-api/internal/artist/public"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/publiccontent"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/stretchr/testify/require"
)

// fakeStore serves the public reads from fixed rows; reads change nothing,
// so its savepoint is a no-op.
type fakeStore struct {
	artists   map[string]artist.Artist
	titles    map[string]string
	ogAssets  map[string]*string
	labels    []artistpublic.LabelRow
	releases  []artistpublic.ReleaseRow
	credits   []artistpublic.ReleaseArtistRow
	works     []artistpublic.WorkRow
	localized map[string]publiccontent.Selection
	sorts     []artistpublic.Sort
	shareHash *string
	shareOK   bool
	locked    func(artist.Artist) artist.Artist
}

func (s *fakeStore) Savepoint() func() { return func() {} }

func (s *fakeStore) ListPublishedArtists(context.Context, artistpublic.ListPage) ([]artist.Artist, int64, error) {
	var listed []artist.Artist
	for _, value := range s.artists {
		if value.Status == artist.StatusPublished {
			listed = append(listed, value)
		}
	}
	return listed, int64(len(listed)), nil
}

func (s *fakeStore) FindArtist(_ context.Context, slugOrID string, byID bool) (artist.Artist, error) {
	for _, value := range s.artists {
		if (byID && value.ID == slugOrID) || (!byID && value.Slug != nil && *value.Slug == slugOrID) {
			return value, nil
		}
	}
	return artist.Artist{}, artist.ErrArtistNotFound
}

func (s *fakeStore) ShareLockArtist(_ context.Context, artistID string) (artist.Artist, error) {
	value, ok := s.artists[artistID]
	if !ok {
		return artist.Artist{}, artist.ErrArtistNotFound
	}
	if s.locked != nil {
		return s.locked(value), nil
	}
	return value, nil
}

func (s *fakeStore) SourceTitles(_ context.Context, artistIDs []string) (map[string]string, error) {
	titles := map[string]string{}
	for _, id := range artistIDs {
		if title, ok := s.titles[id]; ok {
			titles[id] = title
		}
	}
	return titles, nil
}

func (s *fakeStore) SourceOgAssetIDs(context.Context, []string) (map[string]*string, error) {
	return s.ogAssets, nil
}

func (s *fakeStore) ResolveLocalization(context.Context, string, string) (publiccontent.Selection, error) {
	return publiccontent.Selection{DisplayedLocale: "en", SourceLocale: "en"}, nil
}

func (s *fakeStore) ResolveOGConsistency(_ context.Context, _ string, selection publiccontent.Selection, _ publiccontent.ReadyAsset) (publiccontent.Selection, error) {
	return selection, nil
}

func (s *fakeStore) Document(_ context.Context, artistID, _ string) (artist.PublicCreativeContentDocument, error) {
	return artist.PublicCreativeContentDocument{Revision: "revision", SourceTitle: s.titles[artistID]}, nil
}

func (s *fakeStore) IsGroup(context.Context, string) (bool, error) { return true, nil }

func (s *fakeStore) PublishedReleaseCount(context.Context, string) (int64, error) {
	return int64(len(s.releases)), nil
}

func (s *fakeStore) VisibleWorkCount(context.Context, string) (int64, error) {
	return int64(len(s.works)), nil
}

func (s *fakeStore) PublishedLabels(context.Context, string) ([]artistpublic.LabelRow, error) {
	return s.labels, nil
}

func (s *fakeStore) PublishedReleases(_ context.Context, _ string, sorts []artistpublic.Sort, _, _ int32) ([]artistpublic.ReleaseRow, error) {
	s.sorts = sorts
	return s.releases, nil
}

func (s *fakeStore) ReleaseArtists(context.Context, []string, string) ([]artistpublic.ReleaseArtistRow, error) {
	return s.credits, nil
}

func (s *fakeStore) VisibleWorks(_ context.Context, _ string, sorts []artistpublic.Sort, _, _ int32) ([]artistpublic.WorkRow, error) {
	s.sorts = sorts
	return s.works, nil
}

func (s *fakeStore) WorkLocalizations(context.Context, []string, string) (map[string]publiccontent.Selection, error) {
	return s.localized, nil
}

func (s *fakeStore) ShareLink(context.Context, string, string, time.Time) (*string, bool, error) {
	return s.shareHash, s.shareOK, nil
}

// media resolves every asset to a ref named after its ID.
type media struct{}

func ref(id string) *mediaasset.Reference { return &mediaasset.Reference{AssetID: id} }

func (media) LoadArtistImages(_ context.Context, ids []string) (map[string][]artist.ArtistImageProjection, error) {
	images := map[string][]artist.ArtistImageProjection{}
	for _, id := range ids {
		images[id] = []artist.ArtistImageProjection{{ArtistID: id, FileID: "image-" + id, Asset: ref("image-" + id)}}
	}
	return images, nil
}

func (media) ResolveReadyAssetRefs(_ context.Context, candidates []*string) (map[string]*mediaasset.Reference, error) {
	refs := map[string]*mediaasset.Reference{}
	for _, candidate := range candidates {
		if candidate != nil {
			refs[*candidate] = ref(*candidate)
		}
	}
	return refs, nil
}

func (media) ResolveArtistOGAsset(_ context.Context, source, localized *string) (*mediaasset.Reference, error) {
	if localized != nil {
		return ref(*localized), nil
	}
	if source != nil {
		return ref(*source), nil
	}
	return nil, nil
}

func (media) IsReadyAsset(context.Context, string) (bool, error) { return true, nil }

func (media) LoadReleaseArtworkAssets(_ context.Context, ids []string) (map[string]*mediaasset.Reference, error) {
	refs := map[string]*mediaasset.Reference{}
	for _, id := range ids {
		refs[id] = ref("artwork-" + id)
	}
	return refs, nil
}

func (media) LoadWorkImageAssets(_ context.Context, files map[string]*string) (map[string]*mediaasset.Reference, error) {
	refs := map[string]*mediaasset.Reference{}
	for id, file := range files {
		if file != nil {
			refs[id] = ref(*file)
		}
	}
	return refs, nil
}

func (media) LoadReadySourceFileAssets(_ context.Context, _ string, ids []string) (map[string]*mediaasset.Reference, error) {
	refs := map[string]*mediaasset.Reference{}
	for _, id := range ids {
		refs[id] = ref(id)
	}
	return refs, nil
}

func newPublicService(t *testing.T, store *fakeStore) *artistpublic.Service {
	t.Helper()
	service, err := artistpublic.NewService(artistpublic.Deps{
		Units: mutationtest.New[artistpublic.Store](store), SpiceDB: &authztest.Checker{}, Media: media{},
	})
	require.NoError(t, err)
	return service
}

func text(value string) *string { return &value }

func requireCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, code, errs.Wrap(err).Code(), err.Error())
}

func TestPublicArtistListShowsPublishedArtistsWithTheirImagesAndOG(t *testing.T) {
	store := &fakeStore{
		artists: map[string]artist.Artist{
			"published": {ID: "published", Status: artist.StatusPublished, SocialLinks: map[string]string{"site": "https://a.example"}},
			"draft":     {ID: "draft", Status: artist.StatusDraft},
		},
		titles:   map[string]string{"published": "Published"},
		ogAssets: map[string]*string{"published": text("og")},
	}

	page, err := newPublicService(t, store).List(t.Context(), artistpublic.ListArtistsQuery{})

	require.NoError(t, err)
	require.Equal(t, artistpublic.Pagination{Total: 1, Limit: 20}, page.Pagination)
	require.Len(t, page.Artists, 1)
	summary := page.Artists[0]
	require.Equal(t, "Published", summary.Name)
	require.Equal(t, "image-published", optional.Value(summary.Image).AssetID)
	require.Equal(t, "og", optional.Value(summary.OgImage).AssetID)
	require.Equal(t, map[string]string{"site": "https://a.example"}, summary.SocialLinks)

	delete(store.titles, "published")
	_, err = newPublicService(t, store).List(t.Context(), artistpublic.ListArtistsQuery{})
	requireCode(t, err, connect.CodeNotFound)
}

func TestPublicArtistGetProjectsRelationsAndGatesDrafts(t *testing.T) {
	store := &fakeStore{
		artists: map[string]artist.Artist{
			"published": {ID: "published", Slug: text("published"), Status: artist.StatusPublished},
			"draft":     {ID: "draft", Slug: text("draft"), Status: artist.StatusDraft},
		},
		titles:   map[string]string{"published": "Published", "draft": "Draft"},
		labels:   []artistpublic.LabelRow{{ID: "label", Name: "Label", ImageFileID: text("logo")}, {ID: "plain", Name: "Plain"}},
		releases: []artistpublic.ReleaseRow{{ID: "release", Title: "Release"}},
		works:    []artistpublic.WorkRow{{ID: "work", Title: "Work"}},
	}

	detail, err := newPublicService(t, store).Get(t.Context(), artistpublic.GetArtistQuery{SlugOrID: "published"})
	require.NoError(t, err)
	require.Equal(t, "Published", detail.Name)
	require.True(t, detail.IsGroup)
	require.EqualValues(t, 1, detail.ReleaseCount)
	require.EqualValues(t, 1, detail.WorkCount)
	require.Equal(t, []artistpublic.Label{{ID: "label", Name: "Label", Image: ref("logo")}, {ID: "plain", Name: "Plain"}}, detail.Labels)
	require.Equal(t, "image-published", optional.Value(detail.Images[0].Asset).AssetID)

	_, err = newPublicService(t, store).Get(t.Context(), artistpublic.GetArtistQuery{SlugOrID: "draft"})
	requireCode(t, err, connect.CodeNotFound)
	_, err = newPublicService(t, store).Get(t.Context(), artistpublic.GetArtistQuery{SlugOrID: "draft", ShareToken: "token"})
	requireCode(t, err, connect.CodeNotFound)
	store.shareOK = true
	shared, err := newPublicService(t, store).Get(t.Context(), artistpublic.GetArtistQuery{SlugOrID: "draft", ShareToken: "token"})
	require.NoError(t, err, "a live share link without a password admits the draft")
	require.Equal(t, "Draft", shared.Name)
	store.shareHash = text("hash")
	_, err = newPublicService(t, store).Get(t.Context(), artistpublic.GetArtistQuery{SlugOrID: "draft", ShareToken: "token"})
	requireCode(t, err, connect.CodeNotFound)
	_, err = newPublicService(t, store).Get(t.Context(), artistpublic.GetArtistQuery{SlugOrID: "missing"})
	requireCode(t, err, connect.CodeNotFound)
}

func TestPublicArtistGetHidesAnArtistUnpublishedWhileItWasRead(t *testing.T) {
	store := &fakeStore{
		artists: map[string]artist.Artist{"published": {ID: "published", Slug: text("published"), Status: artist.StatusPublished}},
		locked: func(value artist.Artist) artist.Artist {
			value.Status = artist.StatusDraft
			return value
		},
	}

	_, err := newPublicService(t, store).Get(t.Context(), artistpublic.GetArtistQuery{SlugOrID: "published"})

	requireCode(t, err, connect.CodeNotFound)
	require.Equal(t, "artist not found", errs.Wrap(err).Message())
}

func TestPublicArtistReleasesAndWorksValidateSortsAndLocalizeWorks(t *testing.T) {
	store := &fakeStore{
		releases: []artistpublic.ReleaseRow{{ID: "release", Title: "Release"}},
		credits:  []artistpublic.ReleaseArtistRow{{ReleaseID: "release", ArtistID: "other", ArtistName: "Other"}},
		works:    []artistpublic.WorkRow{{ID: "work", Title: "Work", ImageFileID: text("work-image")}},
		localized: map[string]publiccontent.Selection{
			"work": {Title: text("作品"), Summary: text("要約")},
		},
	}
	service := newPublicService(t, store)

	releases, err := service.GetReleases(t.Context(), artistpublic.ArtistWorksQuery{
		ArtistID: "artist", Sorts: []*commonv1.SortSpec{{Field: "title", Order: commonv1.SortOrder_SORT_ORDER_DESC}},
	})
	require.NoError(t, err)
	require.Equal(t, []artistpublic.Sort{{Field: "title", Descending: true}}, store.sorts)
	require.EqualValues(t, 1, releases.Total)
	require.Equal(t, "artwork-release", optional.Value(releases.Releases[0].Artwork).AssetID)
	require.Equal(t, []artistpublic.ReleaseArtist{{ID: "other", Name: "Other"}}, releases.Releases[0].Artists)
	_, err = service.GetReleases(t.Context(), artistpublic.ArtistWorksQuery{ArtistID: "artist", Sorts: []*commonv1.SortSpec{{Field: "type"}}})
	requireCode(t, err, connect.CodeInvalidArgument)

	works, err := service.GetWorks(t.Context(), artistpublic.ArtistWorksQuery{ArtistID: "artist"})
	require.NoError(t, err)
	require.Empty(t, store.sorts)
	require.Equal(t, "作品", works.Works[0].Title)
	require.Equal(t, "要約", *works.Works[0].Summary)
	require.Equal(t, "work-image", optional.Value(works.Works[0].Image).AssetID)
	_, err = service.GetWorks(t.Context(), artistpublic.ArtistWorksQuery{ArtistID: "artist", Sorts: []*commonv1.SortSpec{{Field: "release_date"}}})
	requireCode(t, err, connect.CodeInvalidArgument)
}
