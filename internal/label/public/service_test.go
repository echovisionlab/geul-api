package public_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	optional "github.com/echovisionlab/geul-api/internal/optional"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/label"

	labelpublic "github.com/echovisionlab/geul-api/internal/label/public"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/stretchr/testify/require"
)

// fakeStore serves the public reads from fixed rows; reads change nothing,
// so its savepoint is a no-op.
type fakeStore struct {
	labels   map[string]label.Label
	titles   map[string]string
	assets   map[string]*mediaasset.Reference
	logos    map[string]*mediaasset.Reference
	parent   labelpublic.ParentRow
	artists  []labelpublic.ArtistRow
	releases []labelpublic.ReleaseRow
	credits  []labelpublic.ReleaseArtistRow
	locked   func(label.Label) label.Label
}

func (s *fakeStore) Savepoint() mutationtest.Checkpoint {
	return mutationtest.Checkpoint{State: struct{}{}, Restore: func() {}}
}

func (s *fakeStore) ListPublishedLabels(_ context.Context, page label.ListPage) ([]label.Label, int64, error) {
	var listed []label.Label
	for _, value := range s.labels {
		if value.Status == label.StatusPublished {
			listed = append(listed, value)
		}
	}
	return listed, int64(len(listed)), nil
}

func (s *fakeStore) FindLabel(_ context.Context, slugOrID string, byID bool) (label.Label, error) {
	for _, value := range s.labels {
		if (byID && value.ID == slugOrID) || (!byID && value.Slug != nil && *value.Slug == slugOrID) {
			return value, nil
		}
	}
	return label.Label{}, label.ErrLabelNotFound
}

func (s *fakeStore) ShareLockLabel(_ context.Context, labelID string) (label.Label, error) {
	value, ok := s.labels[labelID]
	if !ok {
		return label.Label{}, label.ErrLabelNotFound
	}
	if s.locked != nil {
		return s.locked(value), nil
	}
	return value, nil
}

func (s *fakeStore) ResolveLocalization(context.Context, string, string) (publiccontent.Selection, error) {
	return publiccontent.Selection{DisplayedLocale: "en", SourceLocale: "en"}, nil
}

func (s *fakeStore) SourceTitles(_ context.Context, labelIDs []string) (map[string]string, error) {
	return s.titles, nil
}

func (s *fakeStore) Document(_ context.Context, labelID, _ string) (labelpublic.ContentDocument, error) {
	return labelpublic.ContentDocument{Revision: "revision", SourceTitle: s.titles[labelID]}, nil
}

func (s *fakeStore) ReadySourceFiles(_ context.Context, fileIDs []string, _ ...string) (map[string]*mediaasset.Reference, error) {
	return pick(s.logos, fileIDs), nil
}

func (s *fakeStore) ReadyAssets(_ context.Context, assetIDs []string) (map[string]*mediaasset.Reference, error) {
	return pick(s.assets, assetIDs), nil
}

func (s *fakeStore) ResolveOg(_ context.Context, assetID *string) (*mediaasset.Reference, error) {
	if assetID == nil {
		return nil, nil
	}
	return s.assets[*assetID], nil
}

func (s *fakeStore) RelationCounts(context.Context, string) (int32, int32, error) {
	return int32(len(s.releases)), int32(len(s.artists)), nil
}

func (s *fakeStore) PublishedParent(context.Context, string) (labelpublic.ParentRow, bool, error) {
	return s.parent, s.parent.ID != "", nil
}

func (s *fakeStore) PublishedArtists(context.Context, string) ([]labelpublic.ArtistRow, error) {
	return s.artists, nil
}

func (s *fakeStore) PublishedReleases(context.Context, string) ([]labelpublic.ReleaseRow, error) {
	return s.releases, nil
}

func (s *fakeStore) ReleaseArtists(context.Context, []string) ([]labelpublic.ReleaseArtistRow, error) {
	return s.credits, nil
}

func pick(source map[string]*mediaasset.Reference, ids []string) map[string]*mediaasset.Reference {
	refs := map[string]*mediaasset.Reference{}
	for _, id := range ids {
		if ref := source[id]; ref != nil {
			refs[id] = ref
		}
	}
	return refs
}

type draftAccess struct{ err error }

func (d draftAccess) Require(context.Context, string, string, string) error { return d.err }

func newPublicService(t *testing.T, store *fakeStore, draft draftAccess) *labelpublic.Service {
	t.Helper()
	service, err := labelpublic.NewService(labelpublic.Deps{Units: mutationtest.New[labelpublic.Store](store), Draft: draft})
	require.NoError(t, err)
	return service
}

func text(value string) *string { return &value }

func TestPublicLabelListShowsOGOnlyWithAnEffectiveLogo(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStore{
		labels: map[string]label.Label{
			"with-logo":    {ID: "with-logo", Status: label.StatusPublished, LogoDarkFileID: text("dark"), OgAssetID: text("og-1"), PublishedAt: &now},
			"without-logo": {ID: "without-logo", Status: label.StatusPublished, OgAssetID: text("og-2")},
			"draft":        {ID: "draft", Status: label.StatusDraft},
		},
		titles: map[string]string{"with-logo": "With logo", "without-logo": "Without logo"},
		assets: map[string]*mediaasset.Reference{"og-1": {AssetID: "og-1"}, "og-2": {AssetID: "og-2"}},
		logos:  map[string]*mediaasset.Reference{"dark": {AssetID: "dark"}},
	}

	page, err := newPublicService(t, store, draftAccess{}).List(t.Context(), labelpublic.ListLabelsQuery{})

	require.NoError(t, err)
	require.Equal(t, labelpublic.Pagination{Total: 2, Limit: 20}, page.Pagination)
	summaries := map[string]labelpublic.Summary{}
	for _, summary := range page.Labels {
		summaries[summary.ID] = summary
	}
	require.Equal(t, "og-1", optional.Value(summaries["with-logo"].OgImage).AssetID)
	require.Equal(t, "dark", optional.Value(summaries["with-logo"].DarkLogo).AssetID)
	require.Nil(t, summaries["without-logo"].OgImage)
	require.Equal(t, "Without logo", summaries["without-logo"].Name)
}

func TestPublicLabelGetProjectsRelationsAndGatesDrafts(t *testing.T) {
	store := &fakeStore{
		labels: map[string]label.Label{
			"published": {ID: "published", Slug: text("published"), Status: label.StatusPublished, ParentLabelID: text("parent")},
			"draft":     {ID: "draft", Slug: text("draft"), Status: label.StatusDraft},
		},
		titles:   map[string]string{"published": "Published"},
		parent:   labelpublic.ParentRow{ID: "parent", Name: "Parent"},
		artists:  []labelpublic.ArtistRow{{ID: "artist", Name: "Artist", ImageFileID: text("image")}},
		releases: []labelpublic.ReleaseRow{{ID: "release", Title: "Release"}},
		credits:  []labelpublic.ReleaseArtistRow{{ReleaseID: "release", ArtistID: "artist", ArtistName: "Artist"}},
		logos:    map[string]*mediaasset.Reference{"image": {AssetID: "image"}},
	}
	denied := errors.New("share token required")

	detail, err := newPublicService(t, store, draftAccess{err: denied}).Get(t.Context(), labelpublic.GetLabelQuery{SlugOrID: "published"})
	require.NoError(t, err)
	require.Equal(t, "Published", detail.Name)
	require.Equal(t, "Parent", detail.Parent.Name)
	require.Equal(t, "image", optional.Value(detail.Artists[0].Image).AssetID)
	require.Equal(t, []labelpublic.ReleaseArtist{{ID: "artist", Name: "Artist"}}, detail.Releases[0].Artists)
	require.EqualValues(t, 1, detail.ReleaseCount)

	_, err = newPublicService(t, store, draftAccess{err: denied}).Get(t.Context(), labelpublic.GetLabelQuery{SlugOrID: "draft"})
	require.ErrorIs(t, err, denied)
	_, err = newPublicService(t, store, draftAccess{}).Get(t.Context(), labelpublic.GetLabelQuery{SlugOrID: "draft"})
	require.NoError(t, err, "admitted draft access reads the draft")
	_, err = newPublicService(t, store, draftAccess{}).Get(t.Context(), labelpublic.GetLabelQuery{SlugOrID: "missing"})
	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code())
}

func TestPublicLabelGetHidesALabelUnpublishedWhileItWasRead(t *testing.T) {
	store := &fakeStore{
		labels: map[string]label.Label{"published": {ID: "published", Slug: text("published"), Status: label.StatusPublished}},
		locked: func(value label.Label) label.Label {
			value.Status = label.StatusDraft
			return value
		},
	}

	_, err := newPublicService(t, store, draftAccess{}).Get(t.Context(), labelpublic.GetLabelQuery{SlugOrID: "published"})

	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code())
	require.Equal(t, "label not found", errs.Wrap(err).Message())
}
