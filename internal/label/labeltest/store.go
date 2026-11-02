// Package labeltest holds the in-memory label.Store the Label use-case
// tests run on, and the Store contract it shares with labelpostgres. The
// fake knows no SQL and takes no lock; it keeps the sentinels, orderings
// and row counts the PostgreSQL store answers with, and a minimal content
// document whose revision advances like the content store's.
package labeltest

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/label"
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
	LabelID string
	Reason  string
}

// Binding is one logo binding.
type Binding struct {
	FileID string
}

type participantKey struct{ entityID, memberID, role string }

type localeKey struct{ labelID, locale string }

type state struct {
	labels         map[string]label.Label
	participants   map[participantKey]time.Time
	locales        map[localeKey]label.LocaleTitle
	documents      map[uuid.UUID]Document
	routes         map[string]bool
	bindings       map[string]map[label.LogoVariant]Binding
	impacts        map[string][]label.DeletionImpact
	artistCounts   map[string]int32
	releaseCounts  map[string]int32
	updates        []label.ContentUpdate
	ogRequests     []OGRequest
	ogCancels      []string
	shareLinks     []string
	lockedFiles    []string
	parentLocks    int
	identities     map[string]string
	members        map[string]bool
	sessions       map[string]*auth.UserInfo
	revokedRoom    map[string]bool
	readyAssets    map[string]*mediaasset.Reference
	readyLogos     map[string]*mediaasset.Reference
	contentResults []contentblock.Result
}

func (s state) clone() state {
	clone := s
	clone.labels = maps.Clone(s.labels)
	clone.participants = maps.Clone(s.participants)
	clone.locales = maps.Clone(s.locales)
	clone.documents = maps.Clone(s.documents)
	clone.routes = maps.Clone(s.routes)
	clone.bindings = make(map[string]map[label.LogoVariant]Binding, len(s.bindings))
	for id, slots := range s.bindings {
		clone.bindings[id] = maps.Clone(slots)
	}
	clone.updates = slices.Clone(s.updates)
	clone.ogRequests = slices.Clone(s.ogRequests)
	clone.ogCancels = slices.Clone(s.ogCancels)
	clone.shareLinks = slices.Clone(s.shareLinks)
	clone.lockedFiles = slices.Clone(s.lockedFiles)
	return clone
}

// Store is the in-memory label.Store. Seed it with the Put and Add
// methods; read it back with the getters.
type Store struct {
	mu sync.Mutex
	state
	// Now is the clock of the participant and locale rows it writes; zero
	// is time.Now.
	Now func() time.Time
	// InitialSourceLocale is the source locale of a new Label; empty is
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
		labels: map[string]label.Label{}, participants: map[participantKey]time.Time{},
		locales: map[localeKey]label.LocaleTitle{}, documents: map[uuid.UUID]Document{},
		routes: map[string]bool{}, bindings: map[string]map[label.LogoVariant]Binding{},
		impacts: map[string][]label.DeletionImpact{}, artistCounts: map[string]int32{},
		releaseCounts: map[string]int32{}, identities: map[string]string{}, members: map[string]bool{},
		sessions: map[string]*auth.UserInfo{}, revokedRoom: map[string]bool{},
		readyAssets: map[string]*mediaasset.Reference{}, readyLogos: map[string]*mediaasset.Reference{},
	}}
}

// New returns a fake Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[label.Store]) {
	store := NewStore()
	return store, mutationtest.New[label.Store](store)
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

var _ label.Store = (*Store)(nil)

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

// PutLabel stores a Label row, filling its ID and timestamps when unset.
func (s *Store) PutLabel(value label.Label) label.Label {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value.ID == "" {
		value.ID = uuid.NewString()
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = s.now()
	}
	if value.UpdatedAt.IsZero() {
		value.UpdatedAt = value.CreatedAt
	}
	if value.SourceLocale == "" {
		value.SourceLocale = "en"
	}
	if value.Status == "" {
		value.Status = label.StatusDraft
	}
	s.labels[value.ID] = value
	return value
}

// PutDocument stores a content document and points labelID at it.
func (s *Store) PutDocument(labelID string, document Document) Document {
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
	value := s.labels[labelID]
	if document.SourceLocale == "" {
		document.SourceLocale = value.SourceLocale
	}
	id := document.ID.String()
	value.ContentDocumentID = &id
	s.labels[labelID] = value
	s.documents[document.ID] = document
	return document
}

// PutLocale stores a Label locale row.
func (s *Store) PutLocale(labelID string, locale label.LocaleTitle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locales[localeKey{labelID, locale.Locale}] = locale
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

// RevokeRoom makes RequireCurrentContributors reject saves of labelID.
func (s *Store) RevokeRoom(labelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revokedRoom[labelID] = true
}

// PutRoute occupies slug with a route.
func (s *Store) PutRoute(slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[slug] = true
}

// PutImpacts names the Artists, Program Events and Releases that reference
// labelID; child Labels come from the rows.
func (s *Store) PutImpacts(labelID string, impacts ...label.DeletionImpact) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.impacts[labelID] = impacts
}

// PutCounts sets labelID's Artist and Release counts.
func (s *Store) PutCounts(labelID string, artists, releases int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.artistCounts[labelID], s.releaseCounts[labelID] = artists, releases
}

// PutReadyAsset makes assetID a ready public asset.
func (s *Store) PutReadyAsset(assetID string, ref *mediaasset.Reference) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readyAssets[assetID] = ref
}

// PutReadyLogo makes the logo asset of fileID ready.
func (s *Store) PutReadyLogo(fileID string, ref *mediaasset.Reference) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readyLogos[fileID] = ref
}

// NextContentResult makes the next content batch answer result, keeping the
// revision it assigns.
func (s *Store) NextContentResult(result contentblock.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contentResults = append(s.contentResults, result)
}

// Label returns the stored Label row.
func (s *Store) Label(labelID string) (label.Label, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.labels[labelID]
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
func (s *Store) Locale(labelID, locale string) (label.LocaleTitle, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.locales[localeKey{labelID, locale}]
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

// Updates returns the content.updated signals published so far.
func (s *Store) Updates() []label.ContentUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.updates)
}

// OGRequests returns the OG generations started so far, OGCancels the
// Labels whose OG image was cancelled.
func (s *Store) OGRequests() []OGRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ogRequests)
}

// OGCancels returns the Labels whose OG generation was cancelled.
func (s *Store) OGCancels() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ogCancels)
}

// Bindings returns labelID's logo bindings.
func (s *Store) Bindings(labelID string) map[label.LogoVariant]Binding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.bindings[labelID])
}

// DeletedShareLinks returns the Labels whose share links were deleted.
func (s *Store) DeletedShareLinks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.shareLinks)
}

// ParentGraphLocks counts the hierarchy locks taken.
func (s *Store) ParentGraphLocks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parentLocks
}

func (s *Store) row(labelID string) (label.Label, error) {
	value, ok := s.labels[labelID]
	if !ok {
		return label.Label{}, label.ErrLabelNotFound
	}
	return value, nil
}

func (s *Store) read(method, labelID string) (label.Label, error) {
	if err := s.fail(method); err != nil {
		return label.Label{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.row(labelID)
}

// FindLabel implements label.Store.
func (s *Store) FindLabel(_ context.Context, labelID string) (label.Label, error) {
	return s.read("FindLabel", labelID)
}

// FindLabelID implements label.Store.
func (s *Store) FindLabelID(_ context.Context, labelID string) (string, error) {
	value, err := s.read("FindLabelID", labelID)
	return value.ID, err
}

// FindLabelLogos implements label.Store.
func (s *Store) FindLabelLogos(_ context.Context, labelID string) (label.Label, error) {
	value, err := s.read("FindLabelLogos", labelID)
	if err != nil {
		return label.Label{}, err
	}
	return label.Label{ID: value.ID, LogoLightFileID: value.LogoLightFileID, LogoDarkFileID: value.LogoDarkFileID}, nil
}

// LockLabel implements label.Store.
func (s *Store) LockLabel(_ context.Context, labelID string) (label.Label, error) {
	return s.read("LockLabel", labelID)
}

// LockLabelID implements label.Store.
func (s *Store) LockLabelID(_ context.Context, labelID string) (string, error) {
	value, err := s.read("LockLabelID", labelID)
	return value.ID, err
}

// ShareLockLabel implements label.Store.
func (s *Store) ShareLockLabel(_ context.Context, labelID string) (label.Label, error) {
	return s.read("ShareLockLabel", labelID)
}

// LockLabelRelations implements label.Store.
func (s *Store) LockLabelRelations(context.Context) error {
	return s.fail("LockLabelRelations")
}

// ListLabels implements label.Store: published-only, ordered by ID; it
// applies only the "search" filter, against the source title.
func (s *Store) ListLabels(_ context.Context, page label.ListPage) ([]label.Label, int64, error) {
	if err := s.fail("ListLabels"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var matched []label.Label
	for _, value := range s.labels {
		if page.PublishedOnly && value.Status != label.StatusPublished {
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

func (s *Store) matches(value label.Label, filters []*commonv1.FilterSpec) bool {
	for _, filter := range filters {
		if filter.GetField() != "search" {
			continue
		}
		title := s.locales[localeKey{value.ID, value.SourceLocale}].Title
		if title == nil || !strings.Contains(strings.ToLower(*title), strings.ToLower(filter.GetValue())) {
			return false
		}
	}
	return true
}

// SlugTaken implements label.Store.
func (s *Store) SlugTaken(_ context.Context, slug, excludeID string) (bool, error) {
	if err := s.fail("SlugTaken"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, value := range s.labels {
		if id != excludeID && value.Slug != nil && *value.Slug == slug {
			return true, nil
		}
	}
	return false, nil
}

// InsertLabel implements label.Store.
func (s *Store) InsertLabel(_ context.Context, value *label.Label) error {
	if err := s.fail("InsertLabel"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if value.Slug != nil {
		for _, other := range s.labels {
			if other.Slug != nil && *other.Slug == *value.Slug {
				return label.ErrSlugTaken
			}
		}
	}
	value.ID = uuid.NewString()
	value.CreatedAt = s.now()
	value.UpdatedAt = value.CreatedAt
	s.labels[value.ID] = *value
	return nil
}

// InsertCreatorOwner implements label.Store.
func (s *Store) InsertCreatorOwner(_ context.Context, labelID, memberID string) error {
	if err := s.fail("InsertCreatorOwner"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.participants[participantKey{labelID, memberID, entityparticipant.RoleOwner}] = s.now()
	return nil
}

func (s *Store) update(method, labelID string, change func(*label.Label)) error {
	if err := s.fail(method); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := s.row(labelID)
	if err != nil {
		return err
	}
	change(&value)
	s.labels[labelID] = value
	return nil
}

// SetLabelStatus implements label.Store.
func (s *Store) SetLabelStatus(_ context.Context, labelID string, status label.Status, publishedAt *time.Time, now time.Time) error {
	return s.update("SetLabelStatus", labelID, func(value *label.Label) {
		value.Status, value.UpdatedAt = status, now
		if publishedAt != nil {
			stamp := *publishedAt
			value.PublishedAt = &stamp
		}
	})
}

// SetLabelLogo implements label.Store.
func (s *Store) SetLabelLogo(_ context.Context, labelID string, variant label.LogoVariant, fileID *string, now time.Time) error {
	return s.update("SetLabelLogo", labelID, func(value *label.Label) {
		var stored *string
		if fileID != nil {
			id := *fileID
			stored = &id
		}
		if variant == label.LogoVariantDark {
			value.LogoDarkFileID = stored
		} else {
			value.LogoLightFileID = stored
		}
		value.UpdatedAt = now
	})
}

// EffectiveLogoFileID implements label.Store.
func (s *Store) EffectiveLogoFileID(_ context.Context, labelID string) (*string, error) {
	if err := s.fail("EffectiveLogoFileID"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	value, ok := s.labels[labelID]
	s.mu.Unlock()
	if !ok {
		// The PostgreSQL scan of a missing row reads no file.
		return nil, nil
	}
	if value.LogoLightFileID != nil {
		return value.LogoLightFileID, nil
	}
	return value.LogoDarkFileID, nil
}

// ClearOgAsset implements label.Store.
func (s *Store) ClearOgAsset(_ context.Context, labelID string) error {
	err := s.update("ClearOgAsset", labelID, func(value *label.Label) { value.OgAssetID = nil })
	if errors.Is(err, label.ErrLabelNotFound) {
		return nil
	}
	return err
}

// TouchLabel implements label.Store.
func (s *Store) TouchLabel(_ context.Context, labelID string, now time.Time) error {
	return s.update("TouchLabel", labelID, func(value *label.Label) { value.UpdatedAt = now })
}

// DeleteLabel implements label.Store.
func (s *Store) DeleteLabel(_ context.Context, labelID string) error {
	if err := s.fail("DeleteLabel"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.row(labelID); err != nil {
		return err
	}
	delete(s.labels, labelID)
	for key := range s.locales {
		if key.labelID == labelID {
			delete(s.locales, key)
		}
	}
	for key := range s.participants {
		if key.entityID == labelID {
			delete(s.participants, key)
		}
	}
	for id, value := range s.labels {
		if value.ParentLabelID != nil && *value.ParentLabelID == labelID {
			value.ParentLabelID = nil
			s.labels[id] = value
		}
	}
	return nil
}

// ContentDocumentID implements label.Store.
func (s *Store) ContentDocumentID(_ context.Context, labelID string, _ label.RowLock) (*uuid.UUID, error) {
	value, err := s.read("ContentDocumentID", labelID)
	if err != nil || value.ContentDocumentID == nil {
		return nil, err
	}
	id, err := uuid.Parse(*value.ContentDocumentID)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// SourceLocale implements label.Store.
func (s *Store) SourceLocale(_ context.Context, labelID string, _ label.RowLock) (string, error) {
	value, err := s.read("SourceLocale", labelID)
	return value.SourceLocale, err
}

// DocumentRoot implements label.Store.
func (s *Store) DocumentRoot(ctx context.Context, labelID string, lock label.RowLock) (label.DocumentRoot, error) {
	value, err := s.read("DocumentRoot", labelID)
	if err != nil {
		return label.DocumentRoot{}, err
	}
	documentID, err := s.ContentDocumentID(ctx, labelID, lock)
	return label.DocumentRoot{ID: value.ID, Status: value.Status, DocumentID: documentID}, err
}

// DocumentMetadata implements label.Store.
func (s *Store) DocumentMetadata(_ context.Context, labelID string) (label.DocumentMetadata, error) {
	value, err := s.read("DocumentMetadata", labelID)
	if err != nil {
		return label.DocumentMetadata{}, err
	}
	return label.DocumentMetadata{
		Slug: value.Slug, CountryCode: value.CountryCode, Website: value.Website,
		SocialLinks: value.SocialLinks, ParentLabelID: value.ParentLabelID,
	}, nil
}

// UpdateDocumentMetadata implements label.Store.
func (s *Store) UpdateDocumentMetadata(_ context.Context, labelID string, change entityparticipant.MetadataChange, now time.Time) error {
	return s.update("UpdateDocumentMetadata", labelID, func(value *label.Label) {
		apply := func(target **string, assignment entityparticipant.Assignment) {
			if assignment.Changed {
				*target = assignment.Value
			}
		}
		apply(&value.Slug, change.Slug)
		apply(&value.CountryCode, change.CountryCode)
		apply(&value.Website, change.Website)
		apply(&value.ParentLabelID, change.Parent)
		if change.SetSocialLinks {
			value.SocialLinks = maps.Clone(change.SocialLinks)
		}
		value.UpdatedAt = now
	})
}

// CheckResourceRoute implements label.Store.
func (s *Store) CheckResourceRoute(_ context.Context, slug string) error {
	if err := s.fail("CheckResourceRoute"); err != nil {
		return err
	}
	return s.requireRoute(slug)
}

// LockResourceRoute implements label.Store.
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
		return domainerr.Duplicate("label", "slug", slug)
	}
	return nil
}

// ResourceRouteAvailable implements label.Store.
func (s *Store) ResourceRouteAvailable(_ context.Context, slug string) (bool, error) {
	if err := s.fail("ResourceRouteAvailable"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.routes[slug], nil
}

// LocaleTitles implements label.Store.
func (s *Store) LocaleTitles(_ context.Context, labelID string) ([]label.LocaleTitle, error) {
	if err := s.fail("LocaleTitles"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var titles []label.LocaleTitle
	for key, value := range s.locales {
		if key.labelID == labelID {
			titles = append(titles, value)
		}
	}
	sort.Slice(titles, func(i, j int) bool { return titles[i].Locale < titles[j].Locale })
	return titles, nil
}

// LocaleTitle implements label.Store.
func (s *Store) LocaleTitle(_ context.Context, labelID, locale string, _ bool) (label.LocaleTitle, bool, error) {
	if err := s.fail("LocaleTitle"); err != nil {
		return label.LocaleTitle{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.locales[localeKey{labelID, locale}]
	return value, ok, nil
}

// LocaleTitleText implements label.Store.
func (s *Store) LocaleTitleText(ctx context.Context, labelID, locale string) (string, error) {
	value, ok, err := s.LocaleTitle(ctx, labelID, locale, false)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", label.ErrLocaleNotFound
	}
	if value.Title == nil {
		return "", nil
	}
	return *value.Title, nil
}

// SaveLocaleTitle implements label.Store.
func (s *Store) SaveLocaleTitle(_ context.Context, labelID, locale, title string, now time.Time) error {
	if err := s.fail("SaveLocaleTitle"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{labelID, locale}
	current, ok := s.locales[key]
	if !ok {
		current = label.LocaleTitle{Locale: locale, CreatedAt: now}
	}
	current.Title, current.UpdatedAt = &title, now
	s.locales[key] = current
	return nil
}

// CreateTargetLocale implements label.Store.
func (s *Store) CreateTargetLocale(_ context.Context, labelID, locale string, now time.Time) (bool, error) {
	if err := s.fail("CreateTargetLocale"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{labelID, locale}
	if _, exists := s.locales[key]; exists {
		return false, nil
	}
	s.locales[key] = label.LocaleTitle{Locale: locale, CreatedAt: now, UpdatedAt: now}
	return true, nil
}

// TouchTargetLocale implements label.Store.
func (s *Store) TouchTargetLocale(_ context.Context, labelID, locale string, updatedAt time.Time) (bool, error) {
	if err := s.fail("TouchTargetLocale"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{labelID, locale}
	value, exists := s.locales[key]
	if !exists {
		return false, nil
	}
	value.UpdatedAt = updatedAt
	s.locales[key] = value
	return true, nil
}

// DeleteTargetLocale implements label.Store.
func (s *Store) DeleteTargetLocale(_ context.Context, labelID, locale string) (bool, error) {
	if err := s.fail("DeleteTargetLocale"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{labelID, locale}
	if _, exists := s.locales[key]; !exists {
		return false, nil
	}
	delete(s.locales, key)
	return true, nil
}

// DeletionImpacts implements label.Store.
func (s *Store) DeletionImpacts(_ context.Context, labelID string) ([]label.DeletionImpact, error) {
	if err := s.fail("DeletionImpacts"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var children []label.DeletionImpact
	for id, value := range s.labels {
		if value.ParentLabelID != nil && *value.ParentLabelID == labelID {
			title := ""
			if locale, ok := s.locales[localeKey{id, value.SourceLocale}]; ok && locale.Title != nil {
				title = *locale.Title
			}
			children = append(children, label.DeletionImpact{Kind: label.ImpactChildLabel, EntityID: id, DisplayName: title})
		}
	}
	sort.Slice(children, func(i, j int) bool { return children[i].EntityID < children[j].EntityID })
	return append(children, s.impacts[labelID]...), nil
}

// CountArtistsByLabel implements label.Store.
func (s *Store) CountArtistsByLabel(_ context.Context, labelIDs []string) (map[string]int32, error) {
	return s.counts("CountArtistsByLabel", s.artistCounts, labelIDs)
}

// CountReleasesByLabel implements label.Store.
func (s *Store) CountReleasesByLabel(_ context.Context, labelIDs []string) (map[string]int32, error) {
	return s.counts("CountReleasesByLabel", s.releaseCounts, labelIDs)
}

func (s *Store) counts(method string, source map[string]int32, labelIDs []string) (map[string]int32, error) {
	if err := s.fail(method); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int32{}
	for _, id := range labelIDs {
		if count, ok := source[id]; ok {
			counts[id] = count
		}
	}
	return counts, nil
}

// DeleteShareLinks implements label.Store.
func (s *Store) DeleteShareLinks(_ context.Context, labelID string) error {
	if err := s.fail("DeleteShareLinks"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shareLinks = append(s.shareLinks, labelID)
	return nil
}

// PublishContentUpdate implements label.Store.
func (s *Store) PublishContentUpdate(_ context.Context, update label.ContentUpdate) error {
	if err := s.fail("PublishContentUpdate"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, update)
	return nil
}

// AppendMemberAudit implements label.Store through the appender, which a
// fake ignores the transaction of.
func (s *Store) AppendMemberAudit(ctx context.Context, writer domainaudit.Appender, memberID string, action sharedtelemetry.AuditAction, build domainaudit.Builder) error {
	return domainaudit.AppendMember(ctx, nil, writer, memberID, action, build)
}

// LockAttachableFiles implements label.Store.
func (s *Store) LockAttachableFiles(_ context.Context, fileIDs []string) error {
	if err := s.fail("LockAttachableFiles"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lockedFiles = append(s.lockedFiles, fileIDs...)
	return nil
}

// BindLogo implements label.Store.
func (s *Store) BindLogo(_ context.Context, fileID, labelID string, variant label.LogoVariant) error {
	if err := s.fail("BindLogo"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bindings[labelID] == nil {
		s.bindings[labelID] = map[label.LogoVariant]Binding{}
	}
	s.bindings[labelID][variant] = Binding{FileID: fileID}
	return nil
}

// ReleaseLogoBinding implements label.Store.
func (s *Store) ReleaseLogoBinding(_ context.Context, labelID string, variant label.LogoVariant) error {
	if err := s.fail("ReleaseLogoBinding"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bindings[labelID], variant)
	return nil
}

// ReleaseLogoBindings implements label.Store.
func (s *Store) ReleaseLogoBindings(_ context.Context, labelID string) error {
	if err := s.fail("ReleaseLogoBindings"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bindings, labelID)
	return nil
}

// ReadyAssets implements label.Store.
func (s *Store) ReadyAssets(_ context.Context, assetIDs []string) (map[string]*mediaasset.Reference, error) {
	return s.ready("ReadyAssets", s.readyAssets, assetIDs)
}

// ReadyLogoAssets implements label.Store.
func (s *Store) ReadyLogoAssets(_ context.Context, fileIDs []string) (map[string]*mediaasset.Reference, error) {
	return s.ready("ReadyLogoAssets", s.readyLogos, fileIDs)
}

func (s *Store) ready(method string, source map[string]*mediaasset.Reference, ids []string) (map[string]*mediaasset.Reference, error) {
	if err := s.fail(method); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	refs := map[string]*mediaasset.Reference{}
	for _, id := range ids {
		if ref := source[id]; ref != nil {
			refs[id] = ref
		}
	}
	return refs, nil
}

// RequestOG implements label.Store.
func (s *Store) RequestOG(_ context.Context, labelID, reason string) (string, error) {
	if err := s.fail("RequestOG"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ogRequests = append(s.ogRequests, OGRequest{LabelID: labelID, Reason: reason})
	return s.OGRunID, nil
}

// CancelOG implements label.Store.
func (s *Store) CancelOG(_ context.Context, labelID string) error {
	if err := s.fail("CancelOG"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ogCancels = append(s.ogCancels, labelID)
	return nil
}

// ResolveSessionPrincipal implements label.Store.
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

// RequireCurrentContributors implements label.Store.
func (s *Store) RequireCurrentContributors(_ context.Context, labelID string, _ []string) error {
	if err := s.fail("RequireCurrentContributors"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revokedRoom[labelID] {
		return domainerr.Denied("collaboration contributors may no longer edit this label")
	}
	return nil
}

// RequireDocumentContributors implements label.Store: every contributor
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

// ResolveInitialSourceLocale implements label.Store.
func (s *Store) ResolveInitialSourceLocale(context.Context, string) string {
	if s.InitialSourceLocale != "" {
		return s.InitialSourceLocale
	}
	return "en"
}
