// Package releasetest holds the in-memory release.Store the Release
// use-case tests run on, and the Store contract it shares with
// releasepostgres. The fake knows no SQL and takes no lock; it keeps the
// sentinels, orderings and row changes the PostgreSQL store answers with,
// and a minimal content document whose revision advances like the content
// store's.
package releasetest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/release"
	"github.com/google/uuid"
)

// Document is a fake content document: its revision, profile and source
// locale, its Blocks, and the locales its Blocks carry.
type Document struct {
	ID           uuid.UUID
	Profile      string
	Revision     uuid.UUID
	SourceLocale string
	Blocks       []contentblock.BaseBlock
	Overlays     []contentblock.LocaleOverlay
	Locales      map[string]bool
}

// OGRequest is one OG generation the Store requested.
type OGRequest struct {
	ReleaseID string
	Reason    string
}

// Member is a fake Member row.
type Member struct {
	Nickname        string
	PreferredLocale *string
	Deleted         bool
}

type localeKey struct{ releaseID, locale string }

// creditRow is a stored Release credit and when it was added.
type creditRow struct {
	release.ReleaseCredit
	created int
}

// artistRow is a stored Release Artist and when it was added.
type artistRow struct {
	release.ReleaseArtist
	created int
}

type state struct {
	releases        map[string]release.Release
	locales         map[localeKey]release.LocaleMetadata
	notes           map[localeKey]map[string]string
	artists         map[string][]artistRow
	labels          map[string][]release.ReleaseLabel
	formats         map[string][]release.ReleaseFormat
	credits         map[string][]creditRow
	references      map[release.ReferenceRelation]map[string][]string
	tracks          map[string]release.Track
	trackCredits    map[string][]release.TrackCredit
	criteriaSet     map[string][]string
	uploads         map[string]bool
	documents       map[uuid.UUID]Document
	files           map[string]string
	artwork         map[string]string
	bindings        map[string]string
	shareLinks      map[string]int
	updates         []release.ContentUpdate
	ogRequests      []OGRequest
	ogCancels       []string
	lockedFiles     []string
	lockedMembers   []string
	sequence        int
	contentResults  []contentblock.Result
	defaultLocale   string
	translationErr  error
	contributorsErr error
}

func (s state) clone() state {
	clone := s
	clone.releases = mutationtest.ClonePersisted(s.releases)
	clone.locales = mutationtest.ClonePersisted(s.locales)
	clone.notes = mutationtest.ClonePersisted(s.notes)
	clone.artists = cloneLists(s.artists)
	clone.labels = cloneLists(s.labels)
	clone.formats = cloneLists(s.formats)
	clone.credits = cloneLists(s.credits)
	clone.references = mutationtest.ClonePersisted(s.references)
	clone.tracks = mutationtest.ClonePersisted(s.tracks)
	clone.trackCredits = cloneLists(s.trackCredits)
	clone.criteriaSet = cloneLists(s.criteriaSet)
	clone.uploads = mutationtest.ClonePersisted(s.uploads)
	clone.documents = mutationtest.ClonePersisted(s.documents)
	clone.files = mutationtest.ClonePersisted(s.files)
	clone.artwork = mutationtest.ClonePersisted(s.artwork)
	clone.bindings = mutationtest.ClonePersisted(s.bindings)
	clone.shareLinks = mutationtest.ClonePersisted(s.shareLinks)
	clone.updates = mutationtest.ClonePersisted(s.updates)
	clone.ogRequests = mutationtest.ClonePersisted(s.ogRequests)
	clone.ogCancels = mutationtest.ClonePersisted(s.ogCancels)
	clone.lockedFiles = slices.Clone(s.lockedFiles)
	clone.lockedMembers = slices.Clone(s.lockedMembers)
	clone.contentResults = slices.Clone(s.contentResults)
	clone.defaultLocale = mutationtest.ClonePersisted(s.defaultLocale)
	return clone
}

func cloneLists[K comparable, V any](lists map[K][]V) map[K][]V {
	clone := make(map[K][]V, len(lists))
	for key, list := range lists {
		clone[key] = mutationtest.ClonePersisted(list)
	}
	return clone
}

// Store is the in-memory release.Store. Seed it with the Put and Add
// methods; read it back with the getters.
type Store struct {
	mu sync.Mutex
	state
	// Now is the clock of the rows it timestamps; zero is time.Now.
	Now func() time.Time
	// Fail fails the Store method of that name with its error.
	Fail map[string]error
	// OnUploadCheck runs before HasTrackUploadSessions answers, in its
	// transaction, to play an upload that starts concurrently.
	OnUploadCheck func()
	// Members are the Member rows a credit or a collaboration save locks.
	Members map[string]Member
	// Sessions are the principals of collaboration sessions.
	Sessions map[string]*auth.UserInfo
	// Artists, Labels and Catalog are the other domains' rows a Release
	// links to; ReadyAssets are the ready artwork assets by File ID; Routes
	// are the slugs another route holds.
	Artists     map[string]release.Summary
	Labels      map[string]release.Summary
	Catalog     map[string]release.Reference
	ReadyAssets map[string]*mediaasset.Reference
	Routes      map[string]bool
}

// NewStore returns an empty fake Store.
func NewStore() *Store {
	return &Store{
		state: state{
			releases: map[string]release.Release{}, locales: map[localeKey]release.LocaleMetadata{},
			notes: map[localeKey]map[string]string{}, artists: map[string][]artistRow{},
			labels: map[string][]release.ReleaseLabel{}, formats: map[string][]release.ReleaseFormat{},
			credits: map[string][]creditRow{}, references: map[release.ReferenceRelation]map[string][]string{},
			tracks: map[string]release.Track{}, trackCredits: map[string][]release.TrackCredit{},
			criteriaSet: map[string][]string{}, uploads: map[string]bool{}, documents: map[uuid.UUID]Document{},
			files: map[string]string{}, artwork: map[string]string{}, bindings: map[string]string{},
			shareLinks: map[string]int{}, defaultLocale: "en",
		},
		Fail: map[string]error{}, Members: map[string]Member{}, Sessions: map[string]*auth.UserInfo{},
		Artists: map[string]release.Summary{}, Labels: map[string]release.Summary{},
		Catalog: map[string]release.Reference{}, ReadyAssets: map[string]*mediaasset.Reference{},
		Routes: map[string]bool{},
	}
}

// New returns a fake Store and the unit of work over it.
func New() (*Store, *mutationtest.UnitOfWork[release.Store]) {
	store := NewStore()
	return store, mutationtest.New[release.Store](store)
}

var _ release.Store = (*Store)(nil)

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	saved := s.state.clone()
	s.mu.Unlock()
	return mutationtest.Checkpoint{
		State: map[string]any{
			"releases":      saved.releases,
			"locales":       saved.locales,
			"notes":         saved.notes,
			"artists":       saved.artists,
			"labels":        saved.labels,
			"formats":       saved.formats,
			"credits":       saved.credits,
			"references":    saved.references,
			"tracks":        saved.tracks,
			"trackCredits":  saved.trackCredits,
			"criteria":      saved.criteriaSet,
			"uploads":       saved.uploads,
			"documents":     saved.documents,
			"files":         saved.files,
			"artwork":       saved.artwork,
			"bindings":      saved.bindings,
			"shareLinks":    saved.shareLinks,
			"updates":       saved.updates,
			"ogRequests":    saved.ogRequests,
			"ogCancels":     saved.ogCancels,
			"defaultLocale": saved.defaultLocale,
		},
		Restore: func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.state = saved
		},
	}
}

func (s *Store) fail(method string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Fail[method]
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// next is the next row sequence number, which orders rows by creation.
func (s *Store) next() int {
	s.sequence++
	return s.sequence
}

// Seeding.

// PutRelease stores a Release row as it is.
func (s *Store) PutRelease(value release.Release) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases[value.ID] = value
}

// PutLocale stores a Release locale and its credit notes.
func (s *Store) PutLocale(releaseID string, metadata release.LocaleMetadata, notes map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{releaseID, metadata.Locale}
	s.locales[key] = metadata
	if notes != nil {
		s.notes[key] = maps.Clone(notes)
	}
}

// PutDocument stores a content document.
func (s *Store) PutDocument(document Document) {
	s.putDocument(document)
}

// AddReleaseArtist, AddReleaseLabel, AddReleaseFormat and AddReleaseCredit
// add a relation row in its order.
func (s *Store) AddReleaseArtist(releaseID string, artist release.ReleaseArtist) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.artists[releaseID] = append(s.artists[releaseID], artistRow{ReleaseArtist: artist, created: s.next()})
}

func (s *Store) AddReleaseLabel(releaseID string, label release.ReleaseLabel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.labels[releaseID] = append(s.labels[releaseID], label)
}

func (s *Store) AddReleaseFormat(releaseID string, format release.ReleaseFormat) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.formats[releaseID] = append(s.formats[releaseID], format)
}

func (s *Store) AddReleaseCredit(releaseID string, credit release.ReleaseCredit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credits[releaseID] = append(s.credits[releaseID], creditRow{ReleaseCredit: credit, created: s.next()})
}

// AddReference links the Release to a catalog reference of the relation.
func (s *Store) AddReference(relation release.ReferenceRelation, releaseID, referenceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.references[relation] == nil {
		s.references[relation] = map[string][]string{}
	}
	s.references[relation][releaseID] = append(s.references[relation][releaseID], referenceID)
}

// PutTrack stores a Track row as it is.
func (s *Store) PutTrack(track release.Track) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tracks[track.ID] = track
}

// AddTrackCredit adds a Track credit in its order.
func (s *Store) AddTrackCredit(credit release.TrackCredit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trackCredits[credit.TrackID] = append(s.trackCredits[credit.TrackID], credit)
}

// PutTrackCriteriaSet stores the Member criteria of a Track's download
// policy.
func (s *Store) PutTrackCriteriaSet(trackID string, criteriaIDs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.criteriaSet[trackID] = slices.Clone(criteriaIDs)
}

// SetUploadSession marks whether a Track has an audio upload session.
func (s *Store) SetUploadSession(trackID string, open bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if open {
		s.uploads[trackID] = true
	} else {
		delete(s.uploads, trackID)
	}
}

// PutFile stores a File row whose ID reads as stored.
func (s *Store) PutFile(requestedID, storedID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[requestedID] = storedID
}

// PutArtwork makes fileID the Release's artwork File.
func (s *Store) PutArtwork(releaseID, fileID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.artwork[releaseID] = fileID
}

// SetTranslationDefault sets the translation default locale, or its
// failure.
func (s *Store) SetTranslationDefault(locale string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaultLocale, s.translationErr = locale, err
}

// RevokeContributors makes RequireCurrentContributors answer err.
func (s *Store) RevokeContributors(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contributorsErr = err
}

// QueueContentResult makes the next source content write answer result.
func (s *Store) QueueContentResult(result contentblock.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contentResults = append(s.contentResults, result)
}

// Reading back.

// Release returns a stored Release row.
func (s *Store) Release(releaseID string) (release.Release, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.releases[releaseID]
	return value, ok
}

// Releases returns every stored Release, by ID.
func (s *Store) Releases() []release.Release {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := slices.Collect(maps.Values(s.releases))
	slices.SortFunc(values, func(a, b release.Release) int { return strings.Compare(a.ID, b.ID) })
	return values
}

// Locale returns a stored Release locale and its credit notes.
func (s *Store) Locale(releaseID, locale string) (release.LocaleMetadata, map[string]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{releaseID, locale}
	value, ok := s.locales[key]
	return value, maps.Clone(s.notes[key]), ok
}

// Document returns a stored content document.
func (s *Store) Document(documentID uuid.UUID) (Document, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.documents[documentID]
	return document, ok
}

// Track returns a stored Track row.
func (s *Store) Track(trackID string) (release.Track, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	track, ok := s.tracks[trackID]
	return track, ok
}

// TrackCriteriaSet returns the Member criteria of a Track's download policy.
func (s *Store) TrackCriteriaSet(trackID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.criteriaSet[trackID])
}

// StoredTrackCredits returns the stored Track credits in order.
func (s *Store) StoredTrackCredits(trackID string) []release.TrackCredit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.trackCredits[trackID])
}

// Artwork returns the Release's artwork File and its asset binding.
func (s *Store) Artwork(releaseID string) (fileID, boundFileID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.artwork[releaseID], s.bindings[releaseID]
}

// Updates are the content.updated signals the Store scheduled.
func (s *Store) Updates() []release.ContentUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.updates)
}

// OGRequests and OGCancels are the OG work the Store requested and
// cancelled.
func (s *Store) OGRequests() []OGRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ogRequests)
}

func (s *Store) OGCancels() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ogCancels)
}

// ShareLinkDeletions counts the share link deletions of a Release.
func (s *Store) ShareLinkDeletions(releaseID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shareLinks[releaseID]
}

// LockedMembers lists the Members the Store locked, in order.
func (s *Store) LockedMembers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.lockedMembers)
}

// ReleaseRows.

func (s *Store) FindRelease(_ context.Context, releaseID string) (release.Release, error) {
	if err := s.fail("FindRelease"); err != nil {
		return release.Release{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.releases[releaseID]
	if !ok {
		return release.Release{}, release.ErrReleaseNotFound
	}
	return value, nil
}

func (s *Store) LockRelease(ctx context.Context, releaseID string, _ release.RowLock) (release.Release, error) {
	if err := s.fail("LockRelease"); err != nil {
		return release.Release{}, err
	}
	return s.FindRelease(ctx, releaseID)
}

func (s *Store) ReleaseExists(ctx context.Context, releaseID string) error {
	_, err := s.FindRelease(ctx, releaseID)
	return err
}

// ListReleases implements release.Store. The fake knows no filter or sort:
// it lists every Release newest first, then by ID.
func (s *Store) ListReleases(_ context.Context, page release.ListPage) ([]release.Release, int64, error) {
	if err := s.fail("ListReleases"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values := slices.Collect(maps.Values(s.releases))
	slices.SortFunc(values, func(a, b release.Release) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	total := int64(len(values))
	start := min(int(page.Offset), len(values))
	end := min(start+int(page.Limit), len(values))
	return slices.Clone(values[start:end]), total, nil
}

func (s *Store) SlugTaken(_ context.Context, slug string, excludeID *string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range s.releases {
		if value.Slug != nil && *value.Slug == slug && (excludeID == nil || value.ID != *excludeID) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) slugHeldByOther(slug *string, releaseID string) bool {
	if slug == nil {
		return false
	}
	for _, value := range s.releases {
		if value.ID != releaseID && value.Slug != nil && *value.Slug == *slug {
			return true
		}
	}
	return false
}

func (s *Store) InsertRelease(_ context.Context, value *release.Release) error {
	if err := s.fail("InsertRelease"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slugHeldByOther(value.Slug, "") {
		return release.ErrSlugTaken
	}
	if value.ID == "" {
		value.ID = uuid.NewString()
	}
	now := s.now()
	value.CreatedAt, value.UpdatedAt = now, now
	if value.SourceLocale == "" {
		value.SourceLocale = "en"
	}
	s.releases[value.ID] = *value
	return nil
}

func (s *Store) UpdateReleaseSettings(_ context.Context, releaseID string, settings release.Settings, now time.Time) error {
	if err := s.fail("UpdateReleaseSettings"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.releases[releaseID]
	if !ok {
		return release.ErrReleaseNotFound
	}
	if s.slugHeldByOther(settings.Slug, releaseID) {
		return release.ErrSlugTaken
	}
	if settings.Slug != nil {
		value.Slug = new(*settings.Slug)
	}
	if settings.Type != nil {
		value.Type = *settings.Type
	}
	if settings.ReleaseDateSet {
		value.ReleaseDate = settings.ReleaseDate
	}
	for _, link := range []struct {
		target **string
		value  *string
	}{
		{&value.SpotifyURL, settings.SpotifyURL}, {&value.AppleMusicURL, settings.AppleMusicURL},
		{&value.BandcampURL, settings.BandcampURL}, {&value.YoutubeMusicURL, settings.YoutubeMusicURL},
		{&value.CatalogNumber, settings.CatalogNumber},
	} {
		if link.value != nil {
			*link.target = new(*link.value)
		}
	}
	value.UpdatedAt = now
	s.releases[releaseID] = value
	return nil
}

func (s *Store) SetReleaseStatus(_ context.Context, releaseID string, status release.Status, publishedAt *time.Time, now time.Time) error {
	if err := s.fail("SetReleaseStatus"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.releases[releaseID]
	if !ok {
		return nil
	}
	value.Status, value.UpdatedAt = status, now
	if publishedAt != nil {
		value.PublishedAt = new(*publishedAt)
	}
	s.releases[releaseID] = value
	return nil
}

func (s *Store) TouchRelease(_ context.Context, releaseID string, now time.Time) error {
	if err := s.fail("TouchRelease"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.releases[releaseID]
	if !ok {
		return release.ErrReleaseNotFound
	}
	value.UpdatedAt = now
	s.releases[releaseID] = value
	return nil
}

// DeleteRelease implements release.Store, with the database cascade of the
// Release's locales, relations, artwork and Tracks.
func (s *Store) DeleteRelease(_ context.Context, releaseID string) error {
	if err := s.fail("DeleteRelease"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.releases[releaseID]; !ok {
		return release.ErrReleaseNotFound
	}
	delete(s.releases, releaseID)
	for key := range s.locales {
		if key.releaseID == releaseID {
			delete(s.locales, key)
			delete(s.notes, key)
		}
	}
	delete(s.artists, releaseID)
	delete(s.labels, releaseID)
	delete(s.formats, releaseID)
	delete(s.credits, releaseID)
	for _, byRelease := range s.references {
		delete(byRelease, releaseID)
	}
	delete(s.artwork, releaseID)
	for id, track := range s.tracks {
		if track.ReleaseID == releaseID {
			delete(s.tracks, id)
			delete(s.trackCredits, id)
			delete(s.criteriaSet, id)
		}
	}
	return nil
}

func (s *Store) ContentDocumentID(_ context.Context, releaseID string, _ release.RowLock) (*uuid.UUID, error) {
	if err := s.fail("ContentDocumentID"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.releases[releaseID]
	if !ok {
		return nil, release.ErrReleaseNotFound
	}
	if value.ContentDocumentID == nil || *value.ContentDocumentID == "" {
		return nil, nil
	}
	documentID, err := uuid.Parse(*value.ContentDocumentID)
	if err != nil {
		return nil, fmt.Errorf("invalid release content_document_id: %w", err)
	}
	return &documentID, nil
}

func (s *Store) SourceLocale(_ context.Context, releaseID string, _ release.RowLock) (string, error) {
	if err := s.fail("SourceLocale"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.releases[releaseID]
	if !ok {
		return "", release.ErrReleaseNotFound
	}
	return value.SourceLocale, nil
}

func (s *Store) DocumentRoot(ctx context.Context, releaseID string, lock release.RowLock) (release.DocumentRoot, error) {
	if err := s.fail("DocumentRoot"); err != nil {
		return release.DocumentRoot{}, err
	}
	documentID, err := s.ContentDocumentID(ctx, releaseID, lock)
	if err != nil {
		return release.DocumentRoot{}, err
	}
	value, _ := s.Release(releaseID)
	return release.DocumentRoot{ID: value.ID, Status: value.Status, SourceLocale: value.SourceLocale, DocumentID: documentID}, nil
}

func (s *Store) routeTaken(slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Routes[slug] {
		return domainerr.New(domainerr.AlreadyExists, "", fmt.Sprintf("release with slug '%s' already exists", slug))
	}
	return nil
}

func (s *Store) CheckResourceRoute(_ context.Context, slug string) error { return s.routeTaken(slug) }

func (s *Store) LockResourceRoute(_ context.Context, slug string) error { return s.routeTaken(slug) }

func (s *Store) ResourceRouteAvailable(_ context.Context, slug string) (bool, error) {
	return s.routeTaken(slug) == nil, nil
}

func (s *Store) DeleteShareLinks(_ context.Context, releaseID string) error {
	if err := s.fail("DeleteShareLinks"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shareLinks[releaseID]++
	return nil
}

func (s *Store) CountTracksPerRelease(_ context.Context, releaseIDs []string) (map[string]int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int32{}
	for _, track := range s.tracks {
		if slices.Contains(releaseIDs, track.ReleaseID) {
			counts[track.ReleaseID]++
		}
	}
	return counts, nil
}

func (s *Store) CountCreditsPerRelease(_ context.Context, releaseIDs []string) (map[string]int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int32{}
	for _, releaseID := range releaseIDs {
		if count := len(s.credits[releaseID]); count != 0 {
			counts[releaseID] = int32(count)
		}
	}
	return counts, nil
}

func (s *Store) PublishContentUpdate(_ context.Context, update release.ContentUpdate) error {
	if err := s.fail("PublishContentUpdate"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, update)
	return nil
}

func (s *Store) RequestOG(_ context.Context, releaseID, reason string) error {
	if err := s.fail("RequestOG"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ogRequests = append(s.ogRequests, OGRequest{ReleaseID: releaseID, Reason: reason})
	return nil
}

// ReleaseLocales.

func (s *Store) LocaleMetadata(_ context.Context, releaseID, locale string, _ bool) (release.LocaleMetadata, bool, error) {
	if err := s.fail("LocaleMetadata"); err != nil {
		return release.LocaleMetadata{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.locales[localeKey{releaseID, locale}]
	return value, ok, nil
}

func (s *Store) LocaleTitle(_ context.Context, releaseID, locale string) (*string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.locales[localeKey{releaseID, locale}]
	if !ok {
		return nil, release.ErrLocaleNotFound
	}
	return value.Title, nil
}

func (s *Store) SaveSourceLocaleTitle(_ context.Context, releaseID, locale, title string, now time.Time) error {
	if err := s.fail("SaveSourceLocaleTitle"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locales[localeKey{releaseID, locale}] = release.LocaleMetadata{Locale: locale, Title: new(title), UpdatedAt: now}
	return nil
}

func (s *Store) InsertLocale(_ context.Context, releaseID, locale string, title *string, now time.Time) error {
	if err := s.fail("InsertLocale"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{releaseID, locale}
	if _, exists := s.locales[key]; exists {
		return fmt.Errorf("release_translation %s/%s already exists", releaseID, locale)
	}
	s.locales[key] = release.LocaleMetadata{Locale: locale, Title: cloneString(title), UpdatedAt: now}
	return nil
}

func (s *Store) UpdateLocaleTitle(_ context.Context, releaseID, locale string, title *string, updatedAt time.Time) (bool, error) {
	if err := s.fail("UpdateLocaleTitle"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{releaseID, locale}
	value, ok := s.locales[key]
	if !ok {
		return false, nil
	}
	value.Title, value.UpdatedAt = cloneString(title), updatedAt
	s.locales[key] = value
	return true, nil
}

func (s *Store) DeleteLocale(_ context.Context, releaseID, locale string) (bool, error) {
	if err := s.fail("DeleteLocale"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{releaseID, locale}
	delete(s.notes, key)
	_, ok := s.locales[key]
	delete(s.locales, key)
	return ok, nil
}

// CreditNotes implements release.Store: only notes of the Release's
// current credits.
func (s *Store) CreditNotes(_ context.Context, releaseID, locale string) (map[string]string, error) {
	if err := s.fail("CreditNotes"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	notes := map[string]string{}
	for creditID, note := range s.notes[localeKey{releaseID, locale}] {
		if s.hasCredit(releaseID, creditID) {
			notes[creditID] = note
		}
	}
	return notes, nil
}

func (s *Store) hasCredit(releaseID, creditID string) bool {
	return slices.ContainsFunc(s.credits[releaseID], func(row creditRow) bool { return row.ID == creditID })
}

func (s *Store) ReplaceCreditNotes(_ context.Context, releaseID, locale string, notes map[string]string, _ time.Time) error {
	if err := s.fail("ReplaceCreditNotes"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := make(map[string]string, len(notes))
	for creditID, note := range notes {
		if !s.hasCredit(releaseID, creditID) {
			return fmt.Errorf("release_credit_locale references credit %s outside the Release", creditID)
		}
		stored[creditID] = strings.TrimSpace(note)
	}
	s.notes[localeKey{releaseID, locale}] = stored
	return nil
}

func (s *Store) TranslationDefaultLocale(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.defaultLocale, s.translationErr
}

// ReleaseRelations.

func (s *Store) ReleaseArtists(_ context.Context, releaseID string) ([]release.ReleaseArtist, error) {
	if err := s.fail("ReleaseArtists"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := slices.Clone(s.artists[releaseID])
	slices.SortStableFunc(rows, func(a, b artistRow) int {
		if a.SortOrder != b.SortOrder {
			return a.SortOrder - b.SortOrder
		}
		return strings.Compare(a.ArtistID, b.ArtistID)
	})
	return artistsOf(rows), nil
}

func (s *Store) ReleaseArtistsByCreation(_ context.Context, releaseID string) ([]release.ReleaseArtist, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := slices.Clone(s.artists[releaseID])
	slices.SortStableFunc(rows, func(a, b artistRow) int {
		if a.SortOrder != b.SortOrder {
			return a.SortOrder - b.SortOrder
		}
		return a.created - b.created
	})
	return artistsOf(rows), nil
}

func artistsOf(rows []artistRow) []release.ReleaseArtist {
	artists := make([]release.ReleaseArtist, len(rows))
	for i, row := range rows {
		artists[i] = row.ReleaseArtist
	}
	return artists
}

func (s *Store) ReplaceReleaseArtists(_ context.Context, releaseID string, _, next []release.ReleaseArtist) error {
	if err := s.fail("ReplaceReleaseArtists"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	created := map[string]int{}
	for _, row := range s.artists[releaseID] {
		created[row.ArtistID] = row.created
	}
	rows := make([]artistRow, 0, len(next))
	for _, artist := range next {
		order, exists := created[artist.ArtistID]
		if !exists {
			order = s.next()
		}
		rows = append(rows, artistRow{ReleaseArtist: artist, created: order})
	}
	s.artists[releaseID] = rows
	return nil
}

func (s *Store) ReleaseLabels(_ context.Context, releaseID string) ([]release.ReleaseLabel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	labels := slices.Clone(s.labels[releaseID])
	slices.SortStableFunc(labels, func(a, b release.ReleaseLabel) int {
		if a.SortOrder != b.SortOrder {
			return a.SortOrder - b.SortOrder
		}
		return strings.Compare(a.LabelID, b.LabelID)
	})
	return labels, nil
}

func (s *Store) ReplaceReleaseLabels(_ context.Context, releaseID string, _, next []release.ReleaseLabel) error {
	if err := s.fail("ReplaceReleaseLabels"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.labels[releaseID] = slices.Clone(next)
	return nil
}

func (s *Store) ReleaseFormats(_ context.Context, releaseID string) ([]release.ReleaseFormat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	formats := slices.Clone(s.formats[releaseID])
	slices.SortFunc(formats, func(a, b release.ReleaseFormat) int { return strings.Compare(a.FormatID, b.FormatID) })
	return formats, nil
}

func (s *Store) ReplaceReleaseFormats(_ context.Context, releaseID string, _, next []release.ReleaseFormat) error {
	if err := s.fail("ReplaceReleaseFormats"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.formats[releaseID] = slices.Clone(next)
	return nil
}

func (s *Store) ReleaseCredits(_ context.Context, releaseID string) ([]release.ReleaseCredit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := slices.Clone(s.credits[releaseID])
	slices.SortStableFunc(rows, func(a, b creditRow) int {
		if a.SortOrder != b.SortOrder {
			return a.SortOrder - b.SortOrder
		}
		return strings.Compare(a.ID, b.ID)
	})
	credits := make([]release.ReleaseCredit, len(rows))
	for i, row := range rows {
		credits[i] = row.ReleaseCredit
	}
	return credits, nil
}

func (s *Store) ReplaceReleaseCredits(_ context.Context, releaseID string, _, next []release.ReleaseCredit) error {
	if err := s.fail("ReplaceReleaseCredits"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	created := map[string]int{}
	for _, row := range s.credits[releaseID] {
		created[row.ID] = row.created
	}
	rows := make([]creditRow, 0, len(next))
	for _, credit := range next {
		order, exists := created[credit.ID]
		if !exists {
			order = s.next()
		}
		rows = append(rows, creditRow{ReleaseCredit: credit, created: order})
	}
	s.credits[releaseID] = rows
	for key, notes := range s.notes {
		if key.releaseID == releaseID {
			maps.DeleteFunc(notes, func(creditID, _ string) bool { return !s.hasCredit(releaseID, creditID) })
		}
	}
	return nil
}

func (s *Store) CreditIDs(_ context.Context, releaseID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.credits[releaseID]))
	for _, row := range s.credits[releaseID] {
		ids = append(ids, row.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *Store) ReferenceIDs(_ context.Context, relation release.ReferenceRelation, releaseID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.references[relation][releaseID]), nil
}

func (s *Store) ReplaceReferences(_ context.Context, relation release.ReferenceRelation, releaseID string, removed, added []string) error {
	if err := s.fail("ReplaceReferences"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.references[relation] == nil {
		s.references[relation] = map[string][]string{}
	}
	ids := slices.DeleteFunc(slices.Clone(s.references[relation][releaseID]), func(id string) bool { return slices.Contains(removed, id) })
	for _, id := range added {
		if slices.Contains(ids, id) {
			return fmt.Errorf("release reference %s already linked", id)
		}
		ids = append(ids, id)
	}
	s.references[relation][releaseID] = ids
	return nil
}

func (s *Store) References(_ context.Context, relation release.ReferenceRelation, releaseID string) ([]release.Reference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	references := make([]release.Reference, 0, len(s.references[relation][releaseID]))
	for _, id := range s.references[relation][releaseID] {
		if reference, ok := s.Catalog[id]; ok {
			references = append(references, reference)
		}
	}
	slices.SortFunc(references, func(a, b release.Reference) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return references, nil
}

func (s *Store) Formats(_ context.Context, releaseID string) ([]release.FormatReference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	formats := make([]release.FormatReference, 0, len(s.formats[releaseID]))
	for _, format := range s.formats[releaseID] {
		if reference, ok := s.Catalog[format.FormatID]; ok {
			formats = append(formats, release.FormatReference{Reference: reference, Description: format.FormatDescription})
		}
	}
	slices.SortFunc(formats, func(a, b release.FormatReference) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return formats, nil
}

func (s *Store) EditorCredits(_ context.Context, releaseID string) ([]release.EditorCredit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := slices.Clone(s.credits[releaseID])
	slices.SortStableFunc(rows, func(a, b creditRow) int {
		if a.SortOrder != b.SortOrder {
			return a.SortOrder - b.SortOrder
		}
		return a.created - b.created
	})
	credits := make([]release.EditorCredit, len(rows))
	for i, row := range rows {
		credits[i] = release.EditorCredit{
			ID: row.ID, ArtistID: row.ArtistID, MemberID: row.MemberID, CreditedName: row.CreditedName,
			CreditRole: row.CreditRole, SortOrder: int32(row.SortOrder),
		}
		if row.MemberID != nil {
			if member, ok := s.Members[*row.MemberID]; ok {
				credits[i].MemberName = new(member.Nickname)
			}
		}
	}
	return credits, nil
}

func (s *Store) ArtistSummaries(_ context.Context, artistIDs []string) (map[string]release.ArtistSummary, error) {
	if err := s.fail("ArtistSummaries"); err != nil {
		return nil, err
	}
	return s.summaries(s.Artists, artistIDs), nil
}

func (s *Store) LabelSummaries(_ context.Context, labelIDs []string) (map[string]release.LabelSummary, error) {
	return s.summaries(s.Labels, labelIDs), nil
}

func (s *Store) summaries(stored map[string]release.Summary, ids []string) map[string]release.Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]release.Summary, len(ids))
	for _, id := range ids {
		if summary, ok := stored[id]; ok {
			result[id] = summary
		}
	}
	return result
}

// LockMemberReferences implements release.Store: each credited Member in
// Member order, answering one that is no live Member as the shared
// reference check does.
func (s *Store) LockMemberReferences(_ context.Context, references []release.MemberReference) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fields := map[string]string{}
	for _, reference := range references {
		if _, seen := fields[reference.MemberID]; !seen {
			fields[reference.MemberID] = reference.Field
		}
	}
	for _, memberID := range slices.Sorted(maps.Keys(fields)) {
		if _, err := uuid.Parse(memberID); err != nil {
			return domainerr.InvalidField(fields[memberID], "must be a canonical UUID")
		}
		if member, ok := s.Members[memberID]; !ok || member.Deleted {
			return domainerr.InvalidField(fields[memberID], "not found: "+memberID)
		}
		s.lockedMembers = append(s.lockedMembers, memberID)
	}
	return nil
}

// TrackRows.

func (s *Store) Tracks(_ context.Context, releaseID string) ([]release.Track, error) {
	if err := s.fail("Tracks"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var tracks []release.Track
	for _, track := range s.tracks {
		if track.ReleaseID == releaseID {
			tracks = append(tracks, track)
		}
	}
	slices.SortFunc(tracks, func(a, b release.Track) int { return a.TrackNumber - b.TrackNumber })
	return tracks, nil
}

func (s *Store) FindTrack(_ context.Context, trackID string) (release.Track, error) {
	if err := s.fail("FindTrack"); err != nil {
		return release.Track{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	track, ok := s.tracks[trackID]
	if !ok {
		return release.Track{}, release.ErrTrackNotFound
	}
	return track, nil
}

func (s *Store) LockTrack(ctx context.Context, trackID string) (release.Track, error) {
	if err := s.fail("LockTrack"); err != nil {
		return release.Track{}, err
	}
	return s.FindTrack(ctx, trackID)
}

func (s *Store) LockTrackID(ctx context.Context, trackID string) error {
	_, err := s.FindTrack(ctx, trackID)
	return err
}

func (s *Store) LockTrackIDs(_ context.Context, releaseID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, track := range s.tracks {
		if track.ReleaseID == releaseID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *Store) LockTrackDownloadCriteriaSet(context.Context, string) error {
	return s.fail("LockTrackDownloadCriteriaSet")
}

func (s *Store) NextTrackNumber(_ context.Context, releaseID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	highest := 0
	for _, track := range s.tracks {
		if track.ReleaseID == releaseID {
			highest = max(highest, track.TrackNumber)
		}
	}
	return highest + 1, nil
}

func (s *Store) InsertTrack(_ context.Context, track *release.Track) error {
	if err := s.fail("InsertTrack"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.tracks {
		if existing.ReleaseID == track.ReleaseID && existing.TrackNumber == track.TrackNumber {
			return fmt.Errorf("track number %d already taken", track.TrackNumber)
		}
	}
	if track.ID == "" {
		track.ID = uuid.NewString()
	}
	if track.DownloadAccess == "" {
		track.DownloadAccess = release.DownloadAccessDisabled
	}
	if track.Metadata == nil {
		track.Metadata = map[string]any{}
	}
	track.CreatedAt = s.now()
	s.tracks[track.ID] = *track
	return nil
}

func (s *Store) SaveTrack(_ context.Context, track release.Track) error {
	if err := s.fail("SaveTrack"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tracks[track.ID] = track
	return nil
}

func (s *Store) DisableTrackDownload(_ context.Context, trackID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if track, ok := s.tracks[trackID]; ok {
		track.DownloadAccess = release.DownloadAccessDisabled
		s.tracks[trackID] = track
	}
	delete(s.criteriaSet, trackID)
	return nil
}

func (s *Store) AttachTrackAudio(_ context.Context, trackID string, audio release.TrackAudio) error {
	if err := s.fail("AttachTrackAudio"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	track, ok := s.tracks[trackID]
	if !ok {
		return nil
	}
	track.AudioOriginalFileID = new(audio.FileID)
	track.ProcessingStatus = new(string(release.TrackProcessingProcessing))
	track.DownloadAccess = release.DownloadAccessDisabled
	if audio.Completed {
		track.ProcessingStatus = new(string(release.TrackProcessingCompleted))
		track.DurationSeconds = audio.DurationSeconds
	}
	s.tracks[trackID] = track
	delete(s.criteriaSet, trackID)
	return nil
}

func (s *Store) DeleteTrack(_ context.Context, trackID string) error {
	if err := s.fail("DeleteTrack"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tracks[trackID]; !ok {
		return release.ErrTrackNotFound
	}
	delete(s.tracks, trackID)
	delete(s.trackCredits, trackID)
	delete(s.criteriaSet, trackID)
	return nil
}

func (s *Store) CountTracksOf(_ context.Context, releaseID string, trackIDs []string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, id := range slices.Compact(slices.Sorted(slices.Values(trackIDs))) {
		if track, ok := s.tracks[id]; ok && track.ReleaseID == releaseID {
			count++
		}
	}
	return count, nil
}

func (s *Store) CountReleaseTracks(ctx context.Context, releaseID string) (int64, error) {
	tracks, err := s.Tracks(ctx, releaseID)
	return int64(len(tracks)), err
}

func (s *Store) TrackOrder(ctx context.Context, releaseID string) ([]string, error) {
	tracks, err := s.Tracks(ctx, releaseID)
	ids := make([]string, len(tracks))
	for i, track := range tracks {
		ids[i] = track.ID
	}
	return ids, err
}

func (s *Store) RenumberTracks(_ context.Context, releaseID string, trackIDs []string) error {
	if err := s.fail("RenumberTracks"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, id := range trackIDs {
		if track, ok := s.tracks[id]; ok && track.ReleaseID == releaseID {
			track.TrackNumber = i + 1
			s.tracks[id] = track
		}
	}
	return nil
}

func (s *Store) HasTrackUploadSessions(_ context.Context, trackIDs []string) (bool, error) {
	if s.OnUploadCheck != nil {
		s.OnUploadCheck()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.ContainsFunc(trackIDs, func(id string) bool { return s.uploads[id] }), nil
}

func (s *Store) RequireNoTrackUploadSessions(_ context.Context, trackID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.uploads[trackID] {
		return release.ErrTrackUploadSessionsChanged
	}
	return nil
}

func (s *Store) TrackCredits(_ context.Context, trackID string) ([]release.TrackCredit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	credits := slices.Clone(s.trackCredits[trackID])
	slices.SortStableFunc(credits, func(a, b release.TrackCredit) int {
		if a.SortOrder != b.SortOrder {
			return a.SortOrder - b.SortOrder
		}
		return strings.Compare(a.ID, b.ID)
	})
	return credits, nil
}

func (s *Store) TrackCreditRows(_ context.Context, trackID string) ([]release.CreditRow, error) {
	if err := s.fail("TrackCreditRows"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	credits := slices.Clone(s.trackCredits[trackID])
	slices.SortStableFunc(credits, func(a, b release.TrackCredit) int {
		if a.SortOrder != b.SortOrder {
			return a.SortOrder - b.SortOrder
		}
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	rows := make([]release.CreditRow, len(credits))
	for i, credit := range credits {
		kind := "text"
		switch {
		case credit.ArtistID != nil:
			kind = "artist"
		case credit.MemberID != nil:
			kind = "member"
		}
		rows[i] = release.CreditRow{
			ID: credit.ID, CreditType: kind, ArtistID: credit.ArtistID, MemberID: credit.MemberID,
			CreditedName: credit.CreditedName, CreditRole: credit.CreditRole, SortOrder: credit.SortOrder,
		}
	}
	return rows, nil
}

// ReplaceTrackCredits implements release.Store, reporting removed credits
// first, then changed and added credits in their new order.
func (s *Store) ReplaceTrackCredits(_ context.Context, current, next []release.TrackCredit) ([]release.TrackCreditChange, error) {
	if err := s.fail("ReplaceTrackCredits"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	currentByID := map[string]release.TrackCredit{}
	for _, credit := range current {
		currentByID[credit.ID] = credit
	}
	kept := map[string]bool{}
	for _, credit := range next {
		kept[credit.ID] = credit.ID != ""
	}
	var changes []release.TrackCreditChange
	for _, credit := range current {
		if !kept[credit.ID] {
			changes = append(changes, release.TrackCreditChange{CreditID: credit.ID, Kind: release.ChangeDeleted})
		}
	}
	trackID := ""
	switch {
	case len(next) != 0:
		trackID = next[0].TrackID
	case len(current) != 0:
		trackID = current[0].TrackID
	}
	stored := make([]release.TrackCredit, 0, len(next))
	for _, credit := range next {
		previous, exists := currentByID[credit.ID]
		if exists && credit.ID != "" {
			if !sameTrackCredit(previous, credit) {
				changes = append(changes, release.TrackCreditChange{CreditID: credit.ID, Kind: release.ChangeUpdated})
			}
			credit.CreatedAt = previous.CreatedAt
		} else {
			if credit.ID == "" {
				credit.ID = uuid.NewString()
			}
			credit.CreatedAt = s.now()
			changes = append(changes, release.TrackCreditChange{CreditID: credit.ID, Kind: release.ChangeCreated})
		}
		stored = append(stored, credit)
	}
	if trackID != "" {
		s.trackCredits[trackID] = stored
	}
	return changes, nil
}

func sameTrackCredit(left, right release.TrackCredit) bool {
	return sameString(left.CreditRole, right.CreditRole) && left.SortOrder == right.SortOrder &&
		sameString(left.ArtistID, right.ArtistID) && sameString(left.MemberID, right.MemberID) &&
		sameString(left.CreditedName, right.CreditedName)
}

func sameString(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	return new(*value)
}

// ReleaseMedia.

func (s *Store) FileID(_ context.Context, fileID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.files[fileID]
	if !ok {
		return "", release.ErrFileNotFound
	}
	return stored, nil
}

func (s *Store) ArtworkFile(_ context.Context, releaseID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fileID, ok := s.artwork[releaseID]
	return fileID, ok, nil
}

func (s *Store) SetArtworkFile(_ context.Context, releaseID, fileID string) error {
	if err := s.fail("SetArtworkFile"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.artwork[releaseID] = fileID
	return nil
}

func (s *Store) ClearArtworkFiles(_ context.Context, releaseID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.artwork, releaseID)
	return nil
}

func (s *Store) LockAttachableFiles(_ context.Context, fileIDs []string) error {
	if err := s.fail("LockAttachableFiles"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lockedFiles = append(s.lockedFiles, fileIDs...)
	return nil
}

func (s *Store) BindArtwork(_ context.Context, fileID, releaseID string) (*mediaasset.Reference, error) {
	if err := s.fail("BindArtwork"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings[releaseID] = fileID
	return s.ReadyAssets[fileID], nil
}

func (s *Store) ReleaseArtworkBinding(_ context.Context, releaseID string) error {
	if err := s.fail("ReleaseArtworkBinding"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bindings, releaseID)
	return nil
}

func (s *Store) CancelOG(_ context.Context, releaseID string) error {
	if err := s.fail("CancelOG"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ogCancels = append(s.ogCancels, releaseID)
	return nil
}

func (s *Store) ReadyArtwork(_ context.Context, releaseID string) (*mediaasset.Reference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fileID, ok := s.artwork[releaseID]
	if !ok {
		return nil, nil
	}
	return s.ReadyAssets[fileID], nil
}

func (s *Store) ReadyArtworks(_ context.Context, releaseIDs []string) (map[string]*mediaasset.Reference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := map[string]*mediaasset.Reference{}
	for _, releaseID := range releaseIDs {
		if fileID, ok := s.artwork[releaseID]; ok {
			if asset, ready := s.ReadyAssets[fileID]; ready {
				result[releaseID] = asset
			}
		}
	}
	return result, nil
}

// ReleaseCollaboration.

func (s *Store) ResolveSessionPrincipal(_ context.Context, sessionID string) (*auth.UserInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	principal, ok := s.Sessions[sessionID]
	if !ok {
		return nil, auth.ErrSessionPrincipalInvalid
	}
	return principal, nil
}

func (s *Store) RequireCurrentContributors(context.Context, string, []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.contributorsErr
}

func (s *Store) LockMembers(_ context.Context, memberIDs []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var locked []string
	for _, id := range memberIDs {
		if _, ok := s.Members[id]; ok {
			locked = append(locked, id)
			s.lockedMembers = append(s.lockedMembers, id)
		}
	}
	return locked, nil
}

func (s *Store) MemberPreferredLocale(_ context.Context, memberID string) (*string, error) {
	if err := s.fail("MemberPreferredLocale"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	member, ok := s.Members[memberID]
	if !ok || member.Deleted {
		return nil, nil
	}
	return cloneString(member.PreferredLocale), nil
}
