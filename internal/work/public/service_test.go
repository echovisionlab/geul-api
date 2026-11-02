package public_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/crypto"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/memberref"

	optional "github.com/echovisionlab/geul-api/internal/optional"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/publiccontent"

	workdomain "github.com/echovisionlab/geul-api/internal/work"
	"github.com/echovisionlab/geul-api/internal/work/public"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

var errReadDown = errors.New("read unavailable")

// fakeStore is the public Work Store over in-memory rows; Fail makes the
// named method fail.
type fakeStore struct {
	works         map[string]public.Work
	sources       map[string]workdomain.SourceDocument
	localizations map[string]publiccontent.Selection
	groups        map[string][]workdomain.CreditGroup
	credits       map[string][]workdomain.Credit
	clients       map[string][]workdomain.ClientRow
	artists       map[string]public.CreditArtistRow
	links         map[string]shareLink
	places        []public.MapFeatureRow
	Fail          map[string]error
}

type shareLink struct {
	workID    string
	expiresAt time.Time
	link      public.ShareLink
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		works: map[string]public.Work{}, sources: map[string]workdomain.SourceDocument{},
		localizations: map[string]publiccontent.Selection{}, groups: map[string][]workdomain.CreditGroup{},
		credits: map[string][]workdomain.Credit{}, clients: map[string][]workdomain.ClientRow{},
		artists: map[string]public.CreditArtistRow{}, links: map[string]shareLink{}, Fail: map[string]error{},
	}
}

// Savepoint implements mutationtest.Savepointer; the reads write nothing to
// roll back.
func (s *fakeStore) Savepoint() mutationtest.Checkpoint {
	return mutationtest.Checkpoint{State: struct{}{}, Restore: func() {}}
}

// put stores a Work and its source title.
func (s *fakeStore) put(status workdomain.Status, title string) public.Work {
	id := uuid.NewString()
	slug := "work-" + id
	documentID := uuid.NewString()
	value := public.Work{Work: workdomain.Work{ID: id, Slug: &slug, Status: status, SourceLocale: "en", ContentDocumentID: &documentID}}
	s.works[id] = value
	s.sources[id] = workdomain.SourceDocument{Title: &title}
	return value
}

func (s *fakeStore) ListWorks(_ context.Context, page public.ListPage) ([]workdomain.Work, int64, error) {
	if err := s.Fail["ListWorks"]; err != nil {
		return nil, 0, err
	}
	var listed []workdomain.Work
	for _, value := range s.works {
		if slices.Contains(public.PublicStatuses(), string(value.Status)) {
			listed = append(listed, value.Work)
		}
	}
	slices.SortFunc(listed, func(a, b workdomain.Work) int { return compare(a.ID, b.ID) })
	total := int64(len(listed))
	listed = listed[min(int(page.Offset), len(listed)):]
	return listed[:min(int(page.Limit), len(listed))], total, nil
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (s *fakeStore) SourceDocuments(_ context.Context, workIDs []string) (map[string]workdomain.SourceDocument, error) {
	documents := map[string]workdomain.SourceDocument{}
	for _, id := range workIDs {
		if source, ok := s.sources[id]; ok {
			documents[id] = source
		}
	}
	return documents, nil
}

func (s *fakeStore) ResolveLocalizations(_ context.Context, workIDs []string, _ string) (map[string]publiccontent.Selection, error) {
	if err := s.Fail["ResolveLocalizations"]; err != nil {
		return nil, err
	}
	selections := map[string]publiccontent.Selection{}
	for _, id := range workIDs {
		if selection, ok := s.localizations[id]; ok {
			selections[id] = selection
		}
	}
	return selections, nil
}

func (s *fakeStore) MapFeatures(context.Context, public.MapBounds, []*commonv1.FilterSpec, []*commonv1.SortSpec) ([]public.MapFeatureRow, error) {
	return s.places, nil
}

func (s *fakeStore) FindWork(_ context.Context, slugOrID string, byID bool) (public.Work, error) {
	for _, value := range s.works {
		if (byID && value.ID == slugOrID) || (!byID && value.Slug != nil && *value.Slug == slugOrID) {
			return value, nil
		}
	}
	return public.Work{}, workdomain.ErrWorkNotFound
}

func (s *fakeStore) ContentDocumentID(_ context.Context, workID string) (uuid.UUID, error) {
	return uuid.MustParse(*s.works[workID].ContentDocumentID), nil
}

func (s *fakeStore) ResolveLocalization(_ context.Context, workID, _ string) (publiccontent.Selection, error) {
	if selection, ok := s.localizations[workID]; ok {
		return selection, nil
	}
	return publiccontent.Selection{DisplayedLocale: "en", SourceLocale: "en"}, nil
}

func (s *fakeStore) ResolveOGConsistency(_ context.Context, _ string, selection publiccontent.Selection) (publiccontent.Selection, error) {
	return selection, nil
}

func (s *fakeStore) Document(_ context.Context, workID string, _ uuid.UUID, locale string) (public.Document, error) {
	return public.Document{
		Document:   &contentv1.LocalizedRichTextDocument{Locale: locale},
		Revision:   "revision-" + workID,
		BlockMedia: []*fileref.BlockMediaItem{{}},
	}, nil
}

func (s *fakeStore) CreditGroups(_ context.Context, workID string) ([]workdomain.CreditGroup, error) {
	if err := s.Fail["CreditGroups"]; err != nil {
		return nil, err
	}
	return s.groups[workID], nil
}

func (s *fakeStore) Credits(_ context.Context, workID string) ([]workdomain.Credit, error) {
	if err := s.Fail["Credits"]; err != nil {
		return nil, err
	}
	return s.credits[workID], nil
}

func (s *fakeStore) Clients(_ context.Context, workID string) ([]workdomain.ClientRow, error) {
	if err := s.Fail["Clients"]; err != nil {
		return nil, err
	}
	return s.clients[workID], nil
}

func (s *fakeStore) ShareLink(_ context.Context, token, workID string, now time.Time) (public.ShareLink, bool, error) {
	stored, ok := s.links[token]
	if !ok || stored.workID != workID || !stored.expiresAt.After(now) {
		return public.ShareLink{}, false, nil
	}
	return stored.link, true, nil
}

func (s *fakeStore) CreditArtist(_ context.Context, artistID string) (public.CreditArtistRow, bool, error) {
	if err := s.Fail["CreditArtist"]; err != nil {
		return public.CreditArtistRow{}, false, err
	}
	artist, ok := s.artists[artistID]
	return artist, ok, nil
}

// assets resolves every ready asset to a URL naming what it stands for.
type assets struct{}

func (assets) ResolveReadyOGAsset(_ context.Context, source, localized *string) (*mediaasset.Reference, error) {
	if localized != nil {
		return &mediaasset.Reference{URL: "og:" + *localized}, nil
	}
	if source != nil {
		return &mediaasset.Reference{URL: "og:" + *source}, nil
	}
	return nil, nil
}

func (assets) ResolveReadyAssetForSourceFile(_ context.Context, fileID string, kinds ...string) *mediaasset.Reference {
	return &mediaasset.Reference{URL: kinds[0] + ":" + fileID}
}

func (assets) ResolveArtistImageAsset(_ context.Context, artistID string) *mediaasset.Reference {
	return &mediaasset.Reference{URL: "artist:" + artistID}
}

// blockMedia records the download authorization each hydration ran under.
type blockMedia struct {
	authorizations []mediaasset.ContentDownloadOwnerAuthorization
}

func (m *blockMedia) HydrateAuthorizedContentBlockMedia(ctx context.Context, items []*fileref.BlockMediaItem) ([]*fileref.BlockMediaItem, error) {
	authorization, _ := mediaasset.ContentDownloadOwnerAuthorizationFromContext(ctx)
	m.authorizations = append(m.authorizations, authorization)
	return items, nil
}

type members map[string]*memberref.Summary

func (m members) LoadPublicMemberSummaries(_ context.Context, ids []string) (map[string]*memberref.Summary, error) {
	summaries := map[string]*memberref.Summary{}
	for _, id := range ids {
		if summary, ok := m[id]; ok {
			summaries[id] = summary
		}
	}
	return summaries, nil
}

type harness struct {
	service *public.Service
	store   *fakeStore
	checker *authztest.Checker
	media   *blockMedia
	members members
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{store: newFakeStore(), checker: authztest.AllowAll(), media: &blockMedia{}, members: members{}}
	service, err := public.NewService(public.Deps{
		Units: mutationtest.New[public.Store](h.store), SpiceDB: h.checker, BlockMedia: h.media, Assets: assets{}, Members: h.members,
	})
	require.NoError(t, err)
	h.service = service
	return h
}

func signedIn() context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
}

func requireNotFound(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code(), "%v", err)
}

func TestGetServesAPublishedOrArchivedWorkToAnyReader(t *testing.T) {
	h := newHarness(t)
	for _, status := range []workdomain.Status{workdomain.StatusPublished, workdomain.StatusArchived} {
		value := h.store.put(status, "Public")
		detail, err := h.service.Get(context.Background(), public.GetWorkQuery{Slug: *value.Slug})
		require.NoError(t, err)
		require.Equal(t, value.ID, detail.Work.ID)
		require.Equal(t, mediaasset.ContentDownloadOwnerAccessPublic, h.media.authorizations[len(h.media.authorizations)-1].Mode)
	}
	require.Empty(t, h.checker.Checks(), "a public Work needs no permission")
	_, err := h.service.Get(context.Background(), public.GetWorkQuery{Slug: uuid.NewString()})
	requireNotFound(t, err)
}

func TestGetServesADraftToItsViewerAndThroughItsShareLink(t *testing.T) {
	h := newHarness(t)
	draft := h.store.put(workdomain.StatusDraft, "Draft")
	view, err := policyv1.Work.View(draft.ID)
	require.NoError(t, err)

	_, err = h.service.Get(context.Background(), public.GetWorkQuery{Slug: draft.ID})
	requireNotFound(t, err)
	reader := signedIn()
	_, err = h.service.Get(reader, public.GetWorkQuery{Slug: draft.ID})
	require.NoError(t, err)
	require.Equal(t, mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft, h.media.authorizations[0].Mode)
	require.Equal(t, auth.GetUser(reader).MemberID.String(), h.media.authorizations[0].MemberID)

	h.checker.Deny(view, nil)
	_, err = h.service.Get(reader, public.GetWorkQuery{Slug: draft.ID})
	requireNotFound(t, err)

	open, locked, expired := uuid.NewString(), uuid.NewString(), uuid.NewString()
	hash, err := crypto.NewPasswordHasher(nil).Hash("secret")
	require.NoError(t, err)
	later := time.Now().Add(time.Hour)
	witness := &mediaasset.ContentDownloadShareLinkWitness{}
	h.store.links[open] = shareLink{workID: draft.ID, expiresAt: later, link: public.ShareLink{Witness: witness}}
	h.store.links[locked] = shareLink{workID: draft.ID, expiresAt: later, link: public.ShareLink{PasswordHash: &hash}}
	h.store.links[expired] = shareLink{workID: draft.ID, expiresAt: time.Now().Add(-time.Minute)}
	other := h.store.put(workdomain.StatusDraft, "Other draft")
	viewOther, err := policyv1.Work.View(other.ID)
	require.NoError(t, err)
	h.checker.Deny(viewOther, nil)
	for name, query := range map[string]public.GetWorkQuery{
		"expired link":           {Slug: draft.ID, ShareToken: expired},
		"another Work's link":    {Slug: other.ID, ShareToken: open},
		"missing password":       {Slug: draft.ID, ShareToken: locked},
		"wrong password":         {Slug: draft.ID, ShareToken: locked, SharePassword: "wrong"},
		"denied viewer no token": {Slug: draft.ID},
	} {
		_, err := h.service.Get(reader, query)
		requireNotFound(t, err)
		require.Len(t, h.media.authorizations, 1, name)
	}
	_, err = h.service.Get(context.Background(), public.GetWorkQuery{Slug: draft.ID, ShareToken: open})
	require.NoError(t, err)
	require.Equal(t, mediaasset.ContentDownloadOwnerAccessShare, h.media.authorizations[1].Mode)
	require.Same(t, witness, h.media.authorizations[1].ShareLink)
	_, err = h.service.Get(context.Background(), public.GetWorkQuery{Slug: draft.ID, ShareToken: locked, SharePassword: "secret"})
	require.NoError(t, err)
}

func TestGetProjectsTheDisplayedLocaleAndTheWorksRelations(t *testing.T) {
	h := newHarness(t)
	value := h.store.put(workdomain.StatusPublished, "Source title")
	featured, sourceOG, localizedOG := "featured-file", "source-og", "localized-og"
	value.FeaturedImageFileID = &featured
	h.store.works[value.ID] = value
	h.store.sources[value.ID] = workdomain.SourceDocument{Title: stringPointer("Source title"), Summary: stringPointer("Source summary"), OgAssetID: &sourceOG}
	h.store.localizations[value.ID] = publiccontent.Selection{DisplayedLocale: "ko", SourceLocale: "en", Title: stringPointer("번역 제목"), OgAssetID: &localizedOG}
	groupID, artistID, memberID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	h.store.groups[value.ID] = []workdomain.CreditGroup{{ID: groupID, WorkID: value.ID, Name: "Group"}}
	h.store.credits[value.ID] = []workdomain.Credit{
		{ID: "artist-credit", GroupID: &groupID, ArtistID: &artistID},
		{ID: "member-credit", MemberID: &memberID},
		{ID: "gone-artist-credit", ArtistID: stringPointer(uuid.NewString())},
	}
	h.store.artists[artistID] = public.CreditArtistRow{ID: artistID, Name: "Artist"}
	h.members[memberID] = &memberref.Summary{ID: memberID}
	h.store.clients[value.ID] = []workdomain.ClientRow{
		{ID: "client-b", Name: "B", DarkFileID: stringPointer("dark-file")},
		{ID: "client-a", Name: "A", LightFileID: stringPointer("light-file")},
	}

	detail, err := h.service.Get(context.Background(), public.GetWorkQuery{Slug: value.ID, AcceptLanguage: "ko"})

	require.NoError(t, err)
	require.Equal(t, "번역 제목", detail.Title)
	require.Equal(t, "Source summary", *detail.Summary, "a locale without its own summary shows the source summary")
	require.Equal(t, "og:"+localizedOG, optional.Value(detail.OgAsset).URL)
	require.Equal(t, "image:"+featured, optional.Value(detail.FeaturedImage).URL)
	require.Equal(t, "ko", detail.Document.GetLocale())
	require.Equal(t, "revision-"+value.ID, detail.Revision)
	require.Equal(t, h.store.groups[value.ID], detail.CreditGroups)
	require.Equal(t, &public.CreditArtist{ID: artistID, Name: "Artist", Image: &mediaasset.Reference{URL: "artist:" + artistID}}, detail.Credits[0].Artist)
	require.Equal(t, memberID, detail.Credits[1].Member.ID)
	require.Nil(t, detail.Credits[2].Artist, "a credit whose Artist is gone shows without it")
	require.Equal(t, []public.Client{
		{ID: "client-b", Name: "B", DarkLogo: &mediaasset.Reference{URL: "logo:dark-file"}},
		{ID: "client-a", Name: "A", LightLogo: &mediaasset.Reference{URL: "logo:light-file"}},
	}, detail.Clients)

	h.store.localizations[value.ID] = publiccontent.Selection{DisplayedLocale: "ko", OmitSourceOgFallback: true}
	detail, err = h.service.Get(context.Background(), public.GetWorkQuery{Slug: value.ID})
	require.NoError(t, err)
	require.Nil(t, detail.OgAsset, "a locale that omits the source OG shows none")
}

func TestGetServesAWorkWithoutTheOptionalRelationsThatFailed(t *testing.T) {
	h := newHarness(t)
	value := h.store.put(workdomain.StatusPublished, "Partial")
	h.store.clients[value.ID] = []workdomain.ClientRow{{ID: "client"}}
	h.store.Fail = map[string]error{"CreditGroups": errReadDown, "Clients": errReadDown}

	detail, err := h.service.Get(context.Background(), public.GetWorkQuery{Slug: value.ID})
	require.NoError(t, err)
	require.Empty(t, detail.CreditGroups)
	require.Empty(t, detail.Clients)

	h.store.Fail = map[string]error{"Credits": errReadDown}
	_, err = h.service.Get(context.Background(), public.GetWorkQuery{Slug: value.ID})
	require.ErrorIs(t, err, errReadDown, "credits are not optional")
}

func TestListShowsEachWorkInItsDisplayedLocale(t *testing.T) {
	h := newHarness(t)
	localized := h.store.put(workdomain.StatusPublished, "Source")
	plain := h.store.put(workdomain.StatusArchived, "Archived")
	h.store.put(workdomain.StatusDraft, "Draft")
	featured := "featured-file"
	plainWork := h.store.works[plain.ID]
	plainWork.FeaturedImageFileID = &featured
	h.store.works[plain.ID] = plainWork
	h.store.localizations[localized.ID] = publiccontent.Selection{DisplayedLocale: "ko", Title: stringPointer("번역"), Summary: stringPointer("요약")}

	page, err := h.service.List(context.Background(), public.ListWorksQuery{AcceptLanguage: "ko"})

	require.NoError(t, err)
	require.Equal(t, public.Pagination{Total: 2, Limit: 20}, page.Pagination)
	byID := map[string]public.Summary{}
	for _, summary := range page.Works {
		byID[summary.Work.ID] = summary
	}
	require.Equal(t, "번역", byID[localized.ID].Title)
	require.Equal(t, "요약", *byID[localized.ID].Summary)
	require.Equal(t, "Archived", byID[plain.ID].Title)
	require.Equal(t, "image:"+featured, optional.Value(byID[plain.ID].FeaturedImage).URL)

	h.store.Fail = map[string]error{"ResolveLocalizations": errReadDown}
	page, err = h.service.List(context.Background(), public.ListWorksQuery{})
	require.NoError(t, err, "a failed localization lists the source copy")
	require.ElementsMatch(t, []string{"Source", "Archived"}, []string{page.Works[0].Title, page.Works[1].Title})

	page, err = h.service.List(context.Background(), public.ListWorksQuery{Limit: 1})
	require.NoError(t, err)
	require.True(t, page.Pagination.HasMore)

	delete(h.store.sources, plain.ID)
	_, err = h.service.List(context.Background(), public.ListWorksQuery{})
	require.ErrorContains(t, err, "missing work source locale row for public list item "+plain.ID)
}

func TestListMapFeaturesGroupsTheWorksOfAPlace(t *testing.T) {
	h := newHarness(t)
	first := h.store.put(workdomain.StatusPublished, "First")
	second := h.store.put(workdomain.StatusPublished, "Second")
	place := public.MapFeatureRow{PlaceID: "place", PlaceName: "Place", PlaceLat: 37.5, PlaceLng: 127}
	for _, value := range []public.Work{first, second} {
		row := place
		row.WorkID, row.WorkTitle = value.ID, *h.store.sources[value.ID].Title
		h.store.places = append(h.store.places, row)
	}
	viewport := &public.Viewport{Bounds: &public.Bounds{West: 126, South: 37, East: 128, North: 38}, Zoom: 12, WidthPx: 1280, HeightPx: 720, ClusterRadiusPx: 56, MinClusterPoints: 2}

	features, err := h.service.ListMapFeatures(context.Background(), public.ListWorkMapFeaturesQuery{Viewport: viewport})

	require.NoError(t, err)
	require.Empty(t, features.Clusters)
	require.Len(t, features.Items, 1)
	require.Equal(t, "place", features.Items[0].PlaceID)
	require.EqualValues(t, 2, features.Items[0].WorkCount)

	delete(h.store.sources, second.ID)
	_, err = h.service.ListMapFeatures(context.Background(), public.ListWorkMapFeaturesQuery{Viewport: viewport})
	require.ErrorContains(t, err, "missing work source locale row for public map item "+second.ID)
	_, err = h.service.ListMapFeatures(context.Background(), public.ListWorkMapFeaturesQuery{})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
}

func stringPointer(value string) *string { return &value }
