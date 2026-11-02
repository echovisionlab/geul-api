// Package pagetest holds the in-memory page.Store the Page use-case tests
// run on, and the Store contract it shares with pagepostgres. The fake knows
// no SQL and takes no lock; it keeps the sentinels, orderings and row counts
// the PostgreSQL store answers with, and a minimal content document whose
// revision advances like the content store's.
package pagetest

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// Document is a fake content document: its revision and the locales its
// Blocks carry.
type Document struct {
	ID       uuid.UUID
	Profile  string
	Revision uuid.UUID
	Blocks   []contentblock.BaseBlock
	Locales  map[string]bool
}

// OGRequest is one OG generation the Store started.
type OGRequest struct {
	PageID     string
	Locale     string
	AllLocales bool
	Reason     string
}

// MenuUpdate is one Menu target rewrite the Store made.
type MenuUpdate struct {
	PageID, PreviousSlug, Next string
}

type localeKey struct{ pageID, locale string }

type state struct {
	pages          map[string]page.Page
	locales        map[localeKey]page.Locale
	versions       map[string]page.Version
	documents      map[uuid.UUID]Document
	routes         map[string]bool
	files          map[string]page.PublicationFile
	presentations  page.Presentations
	readyAssets    map[string]*mediaasset.Reference
	unavailable    map[uuid.UUID]contentv1.MissingAttachmentMediaKind
	attachments    map[uuid.UUID][]contentblock.PublicationAttachment
	blockMedia     map[uuid.UUID][]*contentv1.ContentBlockMediaItem
	members        map[string]string
	identities     map[string]string
	preferred      map[string]*string
	tags           map[string]bool
	memberTags     map[string][]string
	subscribers    map[string]bool
	sessions       map[string]*auth.UserInfo
	lockedSessions map[string]bool
	homepage       string
	shareLinks     map[string]*mediaasset.ContentDownloadShareLinkWitness
	updates        []page.ContentUpdate
	ogRequests     []OGRequest
	ogCancels      []string
	shareDeletes   []string
	menuUpdates    []MenuUpdate
	menuRemovals   []string
	released       []string
	lockedFiles    []string
	lockedRoutes   []string
	contentResults []contentblock.Result
}

func (s state) clone() state {
	clone := s
	clone.pages = maps.Clone(s.pages)
	clone.locales = maps.Clone(s.locales)
	clone.versions = maps.Clone(s.versions)
	clone.documents = maps.Clone(s.documents)
	clone.files = maps.Clone(s.files)
	clone.updates = slices.Clone(s.updates)
	clone.ogRequests = slices.Clone(s.ogRequests)
	clone.ogCancels = slices.Clone(s.ogCancels)
	clone.shareDeletes = slices.Clone(s.shareDeletes)
	clone.menuUpdates = slices.Clone(s.menuUpdates)
	clone.menuRemovals = slices.Clone(s.menuRemovals)
	clone.released = slices.Clone(s.released)
	clone.lockedFiles = slices.Clone(s.lockedFiles)
	clone.lockedRoutes = slices.Clone(s.lockedRoutes)
	clone.lockedSessions = maps.Clone(s.lockedSessions)
	return clone
}

// Store is the in-memory page.Store. Seed it with the Put methods; read it
// back with the getters.
type Store struct {
	mu sync.Mutex
	state
	// Fence locks principals for LockActivePrincipal; nil finds every
	// principal active. Tests pass the identitystatetest.Fence they give the
	// runner, so a deactivated principal is inactive in both.
	Fence identitystate.Fence
	// Settings are the localization settings RuntimeSettings answers.
	Settings translation.RuntimeSettings
	// OGRunID is the run ID RequestOG returns.
	OGRunID string
	// Fail fails the Store method of that name with its error.
	Fail map[string]error
	// FailOnce fails the next call of the Store method of that name with
	// its error.
	FailOnce map[string]error
}

// NewStore returns an empty fake Store.
func NewStore() *Store {
	return &Store{
		state: state{
			pages: map[string]page.Page{}, locales: map[localeKey]page.Locale{}, versions: map[string]page.Version{},
			documents: map[uuid.UUID]Document{}, routes: map[string]bool{}, files: map[string]page.PublicationFile{},
			presentations: page.Presentations{SourceAssets: map[page.PresentationKey]bool{}, Derivatives: map[page.PresentationKey]bool{}},
			readyAssets:   map[string]*mediaasset.Reference{}, unavailable: map[uuid.UUID]contentv1.MissingAttachmentMediaKind{},
			attachments: map[uuid.UUID][]contentblock.PublicationAttachment{}, blockMedia: map[uuid.UUID][]*contentv1.ContentBlockMediaItem{},
			members: map[string]string{}, identities: map[string]string{}, preferred: map[string]*string{},
			tags: map[string]bool{}, memberTags: map[string][]string{}, subscribers: map[string]bool{},
			sessions: map[string]*auth.UserInfo{}, lockedSessions: map[string]bool{},
			shareLinks: map[string]*mediaasset.ContentDownloadShareLinkWitness{},
		},
		Settings: translation.DefaultRuntimeSettings(),
	}
}

// New returns a fake Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[page.Store]) {
	store := NewStore()
	return store, mutationtest.New[page.Store](store)
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() func() {
	s.mu.Lock()
	saved := s.clone()
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.state = saved
		s.mu.Unlock()
	}
}

var _ page.Store = (*Store)(nil)

func (s *Store) fail(method string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.FailOnce[method]; ok {
		delete(s.FailOnce, method)
		return err
	}
	if s.Fail == nil {
		return nil
	}
	return s.Fail[method]
}

func now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// PutPage stores a Page row with an empty content document and its
// source-locale title, filling the ID, timestamps, status, layout and
// source locale when unset.
func (s *Store) PutPage(value page.Page, title string) page.Page {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value.ID == "" {
		value.ID = uuid.NewString()
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = now()
	}
	if value.UpdatedAt.IsZero() {
		value.UpdatedAt = value.CreatedAt
	}
	if value.SourceLocale == "" {
		value.SourceLocale = "en"
	}
	if value.Status == "" {
		value.Status = page.StatusDraft
	}
	if value.DocumentLayout == (page.DocumentLayout{}) {
		value.DocumentLayout = page.DefaultDocumentLayout()
	}
	if value.ContentDocumentID == nil {
		document := Document{ID: uuid.New(), Profile: "page", Revision: uuid.New()}
		s.documents[document.ID] = document
		id := document.ID.String()
		value.ContentDocumentID = &id
	}
	s.pages[value.ID] = value
	s.locales[localeKey{value.ID, value.SourceLocale}] = page.Locale{
		Locale: value.SourceLocale, Title: &title, IncarnationID: uuid.New(), UpdatedAt: value.UpdatedAt,
	}
	return value
}

// PutLocale stores a Page locale row, filling its incarnation and time.
func (s *Store) PutLocale(pageID string, locale page.Locale) page.Locale {
	s.mu.Lock()
	defer s.mu.Unlock()
	if locale.IncarnationID == uuid.Nil {
		locale.IncarnationID = uuid.New()
	}
	if locale.UpdatedAt.IsZero() {
		locale.UpdatedAt = now()
	}
	s.locales[localeKey{pageID, locale.Locale}] = locale
	return locale
}

// PutVersion stores a version checkpoint, filling its ID.
func (s *Store) PutVersion(version page.Version) page.Version {
	s.mu.Lock()
	defer s.mu.Unlock()
	if version.ID == "" {
		version.ID = uuid.NewString()
	}
	if version.CreatedAt.IsZero() {
		version.CreatedAt = now()
	}
	s.versions[version.ID] = version
	return version
}

// PutDocumentLocale makes the document carry Blocks in locale.
func (s *Store) PutDocumentLocale(documentID uuid.UUID, locale string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document := s.documents[documentID]
	document.Locales = maps.Clone(document.Locales)
	if document.Locales == nil {
		document.Locales = map[string]bool{}
	}
	document.Locales[locale] = true
	s.documents[documentID] = document
}

// PutRoute occupies slug with another resource's route.
func (s *Store) PutRoute(slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[slug] = true
}

// PutFile stores an attachable File.
func (s *Store) PutFile(file page.PublicationFile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[file.ID] = file
}

// PutPresentation makes the presentation key of a File ready: an asset
// kind when source is set, a derivative type otherwise.
func (s *Store) PutPresentation(key page.PresentationKey, source bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if source {
		s.presentations.SourceAssets[key] = true
	} else {
		s.presentations.Derivatives[key] = true
	}
}

// PutAttachments names the File attachments of a document.
func (s *Store) PutAttachments(documentID uuid.UUID, attachments ...contentblock.PublicationAttachment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attachments[documentID] = attachments
}

// PutBlockMedia names the Block media of a document.
func (s *Store) PutBlockMedia(documentID uuid.UUID, items ...*contentv1.ContentBlockMediaItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockMedia[documentID] = items
}

// PutReadyAsset makes assetID a ready public asset.
func (s *Store) PutReadyAsset(assetID string, ref *mediaasset.Reference) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readyAssets[assetID] = ref
}

// PutMember stores a Member with nickname acting through identityID; an
// empty identityID leaves it without an active Identity.
func (s *Store) PutMember(memberID, nickname, identityID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[memberID] = nickname
	if identityID != "" {
		s.identities[memberID] = identityID
	}
}

// PutPreferredLocale sets a Member's preferred locale.
func (s *Store) PutPreferredLocale(memberID, locale string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preferred[memberID] = &locale
}

// PutUserTag stores a user tag and gives it to memberIDs.
func (s *Store) PutUserTag(tagID string, memberIDs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tags[tagID] = true
	for _, memberID := range memberIDs {
		s.memberTags[memberID] = append(s.memberTags[memberID], tagID)
	}
}

// PutSubscriber subscribes identityID to the newsletter.
func (s *Store) PutSubscriber(identityID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribers[identityID] = true
}

// PutSession resolves sessionID to principal.
func (s *Store) PutSession(sessionID string, principal *auth.UserInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = principal
}

// PutHomepage makes pageID the homepage.
func (s *Store) PutHomepage(pageID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.homepage = pageID
}

// PutShareLink admits token and password for pageID.
func (s *Store) PutShareLink(pageID, token, password string) *mediaasset.ContentDownloadShareLinkWitness {
	s.mu.Lock()
	defer s.mu.Unlock()
	witness := &mediaasset.ContentDownloadShareLinkWitness{ID: uuid.NewString(), EntityType: "page", EntityID: pageID}
	s.shareLinks[pageID+"\x00"+token+"\x00"+password] = witness
	return witness
}

// NextContentResult makes the next content batch answer result, keeping the
// revision it assigns.
func (s *Store) NextContentResult(result contentblock.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contentResults = append(s.contentResults, result)
}

// Page returns the stored Page row.
func (s *Store) Page(pageID string) (page.Page, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.pages[pageID]
	return value, ok
}

// StoredLocale returns a stored locale row.
func (s *Store) StoredLocale(pageID, locale string) (page.Locale, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.locales[localeKey{pageID, locale}]
	return value, ok
}

// Document returns the stored content document.
func (s *Store) Document(documentID uuid.UUID) (Document, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.documents[documentID]
	return document, ok
}

// Versions lists the stored versions of pageID, newest first.
func (s *Store) Versions(pageID string) []page.Version {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pageVersions(pageID)
}

// Updates lists the content.updated signals scheduled.
func (s *Store) Updates() []page.ContentUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.updates)
}

// OGRequests lists the OG generations started.
func (s *Store) OGRequests() []OGRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ogRequests)
}

// OGCancels lists the Pages whose OG generation was cancelled.
func (s *Store) OGCancels() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ogCancels)
}

// ShareLinkDeletes lists the Pages whose share links were deleted.
func (s *Store) ShareLinkDeletes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.shareDeletes)
}

// MenuUpdates lists the Menu target rewrites.
func (s *Store) MenuUpdates() []MenuUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.menuUpdates)
}

// MenuRemovals lists the Pages whose Menu targets were removed.
func (s *Store) MenuRemovals() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.menuRemovals)
}

// Released lists the featured image bindings released, as "page:binding".
func (s *Store) Released() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.released)
}

// LockedSessions reports whether sessionID was locked.
func (s *Store) LockedSession(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lockedSessions[sessionID]
}

func (s *Store) pageVersions(pageID string) []page.Version {
	versions := make([]page.Version, 0)
	for _, version := range s.versions {
		if version.PageID == pageID {
			versions = append(versions, version)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Version > versions[j].Version })
	return versions
}

func (s *Store) page(pageID string) (page.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.pages[pageID]
	if !ok {
		return page.Page{}, page.ErrPageNotFound
	}
	return value, nil
}

func (s *Store) update(pageID string, change func(*page.Page)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.pages[pageID]
	if !ok {
		return nil
	}
	change(&value)
	s.pages[pageID] = value
	return nil
}

// ListPages implements page.Store. The fake applies no filter and orders by
// creation, newest first, then ID.
func (s *Store) ListPages(_ context.Context, list page.ListPage) ([]page.Page, int64, error) {
	if err := s.fail("ListPages"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pages := slices.Collect(maps.Values(s.pages))
	sort.Slice(pages, func(i, j int) bool {
		if !pages[i].CreatedAt.Equal(pages[j].CreatedAt) {
			return pages[i].CreatedAt.After(pages[j].CreatedAt)
		}
		return pages[i].ID < pages[j].ID
	})
	total := int64(len(pages))
	start := min(int(list.Offset), len(pages))
	end := min(start+int(list.Limit), len(pages))
	return slices.Clone(pages[start:end]), total, nil
}

// FindPage implements page.Store.
func (s *Store) FindPage(_ context.Context, locator page.Locator, value string, _ page.RowLock) (page.Page, error) {
	if err := s.fail("FindPage"); err != nil {
		return page.Page{}, err
	}
	if locator == page.ByID {
		return s.page(value)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, stored := range s.pages {
		if stored.Slug != nil && *stored.Slug == value {
			return stored, nil
		}
	}
	return page.Page{}, page.ErrPageNotFound
}

// DocumentRoot implements page.Store.
func (s *Store) DocumentRoot(_ context.Context, pageID string, _ page.RowLock) (page.DocumentRoot, error) {
	if err := s.fail("DocumentRoot"); err != nil {
		return page.DocumentRoot{}, err
	}
	value, err := s.page(pageID)
	if err != nil {
		return page.DocumentRoot{}, err
	}
	root := page.DocumentRoot{ID: value.ID, SourceLocale: value.SourceLocale}
	if value.ContentDocumentID != nil {
		if id, err := uuid.Parse(*value.ContentDocumentID); err == nil {
			root.ContentDocumentID = &id
		}
	}
	return root, nil
}

// AccessPolicy implements page.Store.
func (s *Store) AccessPolicy(_ context.Context, pageID string) (json.RawMessage, error) {
	value, err := s.page(pageID)
	return value.AccessPolicy, err
}

// DocumentLayout implements page.Store.
func (s *Store) DocumentLayout(_ context.Context, pageID string) (page.DocumentLayout, error) {
	value, err := s.page(pageID)
	return value.DocumentLayout, err
}

// LockSettings implements page.Store.
func (s *Store) LockSettings(ctx context.Context, pageID string) (page.Settings, error) {
	if err := s.fail("LockSettings"); err != nil {
		return page.Settings{}, err
	}
	value, err := s.page(pageID)
	if err != nil {
		return page.Settings{}, err
	}
	root, err := s.DocumentRoot(ctx, pageID, page.UpdateLock)
	if err != nil {
		return page.Settings{}, err
	}
	return page.Settings{
		Slug: value.Slug, ShowTitle: value.ShowTitle, ContentDocumentID: root.ContentDocumentID,
		UpdatedAt: value.UpdatedAt, AccessPolicy: value.AccessPolicy,
	}, nil
}

// SlugTaken implements page.Store.
func (s *Store) SlugTaken(_ context.Context, slug, excludeID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, stored := range s.pages {
		if id != excludeID && stored.Slug != nil && *stored.Slug == slug {
			return true, nil
		}
	}
	return false, nil
}

// RouteOccupied implements page.Store.
func (s *Store) RouteOccupied(_ context.Context, slug string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.routes[slug], nil
}

// LockRoute implements page.Store.
func (s *Store) LockRoute(_ context.Context, slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lockedRoutes = append(s.lockedRoutes, slug)
	return nil
}

func (s *Store) slugTakenLocked(slug *string, excludeID string) bool {
	if slug == nil {
		return false
	}
	for id, stored := range s.pages {
		if id != excludeID && stored.Slug != nil && *stored.Slug == *slug {
			return true
		}
	}
	return false
}

// InsertPage implements page.Store.
func (s *Store) InsertPage(_ context.Context, value *page.Page) error {
	if err := s.fail("InsertPage"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slugTakenLocked(value.Slug, "") {
		return page.ErrSlugTaken
	}
	value.ID = uuid.NewString()
	s.pages[value.ID] = *value
	return nil
}

// UpdateSettings implements page.Store.
func (s *Store) UpdateSettings(_ context.Context, pageID string, change page.SettingsChange, at time.Time) error {
	if err := s.fail("UpdateSettings"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if change.SetSlug && s.slugTakenLocked(change.Slug, pageID) {
		return page.ErrSlugTaken
	}
	value := s.pages[pageID]
	if change.SetSlug {
		value.Slug = change.Slug
	}
	if change.ShowTitle != nil {
		value.ShowTitle = *change.ShowTitle
	}
	if change.AccessPolicy != nil {
		value.AccessPolicy = change.AccessPolicy
	}
	value.UpdatedAt = at
	s.pages[pageID] = value
	return nil
}

// SetStatus implements page.Store.
func (s *Store) SetStatus(_ context.Context, pageID string, status page.Status, publishedAt *time.Time, at time.Time) error {
	return s.update(pageID, func(value *page.Page) {
		value.Status, value.UpdatedAt = status, at
		if publishedAt != nil {
			value.PublishedAt = publishedAt
		}
	})
}

// SetFeaturedImage implements page.Store.
func (s *Store) SetFeaturedImage(_ context.Context, pageID string, fileID *string, at time.Time) error {
	if err := s.fail("SetFeaturedImage"); err != nil {
		return err
	}
	return s.update(pageID, func(value *page.Page) { value.FeaturedImageFileID, value.UpdatedAt = fileID, at })
}

// SetDocumentLayout implements page.Store.
func (s *Store) SetDocumentLayout(_ context.Context, pageID string, layout page.DocumentLayout, at time.Time) error {
	return s.update(pageID, func(value *page.Page) { value.DocumentLayout, value.UpdatedAt = layout, at })
}

// SwitchSourceLocale implements page.Store.
func (s *Store) SwitchSourceLocale(_ context.Context, pageID, from, to string, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.pages[pageID]
	if !ok || value.SourceLocale != from {
		return false, nil
	}
	value.SourceLocale, value.UpdatedAt = to, at
	s.pages[pageID] = value
	return true, nil
}

// TouchPage implements page.Store.
func (s *Store) TouchPage(_ context.Context, pageID string, at time.Time) error {
	return s.update(pageID, func(value *page.Page) { value.UpdatedAt = at })
}

// DeletePage implements page.Store. Deleting the row cascades its locales
// and versions.
func (s *Store) DeletePage(_ context.Context, pageID string) error {
	if err := s.fail("DeletePage"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pages, pageID)
	for key := range s.locales {
		if key.pageID == pageID {
			delete(s.locales, key)
		}
	}
	for id, version := range s.versions {
		if version.PageID == pageID {
			delete(s.versions, id)
		}
	}
	return nil
}

// SourceMetadata implements page.Store.
func (s *Store) SourceMetadata(_ context.Context, pageIDs []string) (map[string]page.SourceMetadata, error) {
	if err := s.fail("SourceMetadata"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	states := map[string]page.SourceMetadata{}
	for _, pageID := range pageIDs {
		pageID = strings.TrimSpace(pageID)
		value, ok := s.pages[pageID]
		if !ok {
			continue
		}
		locale, ok := s.locales[localeKey{pageID, value.SourceLocale}]
		if !ok {
			continue
		}
		states[pageID] = page.SourceMetadata{Locale: value.SourceLocale, Title: locale.Title, Summary: locale.Summary}
	}
	return states, nil
}

// Locale implements page.Store.
func (s *Store) Locale(_ context.Context, pageID, locale string, _ page.RowLock) (page.Locale, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.locales[localeKey{pageID, locale}]
	return value, ok, nil
}

// InsertLocale implements page.Store.
func (s *Store) InsertLocale(_ context.Context, pageID, locale string, title, summary *string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locales[localeKey{pageID, locale}] = page.Locale{Locale: locale, Title: title, Summary: summary, IncarnationID: uuid.New(), UpdatedAt: at}
	return nil
}

// CreateTargetLocale implements page.Store.
func (s *Store) CreateTargetLocale(ctx context.Context, pageID, locale string, title, summary *string, at time.Time) (uuid.UUID, error) {
	if err := s.InsertLocale(ctx, pageID, locale, title, summary, at); err != nil {
		return uuid.Nil, err
	}
	stored, _, _ := s.Locale(ctx, pageID, locale, page.Unlocked)
	return stored.IncarnationID, nil
}

// UpsertLocale implements page.Store.
func (s *Store) UpsertLocale(ctx context.Context, pageID, locale string, title, summary *string, at time.Time) error {
	s.mu.Lock()
	stored, ok := s.locales[localeKey{pageID, locale}]
	s.mu.Unlock()
	if !ok {
		return s.InsertLocale(ctx, pageID, locale, title, summary, at)
	}
	stored.Title, stored.Summary, stored.UpdatedAt = title, summary, at
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locales[localeKey{pageID, locale}] = stored
	return nil
}

// UpdateLocale implements page.Store.
func (s *Store) UpdateLocale(_ context.Context, pageID, locale string, change page.LocaleChange) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.locales[localeKey{pageID, locale}]
	if !ok {
		return false, nil
	}
	if change.SetTitle {
		stored.Title = change.Title
	}
	if change.SetSummary {
		stored.Summary = change.Summary
	}
	stored.UpdatedAt = change.UpdatedAt
	s.locales[localeKey{pageID, locale}] = stored
	return true, nil
}

// DeleteLocale implements page.Store.
func (s *Store) DeleteLocale(_ context.Context, pageID, locale string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.locales[localeKey{pageID, locale}]
	delete(s.locales, localeKey{pageID, locale})
	return ok, nil
}

// ListVersions implements page.Store.
func (s *Store) ListVersions(_ context.Context, pageID string, limit, offset int32) ([]page.Version, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	versions := s.pageVersions(pageID)
	start := min(int(offset), len(versions))
	end := min(start+int(limit), len(versions))
	return versions[start:end], int64(len(versions)), nil
}

// FindVersion implements page.Store.
func (s *Store) FindVersion(_ context.Context, versionID string) (page.Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	version, ok := s.versions[versionID]
	if !ok {
		return page.Version{}, page.ErrVersionNotFound
	}
	return version, nil
}

// LockLatestVersion implements page.Store.
func (s *Store) LockLatestVersion(_ context.Context, pageID string) (page.Version, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	versions := s.pageVersions(pageID)
	if len(versions) == 0 {
		return page.Version{}, false, nil
	}
	return versions[0], true, nil
}

// InsertVersion implements page.Store.
func (s *Store) InsertVersion(_ context.Context, version *page.Version) error {
	if err := s.fail("InsertVersion"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	version.ID = uuid.NewString()
	s.versions[version.ID] = *version
	return nil
}

// LockActivePrincipal implements page.Store.
func (s *Store) LockActivePrincipal(ctx context.Context, principal *auth.UserInfo) (bool, error) {
	if err := s.fail("LockActivePrincipal"); err != nil {
		return false, err
	}
	if s.Fence == nil {
		return true, nil
	}
	return s.Fence.LockActivePrincipal(ctx, nil, principal)
}

// HasUserTag implements page.Store.
func (s *Store) HasUserTag(_ context.Context, memberID string, tagIDs []string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tagID := range s.memberTags[memberID] {
		if slices.Contains(tagIDs, tagID) {
			return true, nil
		}
	}
	return false, nil
}

// SubscribesToNewsletter implements page.Store.
func (s *Store) SubscribesToNewsletter(_ context.Context, identityID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subscribers[identityID], nil
}

// ResolveSessionPrincipal implements page.Store.
func (s *Store) ResolveSessionPrincipal(_ context.Context, sessionID string) (*auth.UserInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	principal, ok := s.sessions[sessionID]
	if !ok {
		return nil, auth.ErrSessionPrincipalInvalid
	}
	return principal, nil
}

// LockSession implements page.Store.
func (s *Store) LockSession(_ context.Context, sessionID string, _ *auth.UserInfo) error {
	if err := s.fail("LockSession"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return auth.ErrSessionPrincipalInvalid
	}
	s.lockedSessions[sessionID] = true
	return nil
}

// LockMembers implements page.Store.
func (s *Store) LockMembers(_ context.Context, memberIDs []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	locked := make([]string, 0, len(memberIDs))
	for _, memberID := range memberIDs {
		if _, ok := s.members[memberID]; ok {
			locked = append(locked, memberID)
		}
	}
	return locked, nil
}

// ActiveIdentities implements page.Store.
func (s *Store) ActiveIdentities(_ context.Context, memberIDs []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	identities := map[string]string{}
	for _, memberID := range memberIDs {
		if identityID, ok := s.identities[memberID]; ok {
			identities[memberID] = identityID
		}
	}
	return identities, nil
}

// Nicknames implements page.Store.
func (s *Store) Nicknames(_ context.Context, memberIDs []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	nicknames := map[string]string{}
	for _, memberID := range memberIDs {
		if nickname, ok := s.members[memberID]; ok {
			nicknames[memberID] = nickname
		}
	}
	return nicknames, nil
}

// PreferredLocale implements page.Store.
func (s *Store) PreferredLocale(_ context.Context, memberID string) (*string, error) {
	if err := s.fail("PreferredLocale"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preferred[memberID], nil
}

// LockUserTags implements page.Store.
func (s *Store) LockUserTags(_ context.Context, tagIDs []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	locked := make([]string, 0, len(tagIDs))
	for _, tagID := range tagIDs {
		if s.tags[tagID] {
			locked = append(locked, tagID)
		}
	}
	return locked, nil
}

// DeleteShareLinks implements page.Store.
func (s *Store) DeleteShareLinks(_ context.Context, pageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shareDeletes = append(s.shareDeletes, pageID)
	return nil
}

// UpdateMenuTargets implements page.Store.
func (s *Store) UpdateMenuTargets(_ context.Context, pageID, previousSlug, next string) error {
	if err := s.fail("UpdateMenuTargets"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.menuUpdates = append(s.menuUpdates, MenuUpdate{PageID: pageID, PreviousSlug: previousSlug, Next: next})
	return nil
}

// RemoveMenuTargets implements page.Store.
func (s *Store) RemoveMenuTargets(_ context.Context, pageID, _ string) error {
	if err := s.fail("RemoveMenuTargets"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.menuRemovals = append(s.menuRemovals, pageID)
	return nil
}

// PublishContentUpdate implements page.Store.
func (s *Store) PublishContentUpdate(_ context.Context, update page.ContentUpdate) error {
	if err := s.fail("PublishContentUpdate"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, update)
	return nil
}

// RuntimeSettings implements page.Store.
func (s *Store) RuntimeSettings(context.Context) (translation.RuntimeSettings, error) {
	if err := s.fail("RuntimeSettings"); err != nil {
		return translation.RuntimeSettings{}, err
	}
	return s.Settings, nil
}
