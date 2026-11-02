// Package posttest holds the in-memory post.Store the Post use-case tests
// run on, the authorizer they check with, and the Store contract the fake
// shares with postpostgres. The fake knows no SQL and takes no lock; it
// keeps the sentinels, orderings and row counts the PostgreSQL store answers
// with, and a minimal content document whose revision advances like the
// content store's.
package posttest

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/google/uuid"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Document is a fake content document: its revision, profile and Blocks,
// and the locales a batch wrote.
type Document struct {
	ID       uuid.UUID
	Profile  string
	Revision uuid.UUID
	Blocks   []contentblock.BaseBlock
	Locales  map[string]bool
}

// Member is a Member a participant, comment or collaboration read sees.
type Member struct {
	IdentityID      string
	Nickname        string
	PreferredLocale *string
	// Active is a live, onboarded Member whose Identity is active and
	// unbanned.
	Active bool
}

// OGRequest is one OG generation the Store started.
type OGRequest struct {
	PostID, Locale, Reason string
	Force                  bool
}

// MemberAudit is a record appended through AppendMemberAudit.
type MemberAudit struct {
	MemberID string
	Action   sharedtelemetry.AuditAction
	Record   sharedtelemetry.AuditRecord
}

type localeKey struct{ postID, locale string }

type localeRow struct {
	post.LocaleMetadata
	OgAssetID *string
	CreatedAt time.Time
}

type relationKey struct {
	postID, memberID string
	role             post.ParticipantRole
}

type state struct {
	posts         map[string]post.Post
	locales       map[localeKey]localeRow
	taxonomy      map[post.Taxonomy]map[string]bool
	links         map[post.Taxonomy]map[string][]string
	relations     map[relationKey]time.Time
	members       map[string]Member
	comments      map[string]post.Comment
	versions      map[string][]post.StoredVersion
	documents     map[uuid.UUID]Document
	mapPlaces     map[string]bool
	files         map[string]bool
	routes        map[string]bool
	readyAssets   map[string]*mediaasset.Reference
	blockMedia    map[uuid.UUID][]*fileref.BlockMediaItem
	sessions      map[string]*auth.UserInfo
	updates       []post.ContentUpdate
	ogRequests    []OGRequest
	ogCancels     []string
	shareLinks    []string
	lockedFiles   []string
	memberAudits  []MemberAudit
	sequence      int
	unavailable   map[uuid.UUID]contentv1.MissingAttachmentMediaKind
	contentResult []contentblock.Result
}

func (s state) clone() state {
	clone := s
	clone.posts = mutationtest.ClonePersisted(s.posts)
	clone.locales = mutationtest.ClonePersisted(s.locales)
	clone.taxonomy = mutationtest.ClonePersisted(s.taxonomy)
	clone.links = mutationtest.ClonePersisted(s.links)
	clone.relations = mutationtest.ClonePersisted(s.relations)
	clone.members = mutationtest.ClonePersisted(s.members)
	clone.comments = mutationtest.ClonePersisted(s.comments)
	clone.versions = mutationtest.ClonePersisted(s.versions)
	clone.documents = mutationtest.ClonePersisted(s.documents)
	clone.mapPlaces = mutationtest.ClonePersisted(s.mapPlaces)
	clone.files = mutationtest.ClonePersisted(s.files)
	clone.routes = mutationtest.ClonePersisted(s.routes)
	clone.readyAssets = mutationtest.ClonePersisted(s.readyAssets)
	clone.blockMedia = mutationtest.ClonePersisted(s.blockMedia)
	clone.sessions = mutationtest.ClonePersisted(s.sessions)
	clone.updates = mutationtest.ClonePersisted(s.updates)
	clone.ogRequests = mutationtest.ClonePersisted(s.ogRequests)
	clone.ogCancels = mutationtest.ClonePersisted(s.ogCancels)
	clone.shareLinks = mutationtest.ClonePersisted(s.shareLinks)
	clone.lockedFiles = slices.Clone(s.lockedFiles)
	clone.memberAudits = mutationtest.ClonePersisted(s.memberAudits)
	clone.unavailable = mutationtest.ClonePersisted(s.unavailable)
	clone.contentResult = slices.Clone(s.contentResult)
	return clone
}

// Store is the in-memory post.Store. Seed it with the Put methods; read it
// back with the getters.
type Store struct {
	mu sync.Mutex
	state
	// Now is the clock of the rows it stamps; zero is time.Now.
	Now func() time.Time
	// DefaultLocale is the translation runtime's default locale; empty is
	// "en".
	DefaultLocale string
	// OGRunID is the run ID RequestOG returns; empty starts none.
	OGRunID string
	// Fail fails the Store method of that name with its error.
	Fail map[string]error
}

var _ post.Store = (*Store)(nil)

// NewStore returns an empty fake Store.
func NewStore() *Store {
	return &Store{state: state{
		posts: map[string]post.Post{}, locales: map[localeKey]localeRow{},
		taxonomy:  map[post.Taxonomy]map[string]bool{post.Categories: {}, post.Tags: {}},
		links:     map[post.Taxonomy]map[string][]string{post.Categories: {}, post.Tags: {}},
		relations: map[relationKey]time.Time{}, members: map[string]Member{}, comments: map[string]post.Comment{},
		versions: map[string][]post.StoredVersion{}, documents: map[uuid.UUID]Document{},
		mapPlaces: map[string]bool{}, files: map[string]bool{}, routes: map[string]bool{},
		readyAssets: map[string]*mediaasset.Reference{}, blockMedia: map[uuid.UUID][]*fileref.BlockMediaItem{},
		sessions: map[string]*auth.UserInfo{}, unavailable: map[uuid.UUID]contentv1.MissingAttachmentMediaKind{},
	}}
}

// New returns a fake Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[post.Store]) {
	store := NewStore()
	return store, mutationtest.New[post.Store](store)
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	saved := s.clone()
	s.mu.Unlock()
	return mutationtest.Checkpoint{
		State: map[string]any{
			"posts":        saved.posts,
			"locales":      saved.locales,
			"taxonomy":     saved.taxonomy,
			"links":        saved.links,
			"relations":    saved.relations,
			"members":      saved.members,
			"comments":     saved.comments,
			"versions":     saved.versions,
			"documents":    saved.documents,
			"mapPlaces":    saved.mapPlaces,
			"files":        saved.files,
			"routes":       saved.routes,
			"readyAssets":  saved.readyAssets,
			"blockMedia":   saved.blockMedia,
			"sessions":     saved.sessions,
			"updates":      saved.updates,
			"ogRequests":   saved.ogRequests,
			"ogCancels":    saved.ogCancels,
			"shareLinks":   saved.shareLinks,
			"memberAudits": saved.memberAudits,
			"unavailable":  saved.unavailable,
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

// nextID is a fresh UUID.
func (s *Store) nextID() string {
	return uuid.NewString()
}

func text(value string) *string { return &value }

// PutPost stores a Post with an empty content document in its source
// locale, a source locale row of title, and an Author when authorID is
// set. It returns the stored Post.
func (s *Store) PutPost(stored post.Post, title string, authorID string) post.Post {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored.ID == "" {
		stored.ID = s.nextID()
	}
	if stored.SourceLocale == "" {
		stored.SourceLocale = "en"
	}
	if stored.Status == "" {
		stored.Status = post.StatusDraft
	}
	if stored.ConfigurationRevision == "" {
		stored.ConfigurationRevision = uuid.NewString()
	}
	if stored.DocumentLayout == (post.DocumentLayout{}) {
		stored.DocumentLayout = post.DefaultDocumentLayout()
	}
	if stored.CreatedAt.IsZero() {
		stored.CreatedAt = s.now()
		stored.UpdatedAt = stored.CreatedAt
	}
	if stored.ContentDocumentID == nil {
		documentID := uuid.New()
		s.documents[documentID] = Document{ID: documentID, Profile: "post", Revision: uuid.New()}
		stored.ContentDocumentID = text(documentID.String())
	}
	s.posts[stored.ID] = stored
	s.locales[localeKey{stored.ID, stored.SourceLocale}] = localeRow{
		LocaleMetadata: post.LocaleMetadata{Locale: stored.SourceLocale, Title: text(title), UpdatedAt: stored.CreatedAt},
		CreatedAt:      stored.CreatedAt,
	}
	if authorID != "" {
		s.relations[relationKey{stored.ID, authorID, post.RoleAuthor}] = stored.CreatedAt
	}
	return stored
}

// PutLocale stores a locale row of a Post.
func (s *Store) PutLocale(postID string, metadata post.LocaleMetadata) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if metadata.UpdatedAt.IsZero() {
		metadata.UpdatedAt = s.now()
	}
	s.locales[localeKey{postID, metadata.Locale}] = localeRow{LocaleMetadata: metadata, CreatedAt: metadata.UpdatedAt}
}

// PutMember stores a Member.
func (s *Store) PutMember(memberID string, member Member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[memberID] = member
}

// PutParticipant gives memberID role on a Post.
func (s *Store) PutParticipant(postID, memberID string, role post.ParticipantRole, createdAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.relations[relationKey{postID, memberID, role}] = createdAt
}

// PutTaxonomy stores existing Categories or Tags.
func (s *Store) PutTaxonomy(taxonomy post.Taxonomy, ids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		s.taxonomy[taxonomy][id] = true
	}
}

// PutComment stores a comment and returns it with its ID.
func (s *Store) PutComment(comment post.Comment) post.Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	if comment.ID == "" {
		comment.ID = s.nextID()
	}
	if comment.CreatedAt.IsZero() {
		s.sequence++
		comment.CreatedAt = s.now().Add(time.Duration(s.sequence) * time.Millisecond)
		comment.UpdatedAt = comment.CreatedAt
	}
	s.comments[comment.ID] = comment
	return comment
}

// PutVersion stores a version of a Post.
func (s *Store) PutVersion(version post.StoredVersion) post.StoredVersion {
	s.mu.Lock()
	defer s.mu.Unlock()
	if version.ID == "" {
		version.ID = s.nextID()
	}
	if version.CreatedAt.IsZero() {
		version.CreatedAt = s.now()
	}
	s.versions[version.PostID] = append(s.versions[version.PostID], version)
	return version
}

// PutMapPlace, PutFile and PutRoute store what a Post may reference or
// collide with: a Map Place, a File and a Page route slug.
func (s *Store) PutMapPlace(id string) { s.mu.Lock(); s.mapPlaces[id] = true; s.mu.Unlock() }
func (s *Store) PutFile(id string)     { s.mu.Lock(); s.files[id] = true; s.mu.Unlock() }
func (s *Store) PutRoute(slug string)  { s.mu.Lock(); s.routes[slug] = true; s.mu.Unlock() }

// PutReadyAsset stores a ready public asset.
func (s *Store) PutReadyAsset(id string, ref *mediaasset.Reference) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readyAssets[id] = ref
}

// PutSession resolves sessionID to principal.
func (s *Store) PutSession(sessionID string, principal *auth.UserInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = principal
}

// PutBlockMedia stores the File attachments of a content document.
func (s *Store) PutBlockMedia(documentID uuid.UUID, items []*fileref.BlockMediaItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockMedia[documentID] = items
}

// NextContentResults queues the results the next content batches answer
// with, in order, instead of the fake's own.
func (s *Store) NextContentResults(results ...contentblock.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contentResult = append(s.contentResult, results...)
}

// Post reads a stored Post.
func (s *Store) Post(postID string) (post.Post, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.posts[postID]
	return stored, ok
}

// Locale reads a stored locale row.
func (s *Store) Locale(postID, locale string) (post.LocaleMetadata, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.locales[localeKey{postID, locale}]
	return row.LocaleMetadata, ok
}

// Participant reports when memberID took role on a Post.
func (s *Store) Participant(postID, memberID string, role post.ParticipantRole) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	createdAt, ok := s.relations[relationKey{postID, memberID, role}]
	return createdAt, ok
}

// Links lists a Post's Category or Tag IDs.
func (s *Store) Links(taxonomy post.Taxonomy, postID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.links[taxonomy][postID])
}

// Comment reads a stored comment.
func (s *Store) Comment(commentID string) (post.Comment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	comment, ok := s.comments[commentID]
	return comment, ok
}

// Versions lists a Post's versions in insertion order.
func (s *Store) Versions(postID string) []post.StoredVersion {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.versions[postID])
}

// Document reads a content document.
func (s *Store) Document(documentID uuid.UUID) (Document, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.documents[documentID]
	return document, ok
}

// Updates are the content.updated signals the Store published.
func (s *Store) Updates() []post.ContentUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.updates)
}

// OGRequests are the OG generations the Store started; OGCancels the
// Posts whose OG image it cancelled; ShareLinkDeletions the Posts whose
// share links it removed; LockedFiles the Files it locked.
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

func (s *Store) ShareLinkDeletions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.shareLinks)
}

func (s *Store) LockedFiles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.lockedFiles)
}

// MemberAudits are the records appended through AppendMemberAudit.
func (s *Store) MemberAudits() []MemberAudit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.memberAudits)
}

func (s *Store) PublishContentUpdate(_ context.Context, update post.ContentUpdate) error {
	if err := s.fail("PublishContentUpdate"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, update)
	return nil
}

func (s *Store) AppendMemberAudit(ctx context.Context, writer domainaudit.Appender, memberID string, action sharedtelemetry.AuditAction, build domainaudit.Builder) error {
	if err := domainaudit.AppendMember(ctx, nil, writer, memberID, action, build); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.memberAudits = append(s.memberAudits, MemberAudit{MemberID: memberID, Action: action})
	return nil
}

// lookup reads a stored Post or answers post.ErrPostNotFound.
func (s *Store) lookup(postID string) (post.Post, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.posts[postID]
	if !ok {
		return post.Post{}, post.ErrPostNotFound
	}
	return stored, nil
}

// withRelations is stored with its Categories and Tags, as a preloading
// read returns it.
func (s *Store) withRelations(stored post.Post) post.Post {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored.Categories, stored.Tags = nil, nil
	for _, id := range s.links[post.Categories][stored.ID] {
		stored.Categories = append(stored.Categories, post.Category{ID: id, Name: id, Slug: id})
	}
	for _, id := range s.links[post.Tags][stored.ID] {
		stored.Tags = append(stored.Tags, post.Tag{ID: id, Name: id, Slug: id})
	}
	return stored
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func trimmed(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}
