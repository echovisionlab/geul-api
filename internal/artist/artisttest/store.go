// Package artisttest holds the in-memory artist.Store the Artist use-case
// tests run on, and the Store contract it shares with artistpostgres. The
// fake knows no SQL and takes no lock; it keeps the sentinels, orderings
// and row counts the PostgreSQL store answers with, and a minimal content
// document whose revision advances like the content store's.
package artisttest

import (
	"context"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// Document is a fake content document: its revision, profile and source
// locale, and the locales its Blocks carry.
type Document struct {
	ID           uuid.UUID
	Profile      string
	Revision     uuid.UUID
	SourceLocale string
	Blocks       []contentblock.BaseBlock
	Locales      map[string]bool
}

// OGRequest is one OG generation the Store started.
type OGRequest struct {
	ArtistID      string
	Locale        string
	SourceChanged bool
	Reason        string
}

// Relations are the rows of other domains that reference an Artist, by
// kind. Child Artists come from the rows.
type Relations struct {
	ProgramEvents []entityparticipant.Related
	Releases      []entityparticipant.Related
	Tracks        []entityparticipant.Related
	Works         []entityparticipant.Related
}

type participantKey struct{ entityID, memberID, role string }

type localeKey struct{ artistID, locale string }

type state struct {
	artists        map[string]artist.Artist
	participants   map[participantKey]time.Time
	locales        map[localeKey]artist.LocaleTitle
	localeOgAssets map[localeKey]*string
	documents      map[uuid.UUID]Document
	routes         map[string]bool
	labelLinks     map[string][]string
	labels         map[string]string
	images         map[string][]string
	relations      map[string]Relations
	releaseCounts  map[string]int32
	updates        []artist.ContentUpdate
	ogRequests     []OGRequest
	ogCancels      []string
	shareLinks     []string
	releasedImages []string
	parentLocks    int
	identities     map[string]string
	members        map[string]bool
	sessions       map[string]*auth.UserInfo
	revokedRoom    map[string]bool
	readyAssets    map[string]*mediaasset.Reference
	readyImages    map[string]*mediaasset.Reference
	contentResults []contentblock.Result
}

func (s state) clone() state {
	clone := s
	clone.artists = mutationtest.ClonePersisted(s.artists)
	clone.participants = mutationtest.ClonePersisted(s.participants)
	clone.locales = mutationtest.ClonePersisted(s.locales)
	clone.localeOgAssets = mutationtest.ClonePersisted(s.localeOgAssets)
	clone.documents = mutationtest.ClonePersisted(s.documents)
	clone.routes = mutationtest.ClonePersisted(s.routes)
	clone.labelLinks = mutationtest.ClonePersisted(s.labelLinks)
	clone.labels = mutationtest.ClonePersisted(s.labels)
	clone.images = mutationtest.ClonePersisted(s.images)
	clone.relations = mutationtest.ClonePersisted(s.relations)
	clone.releaseCounts = mutationtest.ClonePersisted(s.releaseCounts)
	clone.updates = mutationtest.ClonePersisted(s.updates)
	clone.ogRequests = mutationtest.ClonePersisted(s.ogRequests)
	clone.ogCancels = mutationtest.ClonePersisted(s.ogCancels)
	clone.shareLinks = mutationtest.ClonePersisted(s.shareLinks)
	clone.releasedImages = mutationtest.ClonePersisted(s.releasedImages)
	clone.identities = mutationtest.ClonePersisted(s.identities)
	clone.members = mutationtest.ClonePersisted(s.members)
	clone.sessions = mutationtest.ClonePersisted(s.sessions)
	clone.revokedRoom = mutationtest.ClonePersisted(s.revokedRoom)
	clone.readyAssets = mutationtest.ClonePersisted(s.readyAssets)
	clone.readyImages = mutationtest.ClonePersisted(s.readyImages)
	clone.contentResults = slices.Clone(s.contentResults)
	return clone
}

// Store is the in-memory artist.Store. Seed it with the Put methods; read
// it back with the getters.
type Store struct {
	mu sync.Mutex
	state
	// Now is the clock of the rows it stamps; zero is time.Now.
	Now func() time.Time
	// InitialSourceLocale is the source locale of a new Artist; empty is
	// "en".
	InitialSourceLocale string
	// OGRunID is the run ID RequestOG returns.
	OGRunID string
	// Fail fails the Store method of that name with its error.
	Fail map[string]error
}

// NewStore returns an empty fake Store.
func NewStore() *Store {
	return &Store{state: state{
		artists: map[string]artist.Artist{}, participants: map[participantKey]time.Time{},
		locales: map[localeKey]artist.LocaleTitle{}, localeOgAssets: map[localeKey]*string{},
		documents: map[uuid.UUID]Document{}, routes: map[string]bool{}, labelLinks: map[string][]string{},
		labels: map[string]string{}, images: map[string][]string{},
		relations: map[string]Relations{}, releaseCounts: map[string]int32{},
		identities: map[string]string{}, members: map[string]bool{}, sessions: map[string]*auth.UserInfo{},
		revokedRoom: map[string]bool{}, readyAssets: map[string]*mediaasset.Reference{},
		readyImages: map[string]*mediaasset.Reference{},
	}}
}

// New returns a fake Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[artist.Store]) {
	store := NewStore()
	return store, mutationtest.New[artist.Store](store)
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	saved := s.clone()
	s.mu.Unlock()
	return mutationtest.Checkpoint{
		State: map[string]any{
			"artists":        saved.artists,
			"participants":   saved.participants,
			"locales":        saved.locales,
			"localeOgAssets": saved.localeOgAssets,
			"documents":      saved.documents,
			"routes":         saved.routes,
			"labelLinks":     saved.labelLinks,
			"labels":         saved.labels,
			"images":         saved.images,
			"relations":      saved.relations,
			"releaseCounts":  saved.releaseCounts,
			"updates":        saved.updates,
			"ogRequests":     saved.ogRequests,
			"ogCancels":      saved.ogCancels,
			"shareLinks":     saved.shareLinks,
			"releasedImages": saved.releasedImages,
			"identities":     saved.identities,
			"members":        saved.members,
			"sessions":       saved.sessions,
			"revokedRoom":    saved.revokedRoom,
			"readyAssets":    saved.readyAssets,
			"readyImages":    saved.readyImages,
		},
		Restore: func() {
			s.mu.Lock()
			s.state = saved
			s.mu.Unlock()
		},
	}
}

var _ artist.Store = (*Store)(nil)

func (s *Store) fail(method string) error {
	if s.Fail == nil {
		return nil
	}
	return s.Fail[method]
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

// PutArtist stores an Artist row, filling its ID, source locale, status
// and created_at when unset.
func (s *Store) PutArtist(value artist.Artist) artist.Artist {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value.ID == "" {
		value.ID = uuid.NewString()
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = s.now()
	}
	if value.SourceLocale == "" {
		value.SourceLocale = "en"
	}
	if value.Status == "" {
		value.Status = artist.StatusDraft
	}
	s.artists[value.ID] = value
	return value
}

// PutDocument stores a content document and points artistID at it.
func (s *Store) PutDocument(artistID string, document Document) Document {
	s.mu.Lock()
	defer s.mu.Unlock()
	if document.ID == uuid.Nil {
		document.ID = uuid.New()
	}
	if document.Revision == uuid.Nil {
		document.Revision = uuid.New()
	}
	if document.Profile == "" {
		document.Profile = "compact"
	}
	value := s.artists[artistID]
	if document.SourceLocale == "" {
		document.SourceLocale = value.SourceLocale
	}
	id := document.ID.String()
	value.ContentDocumentID = &id
	s.artists[artistID] = value
	s.documents[document.ID] = document
	return document
}

// PutLocale stores an Artist locale row.
func (s *Store) PutLocale(artistID string, locale artist.LocaleTitle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locales[localeKey{artistID, locale.Locale}] = locale
}

// PutLocaleOgAsset gives an Artist locale its OG image.
func (s *Store) PutLocaleOgAsset(artistID, locale, assetID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.localeOgAssets[localeKey{artistID, locale}] = &assetID
}

// PutParticipant gives memberID role on entityID.
func (s *Store) PutParticipant(entityID, memberID, role string, createdAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.participants[participantKey{entityID, memberID, role}] = createdAt
}

// PutMember makes memberID an existing Member acting through identityID;
// an empty identityID leaves it without a linked Identity.
func (s *Store) PutMember(memberID, identityID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[memberID] = true
	if identityID != "" {
		s.identities[memberID] = identityID
	}
}

// PutSession resolves sessionID to principal.
func (s *Store) PutSession(sessionID string, principal *auth.UserInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = principal
}

// RevokeRoom makes RequireCurrentContributors reject saves of artistID.
func (s *Store) RevokeRoom(artistID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revokedRoom[artistID] = true
}

// PutRoute occupies slug with a route.
func (s *Store) PutRoute(slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[slug] = true
}

// PutLabel stores a Label the Artist may link, with its source title;
// draft marks it a draft Label.
func (s *Store) PutLabel(labelID, title string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.labels[labelID] = title
}

// LinkLabels links artistID to labelIDs in that order.
func (s *Store) LinkLabels(artistID string, labelIDs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.labelLinks[artistID] = slices.Clone(labelIDs)
}

// PutRelations names the rows of other domains that reference artistID.
func (s *Store) PutRelations(artistID string, relations Relations) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.relations[artistID] = relations
}

// PutReleaseCount sets artistID's Release count.
func (s *Store) PutReleaseCount(artistID string, count int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseCounts[artistID] = count
}

// PutReadyAsset makes assetID a ready public asset.
func (s *Store) PutReadyAsset(assetID string, ref *mediaasset.Reference) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readyAssets[assetID] = ref
}

// PutReadyImage makes the image asset of fileID ready.
func (s *Store) PutReadyImage(fileID string, ref *mediaasset.Reference) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readyImages[fileID] = ref
}

// NextContentResult makes the next content batch answer result, keeping the
// revision it assigns.
func (s *Store) NextContentResult(result contentblock.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contentResults = append(s.contentResults, result)
}

// Artist returns the stored Artist row.
func (s *Store) Artist(artistID string) (artist.Artist, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.artists[artistID]
	return value, ok
}

// Document returns the stored content document.
func (s *Store) Document(documentID uuid.UUID) (Document, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.documents[documentID]
	return document, ok
}

// Locale returns a stored locale row.
func (s *Store) Locale(artistID, locale string) (artist.LocaleTitle, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.locales[localeKey{artistID, locale}]
	return value, ok
}

// Participants maps each participant of entityID to its role.
func (s *Store) Participants(entityID string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	roles := map[string]string{}
	for key := range s.participants {
		if key.entityID == entityID {
			roles[key.memberID] = key.role
		}
	}
	return roles
}

// LinkedLabels returns the Labels artistID links, in order.
func (s *Store) LinkedLabels(artistID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.labelLinks[artistID])
}

// ImageFiles returns the files bound as artistID's images, in order.
func (s *Store) ImageFiles(artistID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.images[artistID])
}

// Updates returns the content.updated signals published so far.
func (s *Store) Updates() []artist.ContentUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.updates)
}

// OGRequests returns the OG generations started so far.
func (s *Store) OGRequests() []OGRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ogRequests)
}

// OGCancels returns the Artists whose OG generation was cancelled.
func (s *Store) OGCancels() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ogCancels)
}

// DeletedShareLinks returns the Artists whose share links were deleted.
func (s *Store) DeletedShareLinks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.shareLinks)
}

// ReleasedImages returns the Artists whose image bindings were released.
func (s *Store) ReleasedImages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.releasedImages)
}

// ParentGraphLocks counts the hierarchy locks taken.
func (s *Store) ParentGraphLocks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parentLocks
}

func (s *Store) row(artistID string) (artist.Artist, error) {
	value, ok := s.artists[artistID]
	if !ok {
		return artist.Artist{}, artist.ErrArtistNotFound
	}
	return value, nil
}

func (s *Store) read(method, artistID string) (artist.Artist, error) {
	if err := s.fail(method); err != nil {
		return artist.Artist{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.row(artistID)
}

func (s *Store) update(method, artistID string, change func(*artist.Artist)) error {
	if err := s.fail(method); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := s.row(artistID)
	if err != nil {
		return err
	}
	change(&value)
	s.artists[artistID] = value
	return nil
}

// FindArtist implements artist.Store.
func (s *Store) FindArtist(_ context.Context, artistID string) (artist.Artist, error) {
	return s.read("FindArtist", artistID)
}

// ShareLockArtist implements artist.Store.
func (s *Store) ShareLockArtist(_ context.Context, artistID string) (artist.Artist, error) {
	return s.read("ShareLockArtist", artistID)
}

// LockArtistID implements artist.Store.
func (s *Store) LockArtistID(_ context.Context, artistID string) (string, error) {
	value, err := s.read("LockArtistID", artistID)
	return value.ID, err
}

// ListArtists implements artist.Store: ordered by ID, applying the
// label_id filter and the "search" filter against the source title.
func (s *Store) ListArtists(_ context.Context, page artist.ListPage) ([]artist.Artist, int64, error) {
	if err := s.fail("ListArtists"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var matched []artist.Artist
	for _, value := range s.artists {
		if page.PublishedOnly && value.Status != artist.StatusPublished {
			continue
		}
		if page.IDs != nil && !slices.Contains(page.IDs, value.ID) {
			continue
		}
		if !s.matches(value, page.Filters) {
			continue
		}
		matched = append(matched, value)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ID < matched[j].ID })
	total := int64(len(matched))
	start := min(int(page.Offset), len(matched))
	end := min(start+int(page.Limit), len(matched))
	return slices.Clone(matched[start:end]), total, nil
}

func (s *Store) matches(value artist.Artist, filters []*commonv1.FilterSpec) bool {
	for _, filter := range filters {
		switch filter.GetField() {
		case "label_id":
			if filter.GetValue() != "" && !slices.Contains(s.labelLinks[value.ID], filter.GetValue()) {
				return false
			}
		case "search":
			title := s.locales[localeKey{value.ID, value.SourceLocale}].Title
			if title == nil || !strings.Contains(strings.ToLower(*title), strings.ToLower(filter.GetValue())) {
				return false
			}
		}
	}
	return true
}

// SlugTaken implements artist.Store.
func (s *Store) SlugTaken(_ context.Context, slug, excludeID string) (bool, error) {
	if err := s.fail("SlugTaken"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, value := range s.artists {
		if id != excludeID && value.Slug != nil && *value.Slug == slug {
			return true, nil
		}
	}
	return false, nil
}

// InsertArtist implements artist.Store.
func (s *Store) InsertArtist(_ context.Context, value *artist.Artist) error {
	if err := s.fail("InsertArtist"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value.ID = uuid.NewString()
	value.CreatedAt = s.now()
	s.artists[value.ID] = *value
	return nil
}

// InsertCreatorOwner implements artist.Store.
func (s *Store) InsertCreatorOwner(_ context.Context, artistID, memberID string, now time.Time) error {
	if err := s.fail("InsertCreatorOwner"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.participants[participantKey{artistID, memberID, entityparticipant.RoleOwner}] = now
	return nil
}

// SetArtistStatus implements artist.Store.
func (s *Store) SetArtistStatus(_ context.Context, artistID string, status artist.Status, publishedAt *time.Time, now time.Time) error {
	return s.update("SetArtistStatus", artistID, func(value *artist.Artist) {
		stamp := now
		value.Status, value.UpdatedAt = status, &stamp
		if publishedAt != nil {
			published := *publishedAt
			value.PublishedAt = &published
		}
	})
}

// TouchArtist implements artist.Store.
func (s *Store) TouchArtist(_ context.Context, artistID string, now time.Time) error {
	return s.update("TouchArtist", artistID, func(value *artist.Artist) {
		stamp := now
		value.UpdatedAt = &stamp
	})
}

// StampArtist implements artist.Store; a missing row stamps nothing.
func (s *Store) StampArtist(ctx context.Context, artistID string, now time.Time) error {
	if err := s.fail("StampArtist"); err != nil {
		return err
	}
	s.mu.Lock()
	_, ok := s.artists[artistID]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return s.TouchArtist(ctx, artistID, now)
}

// DeleteArtist implements artist.Store; the child Artists lose their
// parent and the Artist's own rows go with it, as the foreign keys do.
func (s *Store) DeleteArtist(_ context.Context, artistID string) error {
	if err := s.fail("DeleteArtist"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.row(artistID); err != nil {
		return err
	}
	delete(s.artists, artistID)
	delete(s.labelLinks, artistID)
	delete(s.images, artistID)
	for key := range s.locales {
		if key.artistID == artistID {
			delete(s.locales, key)
		}
	}
	for key := range s.participants {
		if key.entityID == artistID {
			delete(s.participants, key)
		}
	}
	for id, value := range s.artists {
		if value.ParentArtistID != nil && *value.ParentArtistID == artistID {
			value.ParentArtistID = nil
			s.artists[id] = value
		}
	}
	return nil
}

// ContentDocumentID implements artist.Store.
func (s *Store) ContentDocumentID(_ context.Context, artistID string, _ artist.RowLock) (*string, error) {
	value, err := s.read("ContentDocumentID", artistID)
	if err != nil || value.ContentDocumentID == nil {
		return nil, err
	}
	id := *value.ContentDocumentID
	return &id, nil
}

// SourceLocale implements artist.Store.
func (s *Store) SourceLocale(_ context.Context, artistID string, _ artist.RowLock) (string, error) {
	value, err := s.read("SourceLocale", artistID)
	return value.SourceLocale, err
}

// DocumentRoot implements artist.Store.
func (s *Store) DocumentRoot(_ context.Context, artistID string, _ artist.RowLock) (artist.DocumentRoot, error) {
	value, err := s.read("DocumentRoot", artistID)
	if err != nil {
		return artist.DocumentRoot{}, err
	}
	root := artist.DocumentRoot{ID: value.ID, Status: value.Status}
	if value.ContentDocumentID != nil {
		id, err := uuid.Parse(*value.ContentDocumentID)
		if err != nil {
			return artist.DocumentRoot{}, err
		}
		root.DocumentID = &id
	}
	return root, nil
}

// DocumentMetadata implements artist.Store.
func (s *Store) DocumentMetadata(_ context.Context, artistID string) (artist.DocumentMetadata, error) {
	value, err := s.read("DocumentMetadata", artistID)
	if err != nil {
		return artist.DocumentMetadata{}, err
	}
	return artist.DocumentMetadata{
		RealName: value.RealName, CountryCode: value.CountryCode, Website: value.Website,
		SocialLinks: value.SocialLinks, Slug: value.Slug,
	}, nil
}

// UpdateDocumentMetadata implements artist.Store.
func (s *Store) UpdateDocumentMetadata(_ context.Context, artistID string, change artist.MetadataChange, now time.Time) error {
	return s.update("UpdateDocumentMetadata", artistID, func(value *artist.Artist) {
		apply := func(target **string, assignment entityparticipant.Assignment) {
			if assignment.Changed {
				*target = assignment.Value
			}
		}
		apply(&value.RealName, change.RealName)
		apply(&value.Slug, change.Slug)
		apply(&value.CountryCode, change.CountryCode)
		apply(&value.Website, change.Website)
		apply(&value.ParentArtistID, change.Parent)
		if change.SetSocialLinks {
			value.SocialLinks = maps.Clone(change.SocialLinks)
		}
		stamp := now
		value.UpdatedAt = &stamp
	})
}

// LabelIDs implements artist.Store.
func (s *Store) LabelIDs(_ context.Context, artistID string) ([]string, error) {
	if err := s.fail("LabelIDs"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	labelIDs := slices.Clone(s.labelLinks[artistID])
	slices.Sort(labelIDs)
	return labelIDs, nil
}

// LabelIDsInLinkOrder implements artist.Store.
func (s *Store) LabelIDsInLinkOrder(_ context.Context, artistID string) ([]string, error) {
	if err := s.fail("LabelIDsInLinkOrder"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.labelLinks[artistID]), nil
}

// ReplaceLabelIDs implements artist.Store.
func (s *Store) ReplaceLabelIDs(_ context.Context, artistID string, labelIDs []string) error {
	if err := s.fail("ReplaceLabelIDs"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.labelLinks[artistID] = slices.Clone(labelIDs)
	return nil
}

// CheckResourceRoute implements artist.Store.
func (s *Store) CheckResourceRoute(_ context.Context, slug string) error {
	if err := s.fail("CheckResourceRoute"); err != nil {
		return err
	}
	return s.requireRoute(slug)
}

// LockResourceRoute implements artist.Store.
func (s *Store) LockResourceRoute(_ context.Context, slug string) error {
	if err := s.fail("LockResourceRoute"); err != nil {
		return err
	}
	return s.requireRoute(slug)
}

func (s *Store) requireRoute(slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.routes[slug] {
		return domainerr.Duplicate("artist", "slug", slug)
	}
	return nil
}

// ResourceRouteAvailable implements artist.Store.
func (s *Store) ResourceRouteAvailable(_ context.Context, slug string) (bool, error) {
	if err := s.fail("ResourceRouteAvailable"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.routes[slug], nil
}

// LocaleTitle implements artist.Store.
func (s *Store) LocaleTitle(_ context.Context, artistID, locale string, _ bool) (artist.LocaleTitle, bool, error) {
	if err := s.fail("LocaleTitle"); err != nil {
		return artist.LocaleTitle{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.locales[localeKey{artistID, locale}]
	return value, ok, nil
}

// LocaleTitleText implements artist.Store.
func (s *Store) LocaleTitleText(ctx context.Context, artistID, locale string) (string, error) {
	value, ok, err := s.LocaleTitle(ctx, artistID, locale, false)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", artist.ErrLocaleNotFound
	}
	if value.Title == nil {
		return "", nil
	}
	return *value.Title, nil
}

// LocaleTitleValue implements artist.Store.
func (s *Store) LocaleTitleValue(ctx context.Context, artistID, locale string) (*string, bool, error) {
	value, ok, err := s.LocaleTitle(ctx, artistID, locale, false)
	return value.Title, ok, err
}

// LocaleExists implements artist.Store.
func (s *Store) LocaleExists(ctx context.Context, artistID, locale string) (bool, error) {
	_, ok, err := s.LocaleTitle(ctx, artistID, locale, false)
	return ok, err
}

// LocaleOgAssetID implements artist.Store.
func (s *Store) LocaleOgAssetID(_ context.Context, artistID, locale string) (*string, error) {
	if err := s.fail("LocaleOgAssetID"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{artistID, locale}
	if _, ok := s.locales[key]; !ok {
		return nil, artist.ErrLocaleNotFound
	}
	return s.localeOgAssets[key], nil
}

// SaveLocaleTitle implements artist.Store.
func (s *Store) SaveLocaleTitle(_ context.Context, artistID, locale, title string, now time.Time) error {
	if err := s.fail("SaveLocaleTitle"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locales[localeKey{artistID, locale}] = artist.LocaleTitle{Locale: locale, Title: &title, UpdatedAt: now}
	return nil
}

// CreateTargetLocale implements artist.Store.
func (s *Store) CreateTargetLocale(_ context.Context, artistID, locale string, now time.Time) (bool, error) {
	if err := s.fail("CreateTargetLocale"); err != nil {
		return false, err
	}
	return s.createLocale(artistID, locale, now), nil
}

// EnsureTargetLocale implements artist.Store.
func (s *Store) EnsureTargetLocale(_ context.Context, artistID, locale string) (bool, error) {
	if err := s.fail("EnsureTargetLocale"); err != nil {
		return false, err
	}
	return s.createLocale(artistID, locale, s.now()), nil
}

func (s *Store) createLocale(artistID, locale string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{artistID, locale}
	if _, exists := s.locales[key]; exists {
		return false
	}
	s.locales[key] = artist.LocaleTitle{Locale: locale, UpdatedAt: now}
	return true
}

// TouchTargetLocale implements artist.Store.
func (s *Store) TouchTargetLocale(_ context.Context, artistID, locale string, updatedAt time.Time) (bool, error) {
	if err := s.fail("TouchTargetLocale"); err != nil {
		return false, err
	}
	return s.changeLocale(artistID, locale, func(value *artist.LocaleTitle) { value.UpdatedAt = updatedAt }), nil
}

// SetTargetLocaleTitle implements artist.Store.
func (s *Store) SetTargetLocaleTitle(_ context.Context, artistID, locale, title string, updatedAt time.Time) (bool, error) {
	if err := s.fail("SetTargetLocaleTitle"); err != nil {
		return false, err
	}
	return s.changeLocale(artistID, locale, func(value *artist.LocaleTitle) {
		value.Title, value.UpdatedAt = &title, updatedAt
	}), nil
}

func (s *Store) changeLocale(artistID, locale string, change func(*artist.LocaleTitle)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{artistID, locale}
	value, exists := s.locales[key]
	if !exists {
		return false
	}
	change(&value)
	s.locales[key] = value
	return true
}

// DeleteTargetLocale implements artist.Store.
func (s *Store) DeleteTargetLocale(_ context.Context, artistID, locale string) (bool, error) {
	if err := s.fail("DeleteTargetLocale"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{artistID, locale}
	if _, exists := s.locales[key]; !exists {
		return false, nil
	}
	delete(s.locales, key)
	return true, nil
}

// Images implements artist.Store: each bound file in order, with its ready
// image asset.
func (s *Store) Images(_ context.Context, artistIDs []string) (map[string][]artist.ArtistImageProjection, error) {
	if err := s.fail("Images"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := map[string][]artist.ArtistImageProjection{}
	for _, artistID := range artistIDs {
		for index, fileID := range s.images[artistID] {
			result[artistID] = append(result[artistID], artist.ArtistImageProjection{
				ArtistID: artistID, FileID: fileID, SortOrder: int32(index), Asset: s.readyImages[fileID],
			})
		}
	}
	return result, nil
}

// ReplaceImageBindings implements artist.Store.
func (s *Store) ReplaceImageBindings(_ context.Context, artistID string, fileIDs []string) error {
	if err := s.fail("ReplaceImageBindings"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.images[artistID] = slices.Clone(fileIDs)
	return nil
}

// ReleaseImageBindings implements artist.Store.
func (s *Store) ReleaseImageBindings(_ context.Context, artistID string) error {
	if err := s.fail("ReleaseImageBindings"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releasedImages = append(s.releasedImages, artistID)
	return nil
}

// ReadyAssets implements artist.Store.
func (s *Store) ReadyAssets(_ context.Context, assetIDs []string) (map[string]*mediaasset.Reference, error) {
	if err := s.fail("ReadyAssets"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	refs := map[string]*mediaasset.Reference{}
	for _, id := range assetIDs {
		if ref := s.readyAssets[id]; ref != nil {
			refs[id] = ref
		}
	}
	return refs, nil
}

// RequestOG implements artist.Store.
func (s *Store) RequestOG(_ context.Context, artistID, locale string, sourceChanged bool, reason string) (string, error) {
	if err := s.fail("RequestOG"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ogRequests = append(s.ogRequests, OGRequest{ArtistID: artistID, Locale: locale, SourceChanged: sourceChanged, Reason: reason})
	return s.OGRunID, nil
}

// CancelOG implements artist.Store.
func (s *Store) CancelOG(_ context.Context, artistID string) error {
	if err := s.fail("CancelOG"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ogCancels = append(s.ogCancels, artistID)
	return nil
}

// ResolveSessionPrincipal implements artist.Store.
func (s *Store) ResolveSessionPrincipal(_ context.Context, sessionID string) (*auth.UserInfo, error) {
	if err := s.fail("ResolveSessionPrincipal"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	principal, ok := s.sessions[sessionID]
	if !ok {
		return nil, auth.ErrSessionPrincipalInvalid
	}
	return principal, nil
}

// RequireCurrentContributors implements artist.Store.
func (s *Store) RequireCurrentContributors(_ context.Context, artistID string, _ []string) error {
	if err := s.fail("RequireCurrentContributors"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revokedRoom[artistID] {
		return domainerr.Denied("collaboration contributors may no longer edit this artist")
	}
	return nil
}

// RequireDocumentContributors implements artist.Store: every contributor
// must be a Member PutMember stored.
func (s *Store) RequireDocumentContributors(_ context.Context, contributors []string) error {
	if err := s.fail("RequireDocumentContributors"); err != nil {
		return err
	}
	if len(contributors) == 0 {
		return domainerr.InvalidField("contributor_member_ids", "collaboration mutation requires contributors")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, contributor := range contributors {
		if !s.members[contributor] {
			return domainerr.InvalidField("contributor_member_ids", "contains a Member that does not exist")
		}
	}
	return nil
}

// ResolveInitialSourceLocale implements artist.Store.
func (s *Store) ResolveInitialSourceLocale(context.Context, string) string {
	if s.InitialSourceLocale != "" {
		return s.InitialSourceLocale
	}
	return "en"
}

// ChildArtists implements artist.Store: the Artists naming artistID their
// parent, by ID, each with its source title.
func (s *Store) ChildArtists(_ context.Context, artistID string) ([]entityparticipant.Related, error) {
	if err := s.fail("ChildArtists"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var children []entityparticipant.Related
	for id, value := range s.artists {
		if value.ParentArtistID != nil && *value.ParentArtistID == artistID {
			title := ""
			if locale, ok := s.locales[localeKey{id, value.SourceLocale}]; ok && locale.Title != nil {
				title = *locale.Title
			}
			children = append(children, entityparticipant.Related{ID: id, DisplayName: title, RelationCount: 1})
		}
	}
	sort.Slice(children, func(i, j int) bool { return children[i].ID < children[j].ID })
	return children, nil
}

func (s *Store) related(method, artistID string, pick func(Relations) []entityparticipant.Related) ([]entityparticipant.Related, error) {
	if err := s.fail(method); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(pick(s.relations[artistID])), nil
}

// ProgramEventsOfArtist implements artist.Store.
func (s *Store) ProgramEventsOfArtist(_ context.Context, artistID string) ([]entityparticipant.Related, error) {
	return s.related("ProgramEventsOfArtist", artistID, func(r Relations) []entityparticipant.Related { return r.ProgramEvents })
}

// ReleasesOfArtist implements artist.Store.
func (s *Store) ReleasesOfArtist(_ context.Context, artistID string) ([]entityparticipant.Related, error) {
	return s.related("ReleasesOfArtist", artistID, func(r Relations) []entityparticipant.Related { return r.Releases })
}

// TracksOfArtist implements artist.Store.
func (s *Store) TracksOfArtist(_ context.Context, artistID string) ([]entityparticipant.Related, error) {
	return s.related("TracksOfArtist", artistID, func(r Relations) []entityparticipant.Related { return r.Tracks })
}

// WorksOfArtist implements artist.Store.
func (s *Store) WorksOfArtist(_ context.Context, artistID string) ([]entityparticipant.Related, error) {
	return s.related("WorksOfArtist", artistID, func(r Relations) []entityparticipant.Related { return r.Works })
}

// LabelTitles implements artist.Store.
func (s *Store) LabelTitles(_ context.Context, labelIDs []string) (map[string]string, error) {
	if err := s.fail("LabelTitles"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	titles := map[string]string{}
	for _, id := range labelIDs {
		if title, ok := s.labels[id]; ok {
			titles[id] = title
		}
	}
	return titles, nil
}

// CountReleasesByArtist implements artist.Store.
func (s *Store) CountReleasesByArtist(_ context.Context, artistIDs []string) (map[string]int32, error) {
	if err := s.fail("CountReleasesByArtist"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int32{}
	for _, id := range artistIDs {
		if count, ok := s.releaseCounts[id]; ok {
			counts[id] = count
		}
	}
	return counts, nil
}

// DeleteShareLinks implements artist.Store.
func (s *Store) DeleteShareLinks(_ context.Context, artistID string) error {
	if err := s.fail("DeleteShareLinks"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shareLinks = append(s.shareLinks, artistID)
	return nil
}

// PublishContentUpdate implements artist.Store.
func (s *Store) PublishContentUpdate(_ context.Context, update artist.ContentUpdate) error {
	if err := s.fail("PublishContentUpdate"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, update)
	return nil
}

// AppendMemberAudit implements artist.Store through the appender, which a
// fake ignores the transaction of.
func (s *Store) AppendMemberAudit(ctx context.Context, writer domainaudit.Appender, memberID string, action sharedtelemetry.AuditAction, build domainaudit.Builder) error {
	return domainaudit.AppendMember(ctx, nil, writer, memberID, action, build)
}
