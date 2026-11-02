// Package worktest holds the in-memory work.Store the Work use-case tests
// run on, and the Store contract it shares with workpostgres. The fake
// knows no SQL and takes no lock; it keeps the sentinels, orderings and row
// counts the PostgreSQL store answers with, and a minimal content document
// whose revision advances like the content store's.
package worktest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/identitystate"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	"github.com/echovisionlab/geul-api/internal/work"
	"github.com/google/uuid"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Document is a fake content document: its revision, profile and source
// locale, the Blocks a snapshot replacement stored, the locales its Blocks
// carry and the File references of its Blocks.
type Document struct {
	ID           uuid.UUID
	Profile      string
	Revision     uuid.UUID
	SourceLocale string
	Blocks       []contentblock.BaseBlock
	Locales      map[string]bool
	Media        []*fileref.BlockMediaItem
}

// Member is a Member row the Store reads: its nickname, preferred locale,
// and whether it is deleted or onboarded.
type Member struct {
	Nickname        string
	PreferredLocale *string
	Deleted         bool
	Unonboarded     bool
	// Unlinked is a Member no account Identity names, which cannot be
	// credited.
	Unlinked bool
}

// OGRequest is one OG generation the Store started.
type OGRequest struct {
	WorkID string
	Locale string
	Reason string
}

type localeKey struct{ workID, locale string }

type state struct {
	works            map[string]work.Work
	locales          map[localeKey]work.LocaleMetadata
	localeOgAssets   map[localeKey]*string
	groups           map[string]work.CreditGroup
	credits          map[string]work.Credit
	clients          map[string]work.ClientRow
	workClients      map[string][]string
	versions         map[string]work.Version
	documents        map[uuid.UUID]Document
	routes           map[string]bool
	members          map[string]Member
	mapPlaces        map[string]bool
	files            map[string]bool
	readyAssets      map[string]*mediaasset.Reference
	fileAssets       map[string]*mediaasset.Reference
	artists          map[string]work.CreditArtistRow
	artistMembers    map[string][]string
	sessions         map[string]*auth.UserInfo
	revokedRoom      map[string]bool
	updates          []work.ContentUpdate
	ogRequests       []OGRequest
	ogCancels        []string
	shareLinkDeletes []string
	bindings         map[string]string
	contentResults   []contentblock.Result
	lockedSessions   []string
}

func (s state) clone() state {
	clone := s
	clone.works = maps.Clone(s.works)
	clone.locales = maps.Clone(s.locales)
	clone.localeOgAssets = maps.Clone(s.localeOgAssets)
	clone.groups = maps.Clone(s.groups)
	clone.credits = maps.Clone(s.credits)
	clone.clients = maps.Clone(s.clients)
	clone.workClients = maps.Clone(s.workClients)
	clone.versions = maps.Clone(s.versions)
	clone.documents = maps.Clone(s.documents)
	clone.routes = maps.Clone(s.routes)
	clone.members = maps.Clone(s.members)
	clone.files = maps.Clone(s.files)
	clone.updates = slices.Clone(s.updates)
	clone.ogRequests = slices.Clone(s.ogRequests)
	clone.ogCancels = slices.Clone(s.ogCancels)
	clone.shareLinkDeletes = slices.Clone(s.shareLinkDeletes)
	clone.bindings = maps.Clone(s.bindings)
	clone.lockedSessions = slices.Clone(s.lockedSessions)
	return clone
}

// Store is the in-memory work.Store. Seed it with the Put methods; read it
// back with the getters.
type Store struct {
	mu sync.Mutex
	state
	// Now is the clock of the rows it stamps; zero is time.Now.
	Now func() time.Time
	// DefaultLocaleValue is the translation runtime's default locale; empty
	// is "en".
	DefaultLocaleValue string
	// OGRunID is the run ID RequestOG returns.
	OGRunID string
	// Fail fails the Store method of that name with its error.
	Fail map[string]error
	// Before runs before the Store method of that name, as a concurrent
	// writer that commits just before the lock is granted would.
	Before map[string]func()
}

// NewStore returns an empty fake Store.
func NewStore() *Store {
	return &Store{state: state{
		works: map[string]work.Work{}, locales: map[localeKey]work.LocaleMetadata{}, localeOgAssets: map[localeKey]*string{},
		groups: map[string]work.CreditGroup{}, credits: map[string]work.Credit{}, clients: map[string]work.ClientRow{},
		workClients: map[string][]string{}, versions: map[string]work.Version{}, documents: map[uuid.UUID]Document{},
		routes: map[string]bool{}, members: map[string]Member{}, mapPlaces: map[string]bool{}, files: map[string]bool{},
		readyAssets: map[string]*mediaasset.Reference{}, fileAssets: map[string]*mediaasset.Reference{},
		artists: map[string]work.CreditArtistRow{}, artistMembers: map[string][]string{},
		sessions: map[string]*auth.UserInfo{}, revokedRoom: map[string]bool{}, bindings: map[string]string{},
	}}
}

// New returns a fake Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[work.Store]) {
	store := NewStore()
	return store, mutationtest.New[work.Store](store)
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

var _ work.Store = (*Store)(nil)

func (s *Store) fail(method string) error {
	if hook := s.Before[method]; hook != nil {
		hook()
	}
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

// errDuplicateKey answers an insert of a row whose key exists, as the
// primary key does.
var errDuplicateKey = errors.New("duplicate key value violates unique constraint")

// PutWork stores a Work row, filling its ID, type, source locale, status
// and timestamps when unset.
func (s *Store) PutWork(value work.Work) work.Work {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value.ID == "" {
		value.ID = uuid.NewString()
	}
	if value.Type == "" {
		value.Type = "WORK_TYPE_PORTFOLIO"
	}
	if value.SourceLocale == "" {
		value.SourceLocale = "en"
	}
	if value.Status == "" {
		value.Status = work.StatusDraft
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = s.now()
	}
	if value.UpdatedAt.IsZero() {
		value.UpdatedAt = value.CreatedAt
	}
	s.works[value.ID] = value
	return value
}

// PutDocument stores a content document and points workID at it.
func (s *Store) PutDocument(workID string, document Document) Document {
	s.mu.Lock()
	defer s.mu.Unlock()
	if document.ID == uuid.Nil {
		document.ID = uuid.New()
	}
	if document.Revision == uuid.Nil {
		document.Revision = uuid.New()
	}
	if document.Profile == "" {
		document.Profile = "work"
	}
	value := s.works[workID]
	if document.SourceLocale == "" {
		document.SourceLocale = value.SourceLocale
	}
	id := document.ID.String()
	value.ContentDocumentID = &id
	s.works[workID] = value
	s.documents[document.ID] = document
	return document
}

// PutLocale stores a work_translation row, stamping updated_at when unset.
func (s *Store) PutLocale(workID string, locale work.LocaleMetadata) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if locale.UpdatedAt.IsZero() {
		locale.UpdatedAt = s.now()
	}
	s.locales[localeKey{workID, locale.Locale}] = locale
}

// PutLocaleOgAsset gives a Work locale its OG image.
func (s *Store) PutLocaleOgAsset(workID, locale, assetID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.localeOgAssets[localeKey{workID, locale}] = &assetID
}

// PutCreditGroup stores a credit group, filling its ID when unset.
func (s *Store) PutCreditGroup(group work.CreditGroup) work.CreditGroup {
	s.mu.Lock()
	defer s.mu.Unlock()
	if group.ID == "" {
		group.ID = uuid.NewString()
	}
	s.groups[group.ID] = group
	return group
}

// PutCredit stores a credit, filling its ID when unset.
func (s *Store) PutCredit(credit work.Credit) work.Credit {
	s.mu.Lock()
	defer s.mu.Unlock()
	if credit.ID == "" {
		credit.ID = uuid.NewString()
	}
	s.credits[credit.ID] = credit
	return credit
}

// PutClient stores a client a Work may list.
func (s *Store) PutClient(client work.ClientRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[client.ID] = client
}

// LinkClients lists clientIDs, in order, as workID's clients.
func (s *Store) LinkClients(workID string, clientIDs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workClients[workID] = slices.Clone(clientIDs)
}

// PutVersion stores a version row, filling its ID when unset.
func (s *Store) PutVersion(version work.Version) work.Version {
	s.mu.Lock()
	defer s.mu.Unlock()
	if version.ID == "" {
		version.ID = uuid.NewString()
	}
	s.versions[version.ID] = version
	return version
}

// PutMember stores a Member row.
func (s *Store) PutMember(memberID string, member Member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[memberID] = member
}

// PutRoute occupies slug with a Page route.
func (s *Store) PutRoute(slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[slug] = true
}

// PutMapPlace stores a Map Place.
func (s *Store) PutMapPlace(mapPlaceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mapPlaces[mapPlaceID] = true
}

// PutFile stores an attachable File.
func (s *Store) PutFile(fileID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[fileID] = true
}

// PutReadyAsset makes assetID a ready public asset.
func (s *Store) PutReadyAsset(assetID string, ref *mediaasset.Reference) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readyAssets[assetID] = ref
}

// PutFileAsset makes ref the ready asset of fileID's kind.
func (s *Store) PutFileAsset(fileID, kind string, ref *mediaasset.Reference) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fileAssets[fileID+"\x00"+kind] = ref
}

// PutArtist stores a credited Artist; memberIDs own or manage it.
func (s *Store) PutArtist(artist work.CreditArtistRow, memberIDs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.artists[artist.ID] = artist
	for _, memberID := range memberIDs {
		s.artistMembers[memberID] = append(slices.Clone(s.artistMembers[memberID]), artist.ID)
	}
}

// PutSession resolves sessionID to principal.
func (s *Store) PutSession(sessionID string, principal *auth.UserInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = principal
}

// RevokeRoom makes RequireCurrentContributors reject saves of workID.
func (s *Store) RevokeRoom(workID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revokedRoom[workID] = true
}

// QueueContentResult makes the next content batch answer result, as a
// content store whose Blocks changed in that way would.
func (s *Store) QueueContentResult(result contentblock.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contentResults = append(s.contentResults, result)
}

// Stored returns the Work row and whether it exists.
func (s *Store) Stored(workID string) (work.Work, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.works[workID]
	return value, ok
}

// Locale returns a Work locale row and whether it exists.
func (s *Store) Locale(workID, locale string) (work.LocaleMetadata, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.locales[localeKey{workID, locale}]
	return value, ok
}

// Document returns a content document and whether it exists.
func (s *Store) Document(documentID uuid.UUID) (Document, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.documents[documentID]
	return value, ok
}

// CreditGroups and StoredCredits return a Work's rows by sort order and ID.
func (s *Store) CreditGroups(workID string) []work.CreditGroup {
	groups, _, _ := s.CreditRows(context.Background(), workID)
	return groups
}

func (s *Store) StoredCredits(workID string) []work.Credit {
	_, credits, _ := s.CreditRows(context.Background(), workID)
	return credits
}

// StoredClientIDs returns a Work's clients in order.
func (s *Store) StoredClientIDs(workID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.workClients[workID])
}

// Versions returns a Work's versions, newest first.
func (s *Store) Versions(workID string) []work.Version {
	versions, _ := s.ListVersions(context.Background(), workID, 1<<30, 0)
	return versions
}

// Updates returns the content.updated signals the Store scheduled.
func (s *Store) Updates() []work.ContentUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.updates)
}

// OGRequests and OGCancels return the OG generations the Store started and
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

// ShareLinkDeletes returns the Works whose share links the Store removed.
func (s *Store) ShareLinkDeletes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.shareLinkDeletes)
}

// FeaturedImageBinding returns the File bound as workID's featured image.
func (s *Store) FeaturedImageBinding(workID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fileID, ok := s.bindings[workID]
	return fileID, ok
}

// LockedSessions returns the sessions the Store locked.
func (s *Store) LockedSessions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.lockedSessions)
}

func (s *Store) work(workID string) (work.Work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.works[workID]
	if !ok {
		return work.Work{}, work.ErrWorkNotFound
	}
	return value, nil
}

func rootOf(value work.Work) work.Root {
	return work.Root{ID: value.ID, Status: value.Status, ContentDocumentID: value.ContentDocumentID, SourceLocale: value.SourceLocale}
}

// FindWork implements work.Store.
func (s *Store) FindWork(_ context.Context, workID string) (work.Work, error) {
	if err := s.fail("FindWork"); err != nil {
		return work.Work{}, err
	}
	return s.work(workID)
}

// LockWork implements work.Store.
func (s *Store) LockWork(_ context.Context, workID string, _ work.RowLock) (work.Work, error) {
	if err := s.fail("LockWork"); err != nil {
		return work.Work{}, err
	}
	return s.work(workID)
}

// WorkExists implements work.Store.
func (s *Store) WorkExists(_ context.Context, workID string) (bool, error) {
	if err := s.fail("WorkExists"); err != nil {
		return false, err
	}
	_, err := s.work(workID)
	return err == nil, nil
}

// LockRoot implements work.Store.
func (s *Store) LockRoot(_ context.Context, workID string) (work.Root, error) {
	if err := s.fail("LockRoot"); err != nil {
		return work.Root{}, err
	}
	value, err := s.work(workID)
	if err != nil {
		return work.Root{}, err
	}
	return work.Root{ID: value.ID, Status: value.Status}, nil
}

// ContentRoot implements work.Store.
func (s *Store) ContentRoot(_ context.Context, workID string, _ work.RowLock) (work.Root, error) {
	if err := s.fail("ContentRoot"); err != nil {
		return work.Root{}, err
	}
	value, err := s.work(workID)
	if err != nil {
		return work.Root{}, err
	}
	return rootOf(value), nil
}

// LockDocumentRoot implements work.Store.
func (s *Store) LockDocumentRoot(_ context.Context, documentID uuid.UUID) (work.Root, error) {
	if err := s.fail("LockDocumentRoot"); err != nil {
		return work.Root{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range s.works {
		if value.ContentDocumentID != nil && *value.ContentDocumentID == documentID.String() {
			return rootOf(value), nil
		}
	}
	return work.Root{}, work.ErrWorkNotFound
}

// SourceLocale implements work.Store.
func (s *Store) SourceLocale(_ context.Context, workID string) (string, error) {
	if err := s.fail("SourceLocale"); err != nil {
		return "", err
	}
	value, err := s.work(workID)
	return value.SourceLocale, err
}

// LockSourceLocale implements work.Store.
func (s *Store) LockSourceLocale(ctx context.Context, workID string) (string, error) {
	if err := s.fail("LockSourceLocale"); err != nil {
		return "", err
	}
	return s.SourceLocale(ctx, workID)
}

func timeLess(left, right time.Time, descending bool) bool {
	if descending {
		return left.After(right)
	}
	return left.Before(right)
}

// ListWorks implements work.Store. It sorts by created_at, updated_at,
// published_at or the source title (created_at descending by default) with
// ties by ID, and takes the search (ILIKE on the source title), has_slug,
// has_featured_image and status, type and map_place_id (EQ) filters; any
// other filter fails, since only the PostgreSQL store compiles them.
func (s *Store) ListWorks(_ context.Context, page work.AdminListPage) ([]work.Work, int64, error) {
	if err := s.fail("ListWorks"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	all := slices.Collect(maps.Values(s.works))
	titles := make(map[string]string, len(all))
	for _, value := range all {
		if row, ok := s.locales[localeKey{value.ID, value.SourceLocale}]; ok && row.Title != nil {
			titles[value.ID] = *row.Title
		}
	}
	s.mu.Unlock()
	matched := make([]work.Work, 0, len(all))
	for _, value := range all {
		keep, err := matchesAdminFilters(value, titles[value.ID], page.Filters)
		if err != nil {
			return nil, 0, err
		}
		if keep {
			matched = append(matched, value)
		}
	}
	field, descending := "created_at", true
	if len(page.Sorts) != 0 {
		field = page.Sorts[0].GetField()
		descending = page.Sorts[0].GetOrder() == commonv1.SortOrder_SORT_ORDER_DESC
	}
	sort.SliceStable(matched, func(i, j int) bool {
		left, right := matched[i], matched[j]
		if field == "title" {
			if titles[left.ID] != titles[right.ID] {
				return (titles[left.ID] < titles[right.ID]) != descending
			}
			return left.ID < right.ID
		}
		var a, b time.Time
		switch field {
		case "updated_at":
			a, b = left.UpdatedAt, right.UpdatedAt
		case "published_at":
			if left.PublishedAt != nil {
				a = *left.PublishedAt
			}
			if right.PublishedAt != nil {
				b = *right.PublishedAt
			}
		default:
			a, b = left.CreatedAt, right.CreatedAt
		}
		if !a.Equal(b) {
			return timeLess(a, b, descending)
		}
		return left.ID < right.ID
	})
	total := int64(len(matched))
	return pageOf(matched, page.Limit, page.Offset), total, nil
}

func matchesAdminFilters(value work.Work, title string, filters []*commonv1.FilterSpec) (bool, error) {
	for _, filter := range filters {
		if filter == nil {
			continue
		}
		switch filter.GetField() {
		case "has_slug":
			has := value.Slug != nil && *value.Slug != ""
			if (filter.GetValue() == "true" && !has) || (filter.GetValue() == "false" && has) {
				return false, nil
			}
		case "has_featured_image":
			has := value.FeaturedImageFileID != nil
			if (filter.GetValue() == "true" && !has) || (filter.GetValue() == "false" && has) {
				return false, nil
			}
		case "search":
			if filter.GetOp() != commonv1.FilterOp_FILTER_OP_ILIKE {
				return false, fmt.Errorf("fake Work store does not take the search filter %s", filter.GetOp())
			}
			if !strings.Contains(strings.ToLower(title), strings.ToLower(filter.GetValue())) {
				return false, nil
			}
		case "status", "type", "map_place_id":
			if filter.GetOp() != commonv1.FilterOp_FILTER_OP_EQ {
				return false, fmt.Errorf("fake Work store does not take the %s filter %s", filter.GetField(), filter.GetOp())
			}
			column := map[string]string{"status": string(value.Status), "type": value.Type}[filter.GetField()]
			if filter.GetField() == "map_place_id" && value.MapPlaceID != nil {
				column = *value.MapPlaceID
			}
			if column != filter.GetValue() {
				return false, nil
			}
		default:
			return false, fmt.Errorf("fake Work store does not take the %q filter", filter.GetField())
		}
	}
	return true, nil
}

func pageOf[T any](values []T, limit, offset int32) []T {
	start := min(int(offset), len(values))
	end := len(values)
	if limit >= 0 {
		end = min(start+int(limit), len(values))
	}
	return values[start:end]
}

func (s *Store) countPerWork(workIDs []string, count func(workID string) int32) map[string]int32 {
	counts := map[string]int32{}
	for _, workID := range workIDs {
		if n := count(workID); n != 0 {
			counts[workID] = n
		}
	}
	return counts
}

// CountCredits implements work.Store.
func (s *Store) CountCredits(_ context.Context, workIDs []string) (map[string]int32, error) {
	if err := s.fail("CountCredits"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.countPerWork(workIDs, func(workID string) int32 {
		var n int32
		for _, credit := range s.credits {
			if credit.WorkID == workID {
				n++
			}
		}
		return n
	}), nil
}

// CountClients implements work.Store.
func (s *Store) CountClients(_ context.Context, workIDs []string) (map[string]int32, error) {
	if err := s.fail("CountClients"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.countPerWork(workIDs, func(workID string) int32 { return int32(len(s.workClients[workID])) }), nil
}

func (s *Store) slugHeld(slug, excludeID string) bool {
	for _, value := range s.works {
		if value.ID != excludeID && value.Slug != nil && *value.Slug == slug {
			return true
		}
	}
	return false
}

// SlugTaken implements work.Store.
func (s *Store) SlugTaken(_ context.Context, slug string, excludeID *string) (bool, error) {
	if err := s.fail("SlugTaken"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exclude := ""
	if excludeID != nil {
		exclude = *excludeID
	}
	return s.slugHeld(slug, exclude), nil
}

// InsertWork implements work.Store.
func (s *Store) InsertWork(_ context.Context, value *work.Work) error {
	if err := s.fail("InsertWork"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if value.Slug != nil && s.slugHeld(*value.Slug, "") {
		return work.ErrSlugTaken
	}
	value.ID = uuid.NewString()
	value.CreatedAt = s.now()
	value.UpdatedAt = value.CreatedAt
	s.works[value.ID] = *value
	return nil
}

// UpdateWork implements work.Store.
func (s *Store) UpdateWork(_ context.Context, workID string, change work.WorkChange) error {
	if err := s.fail("UpdateWork"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.works[workID]
	if !ok {
		return nil
	}
	if change.ClearSlug {
		value.Slug = nil
	} else if change.Slug != nil {
		if s.slugHeld(*change.Slug, workID) {
			return work.ErrSlugTaken
		}
		slug := *change.Slug
		value.Slug = &slug
	}
	if change.Type != nil {
		value.Type = *change.Type
	}
	if change.Metadata != nil {
		value.Metadata = change.Metadata
	}
	if change.Featured != nil {
		value.Featured = *change.Featured
	}
	if change.Year != nil {
		value.Year = *change.Year
	}
	if change.Month != nil {
		value.Month = *change.Month
	}
	if change.UntilYear.Set {
		value.UntilYear = change.UntilYear.Value
	}
	if change.UntilMonth.Set {
		value.UntilMonth = change.UntilMonth.Value
	}
	if change.IsPresent != nil {
		value.IsPresent = *change.IsPresent
	}
	if change.ClearPlace {
		value.MapPlaceID = nil
	} else if change.MapPlaceID != nil {
		mapPlaceID := *change.MapPlaceID
		value.MapPlaceID = &mapPlaceID
	}
	value.UpdatedAt = change.UpdatedAt
	s.works[workID] = value
	return nil
}

// SetStatus implements work.Store.
func (s *Store) SetStatus(_ context.Context, workID string, status work.Status, publishedAt *time.Time, now time.Time) error {
	if err := s.fail("SetStatus"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.works[workID]
	if !ok {
		return nil
	}
	value.Status, value.UpdatedAt = status, now
	if publishedAt != nil {
		at := *publishedAt
		value.PublishedAt = &at
	}
	s.works[workID] = value
	return nil
}

// SetFeaturedImage implements work.Store.
func (s *Store) SetFeaturedImage(_ context.Context, workID string, fileID *string, now time.Time) error {
	if err := s.fail("SetFeaturedImage"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.works[workID]
	if !ok {
		return work.ErrWorkNotFound
	}
	value.FeaturedImageFileID, value.UpdatedAt = cloneString(fileID), now
	s.works[workID] = value
	return nil
}

// FeaturedImageFileID implements work.Store.
func (s *Store) FeaturedImageFileID(_ context.Context, workID string) (*string, error) {
	if err := s.fail("FeaturedImageFileID"); err != nil {
		return nil, err
	}
	value, err := s.work(workID)
	return cloneString(value.FeaturedImageFileID), err
}

// TouchWork implements work.Store.
func (s *Store) TouchWork(_ context.Context, workID string, now time.Time) error {
	if err := s.fail("TouchWork"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if value, ok := s.works[workID]; ok {
		value.UpdatedAt = now
		s.works[workID] = value
	}
	return nil
}

// StampWork implements work.Store.
func (s *Store) StampWork(ctx context.Context, workID string, now time.Time) error {
	if err := s.fail("StampWork"); err != nil {
		return err
	}
	return s.TouchWork(ctx, workID, now)
}

// SwitchSourceLocale implements work.Store.
func (s *Store) SwitchSourceLocale(_ context.Context, workID, from, to string, now time.Time) error {
	if err := s.fail("SwitchSourceLocale"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.works[workID]
	if !ok || value.SourceLocale != from {
		return work.ErrRowChanged
	}
	value.SourceLocale, value.UpdatedAt = to, now
	s.works[workID] = value
	return nil
}

// DeleteWork implements work.Store. Like the foreign keys, the Work's
// locales, credits, clients and versions go with it.
func (s *Store) DeleteWork(_ context.Context, workID string) error {
	if err := s.fail("DeleteWork"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.works, workID)
	maps.DeleteFunc(s.locales, func(key localeKey, _ work.LocaleMetadata) bool { return key.workID == workID })
	maps.DeleteFunc(s.groups, func(_ string, group work.CreditGroup) bool { return group.WorkID == workID })
	maps.DeleteFunc(s.credits, func(_ string, credit work.Credit) bool { return credit.WorkID == workID })
	maps.DeleteFunc(s.versions, func(_ string, version work.Version) bool { return version.WorkID == workID })
	delete(s.workClients, workID)
	return nil
}

// MapPlaceExists implements work.Store.
func (s *Store) MapPlaceExists(_ context.Context, mapPlaceID string) (bool, error) {
	if err := s.fail("MapPlaceExists"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mapPlaces[mapPlaceID], nil
}

// CheckResourceRoute implements work.Store with routeregistry's answer.
func (s *Store) CheckResourceRoute(_ context.Context, slug string) error {
	if err := s.fail("CheckResourceRoute"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.routes[slug] {
		return domainerr.Duplicate("work", "slug", slug)
	}
	return nil
}

// LockResourceRoute implements work.Store.
func (s *Store) LockResourceRoute(ctx context.Context, slug string) error {
	if err := s.fail("LockResourceRoute"); err != nil {
		return err
	}
	return s.CheckResourceRoute(ctx, slug)
}

// ResourceRouteAvailable implements work.Store.
func (s *Store) ResourceRouteAvailable(_ context.Context, slug string) (bool, error) {
	if err := s.fail("ResourceRouteAvailable"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.routes[slug], nil
}

// PreferredLocale implements work.Store.
func (s *Store) PreferredLocale(_ context.Context, memberID string) (*string, error) {
	if err := s.fail("PreferredLocale"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	member, ok := s.members[memberID]
	if !ok || member.Deleted {
		return nil, nil
	}
	return cloneString(member.PreferredLocale), nil
}

// DefaultLocale implements work.Store.
func (s *Store) DefaultLocale(context.Context) (string, error) {
	if err := s.fail("DefaultLocale"); err != nil {
		return "", err
	}
	if s.DefaultLocaleValue == "" {
		return "en", nil
	}
	return s.DefaultLocaleValue, nil
}

// DeleteShareLinks implements work.Store.
func (s *Store) DeleteShareLinks(_ context.Context, workID string) error {
	if err := s.fail("DeleteShareLinks"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shareLinkDeletes = append(s.shareLinkDeletes, workID)
	return nil
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneMetadata(value work.LocaleMetadata) work.LocaleMetadata {
	value.Title, value.Summary = cloneString(value.Title), cloneString(value.Summary)
	return value
}

func (s *Store) sourceLocaleRows(workIDs []string) map[string]work.LocaleMetadata {
	rows := map[string]work.LocaleMetadata{}
	for _, workID := range workIDs {
		workID = strings.TrimSpace(workID)
		value, ok := s.works[workID]
		if !ok {
			continue
		}
		if locale, ok := s.locales[localeKey{workID, value.SourceLocale}]; ok {
			rows[workID] = cloneMetadata(locale)
		}
	}
	return rows
}

// SourceMetadata implements work.Store.
func (s *Store) SourceMetadata(_ context.Context, workIDs []string) (map[string]work.LocaleMetadata, error) {
	if err := s.fail("SourceMetadata"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.sourceLocaleRows(workIDs)
	for workID, row := range rows {
		row.UpdatedAt = time.Time{}
		rows[workID] = row
	}
	return rows, nil
}

// SourceDocuments implements work.Store.
func (s *Store) SourceDocuments(_ context.Context, workIDs []string) (map[string]work.SourceDocument, error) {
	if err := s.fail("SourceDocuments"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	documents := map[string]work.SourceDocument{}
	for workID, row := range s.sourceLocaleRows(workIDs) {
		documents[workID] = work.SourceDocument{
			Title: row.Title, Summary: row.Summary,
			OgAssetID: cloneString(s.localeOgAssets[localeKey{workID, row.Locale}]),
		}
	}
	return documents, nil
}

func (s *Store) locale(workID, locale string) (work.LocaleMetadata, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.locales[localeKey{workID, locale}]
	return cloneMetadata(row), ok
}

// LocaleMetadata implements work.Store.
func (s *Store) LocaleMetadata(_ context.Context, workID, locale string, _ bool) (work.LocaleMetadata, bool, error) {
	if err := s.fail("LocaleMetadata"); err != nil {
		return work.LocaleMetadata{}, false, err
	}
	row, ok := s.locale(workID, locale)
	return row, ok, nil
}

// LockLocaleText implements work.Store.
func (s *Store) LockLocaleText(_ context.Context, workID, locale string) (work.LocaleMetadata, error) {
	if err := s.fail("LockLocaleText"); err != nil {
		return work.LocaleMetadata{}, err
	}
	row, ok := s.locale(workID, locale)
	if !ok {
		return work.LocaleMetadata{}, work.ErrLocaleNotFound
	}
	row.UpdatedAt = time.Time{}
	return row, nil
}

// LockedLocaleText implements work.Store.
func (s *Store) LockedLocaleText(_ context.Context, workID, locale string) (work.LocaleMetadata, bool, error) {
	if err := s.fail("LockedLocaleText"); err != nil {
		return work.LocaleMetadata{}, false, err
	}
	row, ok := s.locale(workID, locale)
	if !ok {
		return work.LocaleMetadata{}, false, nil
	}
	row.UpdatedAt = time.Time{}
	return row, true, nil
}

// LocaleUpdatedAt implements work.Store.
func (s *Store) LocaleUpdatedAt(_ context.Context, workID, locale string) (time.Time, error) {
	if err := s.fail("LocaleUpdatedAt"); err != nil {
		return time.Time{}, err
	}
	row, ok := s.locale(workID, locale)
	if !ok {
		return time.Time{}, work.ErrLocaleNotFound
	}
	return row.UpdatedAt, nil
}

func (s *Store) insertLocale(workID string, row work.LocaleMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{workID, row.Locale}
	if _, exists := s.locales[key]; exists {
		return errDuplicateKey
	}
	s.locales[key] = cloneMetadata(row)
	return nil
}

// InsertSourceLocale implements work.Store.
func (s *Store) InsertSourceLocale(_ context.Context, workID, locale, title string, summary *string, now time.Time) error {
	if err := s.fail("InsertSourceLocale"); err != nil {
		return err
	}
	return s.insertLocale(workID, work.LocaleMetadata{Locale: locale, Title: &title, Summary: summary, UpdatedAt: now})
}

// UpsertSourceLocale implements work.Store.
func (s *Store) UpsertSourceLocale(_ context.Context, workID, locale string, title, summary *string, now time.Time) error {
	if err := s.fail("UpsertSourceLocale"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locales[localeKey{workID, locale}] = cloneMetadata(work.LocaleMetadata{Locale: locale, Title: title, Summary: summary, UpdatedAt: now})
	return nil
}

func (s *Store) updateLocale(workID, locale string, update func(*work.LocaleMetadata)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{workID, locale}
	row, ok := s.locales[key]
	if !ok {
		return work.ErrRowChanged
	}
	update(&row)
	s.locales[key] = cloneMetadata(row)
	return nil
}

func applyTextChange(row *work.LocaleMetadata, change work.LocaleTextChange) {
	if change.SetTitle {
		row.Title = cloneString(change.Title)
	}
	if change.SetSummary {
		row.Summary = cloneString(change.Summary)
	}
	row.UpdatedAt = change.UpdatedAt
}

// UpdateLocaleText implements work.Store.
func (s *Store) UpdateLocaleText(_ context.Context, workID, locale string, change work.LocaleTextChange) error {
	if err := s.fail("UpdateLocaleText"); err != nil {
		return err
	}
	return s.updateLocale(workID, locale, func(row *work.LocaleMetadata) { applyTextChange(row, change) })
}

// RestoreLocaleText implements work.Store.
func (s *Store) RestoreLocaleText(_ context.Context, workID, locale string, title, summary *string, now time.Time) error {
	if err := s.fail("RestoreLocaleText"); err != nil {
		return err
	}
	return s.updateLocale(workID, locale, func(row *work.LocaleMetadata) {
		row.Title, row.Summary, row.UpdatedAt = title, summary, now
	})
}

// InsertTargetLocale implements work.Store.
func (s *Store) InsertTargetLocale(_ context.Context, workID string, metadata work.LocaleMetadata) error {
	if err := s.fail("InsertTargetLocale"); err != nil {
		return err
	}
	return s.insertLocale(workID, metadata)
}

// UpdateTargetLocale implements work.Store.
func (s *Store) UpdateTargetLocale(_ context.Context, workID, locale string, change work.LocaleTextChange) error {
	if err := s.fail("UpdateTargetLocale"); err != nil {
		return err
	}
	return s.updateLocale(workID, locale, func(row *work.LocaleMetadata) { applyTextChange(row, change) })
}

// DeleteTargetLocale implements work.Store.
func (s *Store) DeleteTargetLocale(_ context.Context, workID, locale string) error {
	if err := s.fail("DeleteTargetLocale"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := localeKey{workID, locale}
	if _, ok := s.locales[key]; !ok {
		return work.ErrRowChanged
	}
	delete(s.locales, key)
	return nil
}

func sortBySortOrder[T any](values []T, sortOrder func(T) int, id func(T) string) {
	sort.Slice(values, func(i, j int) bool {
		if left, right := sortOrder(values[i]), sortOrder(values[j]); left != right {
			return left < right
		}
		return id(values[i]) < id(values[j])
	})
}

// CreditRows implements work.Store.
func (s *Store) CreditRows(_ context.Context, workID string) ([]work.CreditGroup, []work.Credit, error) {
	if err := s.fail("CreditRows"); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := []work.CreditGroup{}
	for _, group := range s.groups {
		if group.WorkID == workID {
			groups = append(groups, group)
		}
	}
	credits := []work.Credit{}
	for _, credit := range s.credits {
		if credit.WorkID == workID {
			credits = append(credits, credit)
		}
	}
	sortBySortOrder(groups, func(group work.CreditGroup) int { return group.SortOrder }, func(group work.CreditGroup) string { return group.ID })
	sortBySortOrder(credits, func(credit work.Credit) int { return credit.SortOrder }, func(credit work.Credit) string { return credit.ID })
	return groups, credits, nil
}

// FindCreditGroup implements work.Store.
func (s *Store) FindCreditGroup(_ context.Context, groupID string) (work.CreditGroup, error) {
	if err := s.fail("FindCreditGroup"); err != nil {
		return work.CreditGroup{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	group, ok := s.groups[groupID]
	if !ok {
		return work.CreditGroup{}, work.ErrCreditGroupNotFound
	}
	return group, nil
}

// LockCreditGroup implements work.Store.
func (s *Store) LockCreditGroup(ctx context.Context, groupID, workID string) (work.CreditGroup, error) {
	if err := s.fail("LockCreditGroup"); err != nil {
		return work.CreditGroup{}, err
	}
	group, err := s.FindCreditGroup(ctx, groupID)
	if err != nil || group.WorkID != workID {
		return work.CreditGroup{}, work.ErrCreditGroupNotFound
	}
	return group, nil
}

// CreditGroupInWork implements work.Store.
func (s *Store) CreditGroupInWork(_ context.Context, workID, groupID string) (bool, error) {
	if err := s.fail("CreditGroupInWork"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	group, ok := s.groups[groupID]
	return ok && group.WorkID == workID, nil
}

// CreditItemExists implements work.Store.
func (s *Store) CreditItemExists(_ context.Context, itemID string) (bool, error) {
	if err := s.fail("CreditItemExists"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, group := s.groups[itemID]
	_, credit := s.credits[itemID]
	return group || credit, nil
}

// InsertCreditGroup implements work.Store.
func (s *Store) InsertCreditGroup(_ context.Context, group *work.CreditGroup) error {
	if err := s.fail("InsertCreditGroup"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	group.ID = uuid.NewString()
	s.groups[group.ID] = *group
	return nil
}

// RenameCreditGroup implements work.Store.
func (s *Store) RenameCreditGroup(_ context.Context, groupID, name string) error {
	if err := s.fail("RenameCreditGroup"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if group, ok := s.groups[groupID]; ok {
		group.Name = name
		s.groups[groupID] = group
	}
	return nil
}

// DeleteCreditGroup implements work.Store.
func (s *Store) DeleteCreditGroup(_ context.Context, groupID string) error {
	if err := s.fail("DeleteCreditGroup"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.groups, groupID)
	return nil
}

// FindCredit implements work.Store.
func (s *Store) FindCredit(_ context.Context, creditID string) (work.Credit, error) {
	if err := s.fail("FindCredit"); err != nil {
		return work.Credit{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	credit, ok := s.credits[creditID]
	if !ok {
		return work.Credit{}, work.ErrCreditNotFound
	}
	return credit, nil
}

// LockCredit implements work.Store.
func (s *Store) LockCredit(ctx context.Context, creditID, workID string) (work.Credit, error) {
	if err := s.fail("LockCredit"); err != nil {
		return work.Credit{}, err
	}
	credit, err := s.FindCredit(ctx, creditID)
	if err != nil || credit.WorkID != workID {
		return work.Credit{}, work.ErrCreditNotFound
	}
	return credit, nil
}

// InsertCredit implements work.Store.
func (s *Store) InsertCredit(_ context.Context, credit *work.Credit) error {
	if err := s.fail("InsertCredit"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	credit.ID = uuid.NewString()
	s.credits[credit.ID] = *credit
	return nil
}

// UpdateCredit implements work.Store.
func (s *Store) UpdateCredit(_ context.Context, creditID string, setGroup bool, groupID *string, role *string) error {
	if err := s.fail("UpdateCredit"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	credit, ok := s.credits[creditID]
	if !ok {
		return nil
	}
	if setGroup {
		credit.GroupID = cloneString(groupID)
	}
	if role != nil {
		credit.CreditRole = cloneString(role)
	}
	s.credits[creditID] = credit
	return nil
}

// UngroupCredit implements work.Store.
func (s *Store) UngroupCredit(_ context.Context, creditID, workID string) error {
	if err := s.fail("UngroupCredit"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if credit, ok := s.credits[creditID]; ok && credit.WorkID == workID {
		credit.GroupID = nil
		s.credits[creditID] = credit
	}
	return nil
}

// DeleteCredit implements work.Store.
func (s *Store) DeleteCredit(_ context.Context, creditID string) error {
	if err := s.fail("DeleteCredit"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.credits, creditID)
	return nil
}

// SetGroupSortOrder implements work.Store.
func (s *Store) SetGroupSortOrder(_ context.Context, workID, groupID string, sortOrder int) error {
	if err := s.fail("SetGroupSortOrder"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	group, ok := s.groups[groupID]
	if !ok || group.WorkID != workID {
		return work.ErrRowChanged
	}
	group.SortOrder = sortOrder
	s.groups[groupID] = group
	return nil
}

// SetCreditSortOrder implements work.Store.
func (s *Store) SetCreditSortOrder(_ context.Context, workID, creditID string, groupID *string, sortOrder int) error {
	if err := s.fail("SetCreditSortOrder"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	credit, ok := s.credits[creditID]
	if !ok || credit.WorkID != workID {
		return work.ErrRowChanged
	}
	credit.GroupID, credit.SortOrder = cloneString(groupID), sortOrder
	s.credits[creditID] = credit
	return nil
}

// LockCreditMember implements work.Store with authorizationtarget's
// answers: the Member must be onboarded and not deleted.
func (s *Store) LockCreditMember(_ context.Context, memberID string) error {
	if err := s.fail("LockCreditMember"); err != nil {
		return err
	}
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return domainerr.InvalidField("member_id", "must be a canonical UUID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	member, ok := s.members[memberID]
	if !ok || member.Deleted || member.Unonboarded || member.Unlinked {
		return domainerr.InvalidField("member_id", "not found: "+memberID)
	}
	return nil
}

// CreditArtist implements work.Store.
func (s *Store) CreditArtist(_ context.Context, artistID string) (work.CreditArtistRow, bool, error) {
	if err := s.fail("CreditArtist"); err != nil {
		return work.CreditArtistRow{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	artist, ok := s.artists[artistID]
	return artist, ok, nil
}

// CreditingArtistIDs implements work.Store.
func (s *Store) CreditingArtistIDs(_ context.Context, memberID string) ([]string, error) {
	if err := s.fail("CreditingArtistIDs"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Compact(slices.Sorted(slices.Values(s.artistMembers[memberID]))), nil
}

// ListCreditedWorks implements work.Store, ordered by the Work's
// created_at and the credit's ID, both descending; it takes no filter or
// sort.
func (s *Store) ListCreditedWorks(_ context.Context, page work.CreditedPage) ([]work.CreditedRow, int64, error) {
	if err := s.fail("ListCreditedWorks"); err != nil {
		return nil, 0, err
	}
	if len(page.Filters) != 0 || len(page.Sorts) != 0 {
		return nil, 0, errors.New("fake Work store does not filter or sort credited Works")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []work.CreditedRow
	for _, credit := range s.credits {
		member := credit.MemberID != nil && *credit.MemberID == page.MemberID
		artist := credit.ArtistID != nil && slices.Contains(page.ArtistIDs, *credit.ArtistID)
		value, ok := s.works[credit.WorkID]
		if !ok || (!member && !artist) {
			continue
		}
		row := work.CreditedRow{
			WorkID: value.ID, SourceLocale: value.SourceLocale, Slug: value.Slug, Type: value.Type,
			Status: string(value.Status), CreatedAt: value.CreatedAt, CreditID: credit.ID, CreditRole: credit.CreditRole,
			ArtistID: credit.ArtistID, MemberID: credit.MemberID,
		}
		if source, ok := s.locales[localeKey{value.ID, value.SourceLocale}]; ok && source.Title != nil {
			row.Title = *source.Title
		}
		if credit.ArtistID != nil {
			if credited, ok := s.artists[*credit.ArtistID]; ok {
				row.ArtistName, row.ArtistImageFileID = &credited.Name, cloneString(credited.ImageFileID)
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].CreatedAt.After(rows[j].CreatedAt)
		}
		return rows[i].CreditID > rows[j].CreditID
	})
	return pageOf(rows, page.Limit, page.Offset), int64(len(rows)), nil
}

// ClientIDs implements work.Store; a Work without clients reads as an empty
// list, as the column read does.
func (s *Store) ClientIDs(_ context.Context, workID string) ([]string, error) {
	if err := s.fail("ClientIDs"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := slices.Clone(s.workClients[workID])
	if ids == nil {
		ids = []string{}
	}
	return ids, nil
}

// Clients implements work.Store.
func (s *Store) Clients(_ context.Context, workID string) ([]work.ClientRow, error) {
	if err := s.fail("Clients"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := []work.ClientRow{}
	for _, clientID := range s.workClients[workID] {
		if client, ok := s.clients[clientID]; ok {
			rows = append(rows, client)
		}
	}
	return rows, nil
}

// CountExistingClients implements work.Store.
func (s *Store) CountExistingClients(_ context.Context, clientIDs []string) (int64, error) {
	if err := s.fail("CountExistingClients"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, clientID := range slices.Compact(slices.Sorted(slices.Values(clientIDs))) {
		if _, ok := s.clients[clientID]; ok {
			count++
		}
	}
	return count, nil
}

// ReplaceClients implements work.Store.
func (s *Store) ReplaceClients(_ context.Context, workID string, clientIDs []string) error {
	if err := s.fail("ReplaceClients"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workClients[workID] = slices.Clone(clientIDs)
	return nil
}

// CountVersions implements work.Store.
func (s *Store) CountVersions(_ context.Context, workID string) (int64, error) {
	if err := s.fail("CountVersions"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, version := range s.versions {
		if version.WorkID == workID {
			count++
		}
	}
	return count, nil
}

// ListVersions implements work.Store.
func (s *Store) ListVersions(_ context.Context, workID string, limit, offset int32) ([]work.Version, error) {
	if err := s.fail("ListVersions"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	versions := []work.Version{}
	for _, version := range s.versions {
		if version.WorkID == workID {
			versions = append(versions, version)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Version > versions[j].Version })
	return pageOf(versions, limit, offset), nil
}

// FindVersion implements work.Store.
func (s *Store) FindVersion(_ context.Context, versionID string) (work.Version, error) {
	if err := s.fail("FindVersion"); err != nil {
		return work.Version{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	version, ok := s.versions[versionID]
	if !ok {
		return work.Version{}, work.ErrVersionNotFound
	}
	return version, nil
}

// LockLatestVersion implements work.Store.
func (s *Store) LockLatestVersion(ctx context.Context, workID string) (work.Version, bool, error) {
	if err := s.fail("LockLatestVersion"); err != nil {
		return work.Version{}, false, err
	}
	versions, err := s.ListVersions(ctx, workID, 1, 0)
	if err != nil || len(versions) == 0 {
		return work.Version{}, false, err
	}
	return versions[0], true, nil
}

// InsertVersion implements work.Store.
func (s *Store) InsertVersion(_ context.Context, version *work.Version) error {
	if err := s.fail("InsertVersion"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.versions {
		if existing.WorkID == version.WorkID && existing.Version == version.Version {
			return errDuplicateKey
		}
	}
	version.ID = uuid.NewString()
	s.versions[version.ID] = *version
	return nil
}

// ContributorNicknames implements work.Store; deleted Members keep their
// nickname.
func (s *Store) ContributorNicknames(_ context.Context, memberIDs []string) (map[string]string, error) {
	if err := s.fail("ContributorNicknames"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	nicknames := map[string]string{}
	for _, memberID := range memberIDs {
		if member, ok := s.members[memberID]; ok {
			nicknames[memberID] = member.Nickname
		}
	}
	return nicknames, nil
}

// ReadyAssets implements work.Store.
func (s *Store) ReadyAssets(_ context.Context, assetIDs []string) (map[string]*mediaasset.Reference, error) {
	if err := s.fail("ReadyAssets"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	refs := map[string]*mediaasset.Reference{}
	for _, id := range assetIDs {
		if ref, ok := s.readyAssets[id]; ok {
			refs[id] = ref
		}
	}
	return refs, nil
}

// ReadyFileAsset implements work.Store.
func (s *Store) ReadyFileAsset(_ context.Context, fileID, kind string) (*mediaasset.Reference, error) {
	if err := s.fail("ReadyFileAsset"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fileAssets[fileID+"\x00"+kind], nil
}

// LockAttachableFile implements work.Store with mediaasset's answer for a
// File that no longer exists.
func (s *Store) LockAttachableFile(_ context.Context, fileID string) error {
	if err := s.fail("LockAttachableFile"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.files[fileID] {
		return domainerr.Precondition("one or more files no longer exist")
	}
	return nil
}

// FileID implements work.Store.
func (s *Store) FileID(_ context.Context, fileID string) (string, error) {
	if err := s.fail("FileID"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.files[fileID] {
		return "", work.ErrFileNotFound
	}
	return fileID, nil
}

// BindFeaturedImage implements work.Store; it answers the File's ready
// image asset.
func (s *Store) BindFeaturedImage(_ context.Context, workID, fileID string) (*mediaasset.Reference, error) {
	if err := s.fail("BindFeaturedImage"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings[workID] = fileID
	return s.fileAssets[fileID+"\x00image"], nil
}

// ReleaseFeaturedImage implements work.Store.
func (s *Store) ReleaseFeaturedImage(_ context.Context, workID string) error {
	if err := s.fail("ReleaseFeaturedImage"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bindings, workID)
	return nil
}

// UnavailableAttachmentKinds implements work.Store; every attachment is
// available.
func (s *Store) UnavailableAttachmentKinds(context.Context, *contentv1.LocalizedRichTextDocument) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error) {
	if err := s.fail("UnavailableAttachmentKinds"); err != nil {
		return nil, err
	}
	return map[uuid.UUID]contentv1.MissingAttachmentMediaKind{}, nil
}

// HydrateBlockMedia implements work.Store by returning the items unchanged.
func (s *Store) HydrateBlockMedia(_ context.Context, _ string, _ uuid.UUID, _ *auth.UserInfo, items []*fileref.BlockMediaItem) ([]*fileref.BlockMediaItem, error) {
	if err := s.fail("HydrateBlockMedia"); err != nil {
		return nil, err
	}
	return items, nil
}

// RequestOG implements work.Store.
func (s *Store) RequestOG(_ context.Context, workID, locale, reason string) (string, error) {
	if err := s.fail("RequestOG"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ogRequests = append(s.ogRequests, OGRequest{WorkID: workID, Locale: locale, Reason: reason})
	return s.OGRunID, nil
}

// CancelOG implements work.Store.
func (s *Store) CancelOG(_ context.Context, workID string) error {
	if err := s.fail("CancelOG"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ogCancels = append(s.ogCancels, workID)
	return nil
}

// RequireFreshCan implements work.Store; the fake fence takes no
// transaction.
func (s *Store) RequireFreshCan(ctx context.Context, fence identitystate.Fence, checker authz.AuthorizationDecisionChecker, can policyv1.Can) error {
	if err := s.fail("RequireFreshCan"); err != nil {
		return err
	}
	return identitystate.RequireFreshCan(ctx, nil, fence, checker, can)
}

// ResolveSessionPrincipal implements work.Store.
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

// LockSession implements work.Store.
func (s *Store) LockSession(_ context.Context, sessionID string, principal *auth.UserInfo) error {
	if err := s.fail("LockSession"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.sessions[sessionID]; !ok || current != principal {
		return auth.ErrSessionPrincipalInvalid
	}
	s.lockedSessions = append(s.lockedSessions, sessionID)
	return nil
}

// RequireCurrentContributors implements work.Store.
func (s *Store) RequireCurrentContributors(_ context.Context, workID string, _ []string) error {
	if err := s.fail("RequireCurrentContributors"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revokedRoom[workID] {
		return domainerr.Denied("collaboration contributors may no longer edit this work")
	}
	return nil
}

// LockContributors implements work.Store: every Member row counts, deleted
// or not.
func (s *Store) LockContributors(_ context.Context, memberIDs []string) ([]string, error) {
	if err := s.fail("LockContributors"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	locked := []string{}
	for _, memberID := range memberIDs {
		if _, ok := s.members[memberID]; ok {
			locked = append(locked, memberID)
		}
	}
	return locked, nil
}

// PublishContentUpdate implements work.Store.
func (s *Store) PublishContentUpdate(_ context.Context, update work.ContentUpdate) error {
	if err := s.fail("PublishContentUpdate"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, update)
	return nil
}
