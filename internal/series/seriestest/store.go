// Package seriestest provides the in-memory Series Store, the Series
// authorization port over authztest.Checker, and the Store contract that
// both the fake and internal/series/postgres keep.
package seriestest

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/series"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Store is the in-memory series.Store. It knows no schema and takes no lock;
// the unit of work serializes its transactions. Its sentinels and orderings
// are the PostgreSQL store's.
type Store struct {
	mu    sync.Mutex
	state state
}

// state is everything a Savepoint captures.
type state struct {
	series       map[string]series.Series
	translations map[translationKey]series.Translation
	documents    map[string]series.ContentDocumentRow
	managers     map[managerKey]manager
	members      map[string]Member
	occupied     map[string]bool
	posts        map[string]Post
	files        map[string]bool
	images       map[string]*series.Asset
	featured     map[string]string
	ogReleased   map[string]bool
	ogRequests   []OGRequest
	menu         []MenuChange
	signals      []series.ContentSignal
	contributors map[string]error
	failures     map[string]error
	runs         int
}

type translationKey struct{ seriesID, locale string }
type managerKey struct{ seriesID, memberID string }

type manager struct {
	createdAt, updatedAt time.Time
}

// Member is a manager change target: the Identity it locks and whether it is
// still linked to it.
type Member struct {
	IdentityID string
	Unlinked   bool
}

// Post is a Post as the Series Post port sees it.
type Post struct {
	series.SeriesPost
	SeriesID  *string
	UpdatedAt time.Time
	// EditErr is the Post edit decision's answer, nil when allowed.
	EditErr error
}

// OGRequest is one OG image request a Series transaction made.
type OGRequest struct {
	SeriesID   string
	Locale     string
	AllLocales bool
	Reason     string
}

// MenuChange is one Menu target rewrite: a rename, or a removal when NewSlug
// is empty.
type MenuChange struct {
	SeriesID, OldSlug, NewSlug string
}

// New returns an empty fake store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[series.Store]) {
	store := &Store{state: state{
		series: map[string]series.Series{}, translations: map[translationKey]series.Translation{},
		documents: map[string]series.ContentDocumentRow{}, managers: map[managerKey]manager{},
		members: map[string]Member{}, occupied: map[string]bool{}, posts: map[string]Post{},
		files: map[string]bool{}, images: map[string]*series.Asset{}, featured: map[string]string{},
		ogReleased: map[string]bool{}, contributors: map[string]error{}, failures: map[string]error{},
	}}
	return store, mutationtest.New[series.Store](store)
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved := s.state.clone()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		saved.failures = s.state.failures
		s.state = saved
	}
}

func (st state) clone() state {
	posts := make(map[string]Post, len(st.posts))
	for id, post := range st.posts {
		post.SeriesID = clonePointer(post.SeriesID)
		post.SeriesOrder = clonePointer(post.SeriesOrder)
		posts[id] = post
	}
	return state{
		series: maps.Clone(st.series), translations: maps.Clone(st.translations), documents: maps.Clone(st.documents),
		managers: maps.Clone(st.managers), members: maps.Clone(st.members), occupied: maps.Clone(st.occupied),
		posts: posts, files: maps.Clone(st.files), images: maps.Clone(st.images), featured: maps.Clone(st.featured),
		ogReleased: maps.Clone(st.ogReleased), ogRequests: slices.Clone(st.ogRequests), menu: slices.Clone(st.menu),
		signals: slices.Clone(st.signals), contributors: maps.Clone(st.contributors), failures: st.failures, runs: st.runs,
	}
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// Fail makes the named Store method answer err until cleared with nil.
func (s *Store) Fail(method string, err error) *Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.state.failures, method)
	} else {
		s.state.failures[method] = err
	}
	return s
}

func (s *Store) failure(method string) error {
	return s.state.failures[method]
}

// SeedSeries stores a Series root, its source copy in its source locale and
// its compact content document. Zero fields take defaults: a new ID, slug
// "series-<id>", a draft status, source locale "en", and a fresh document.
func (s *Store) SeedSeries(root series.Series) series.Series {
	s.mu.Lock()
	defer s.mu.Unlock()
	if root.ID == "" {
		root.ID = uuid.NewString()
	}
	if root.Slug == "" {
		root.Slug = "series-" + root.ID
	}
	if root.Status == "" {
		root.Status = series.StatusDraft
	}
	if root.SourceLocale == "" {
		root.SourceLocale = "en"
	}
	if root.ContentDocumentID == "" {
		root.ContentDocumentID = uuid.NewString()
	}
	if root.CreatedAt.IsZero() {
		root.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if _, ok := s.state.documents[root.ContentDocumentID]; !ok {
		revision := uuid.NewString()
		s.state.documents[root.ContentDocumentID] = series.ContentDocumentRow{Profile: "compact", Revision: &revision}
	}
	title := root.Title
	key := translationKey{root.ID, root.SourceLocale}
	if _, ok := s.state.translations[key]; !ok {
		s.state.translations[key] = series.Translation{
			SeriesID: root.ID, Locale: root.SourceLocale, Title: &title, Summary: root.Description,
			ContentText: root.Description, CreatedAt: root.CreatedAt, UpdatedAt: root.CreatedAt, OgAssetID: root.OgAssetID,
		}
	}
	stored := root
	stored.Title, stored.Description, stored.OgAssetID = "", nil, nil
	s.state.series[root.ID] = stored
	return stored
}

// SeedTranslation stores a locale row.
func (s *Store) SeedTranslation(translation series.Translation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.translations[translationKey{translation.SeriesID, translation.Locale}] = translation
}

// SeedMember makes memberID a manager change target.
func (s *Store) SeedMember(memberID string, member Member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.members[memberID] = member
}

// SeedManager attributes memberID as a manager of seriesID.
func (s *Store) SeedManager(seriesID, memberID string, createdAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.managers[managerKey{seriesID, memberID}] = manager{createdAt: createdAt, updatedAt: createdAt}
}

// SeedPost stores a Post, in a Series when SeriesID is set.
func (s *Store) SeedPost(post Post) Post {
	s.mu.Lock()
	defer s.mu.Unlock()
	if post.ID == "" {
		post.ID = uuid.NewString()
	}
	s.state.posts[post.ID] = post
	return post
}

// SeedFile makes fileID attachable, its ready image asset when image is set.
func (s *Store) SeedFile(fileID string, image *series.Asset) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.files[fileID] = true
	if image != nil {
		s.state.images[fileID] = image
	}
}

// OccupyRoute makes a Page occupy the Series route of slug.
func (s *Store) OccupyRoute(slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.occupied[slug] = true
}

// RejectContributors makes RequireCurrentContributors answer err for
// seriesID.
func (s *Store) RejectContributors(seriesID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.contributors[seriesID] = err
}

// Stored returns a Series root as stored, without its copy.
func (s *Store) Stored(seriesID string) (series.Series, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, ok := s.state.series[seriesID]
	return root, ok
}

// StoredTranslation returns a stored locale row.
func (s *Store) StoredTranslation(seriesID, locale string) (series.Translation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	translation, ok := s.state.translations[translationKey{seriesID, locale}]
	return translation, ok
}

// Revision returns the current revision of a Series content document.
func (s *Store) Revision(seriesID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.state.documents[s.state.series[seriesID].ContentDocumentID]
	if !ok || document.Revision == nil {
		return ""
	}
	return *document.Revision
}

// Documents reports how many content documents exist.
func (s *Store) Documents() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.state.documents)
}

// Managed reports whether memberID is attributed as a manager of seriesID.
func (s *Store) Managed(seriesID, memberID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.state.managers[managerKey{seriesID, memberID}]
	return ok
}

// StoredPost returns a stored Post.
func (s *Store) StoredPost(postID string) Post {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.posts[postID]
}

// Order returns the Posts of seriesID in series order.
func (s *Store) Order(seriesID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.seriesPostIDs(seriesID)
}

// FeaturedBinding returns the File bound as the featured image of seriesID.
func (s *Store) FeaturedBinding(seriesID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fileID, ok := s.state.featured[seriesID]
	return fileID, ok
}

// OGReleased reports whether the OG generations of seriesID were cancelled.
func (s *Store) OGReleased(seriesID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.ogReleased[seriesID]
}

// OGRequests returns the committed OG image requests, in order.
func (s *Store) OGRequests() []OGRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.state.ogRequests)
}

// MenuChanges returns the committed Menu target rewrites, in order.
func (s *Store) MenuChanges() []MenuChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.state.menu)
}

// Signals returns the committed content signals, in order.
func (s *Store) Signals() []series.ContentSignal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.state.signals)
}

func (s *Store) LockSeries(_ context.Context, seriesID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("LockSeries"); err != nil {
		return err
	}
	if _, ok := s.state.series[seriesID]; !ok {
		return series.ErrSeriesNotFound
	}
	return nil
}

func (s *Store) SourceCopies(_ context.Context, seriesIDs []string) (map[string]series.SourceCopy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("SourceCopies"); err != nil {
		return nil, err
	}
	copies := map[string]series.SourceCopy{}
	for _, id := range seriesIDs {
		root, ok := s.state.series[id]
		if !ok {
			continue
		}
		translation, ok := s.state.translations[translationKey{id, root.SourceLocale}]
		if !ok {
			continue
		}
		copies[id] = series.SourceCopy{
			Title: clonePointer(translation.Title), Summary: clonePointer(translation.Summary),
			ContentText: clonePointer(translation.Summary), OgAssetID: clonePointer(translation.OgAssetID),
		}
	}
	return copies, nil
}

func (s *Store) Translation(_ context.Context, seriesID, locale string, _ bool) (series.Translation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	translation, ok := s.state.translations[translationKey{seriesID, locale}]
	if !ok {
		return series.Translation{}, series.ErrTranslationNotFound
	}
	return translation, nil
}

func (s *Store) UpsertTranslation(_ context.Context, seriesID, locale string, write series.TranslationWrite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("UpsertTranslation"); err != nil {
		return err
	}
	key := translationKey{seriesID, locale}
	translation, ok := s.state.translations[key]
	if !ok {
		translation = series.Translation{SeriesID: seriesID, Locale: locale, CreatedAt: write.Now}
	}
	translation.Title, translation.Summary = clonePointer(write.Title), clonePointer(write.Summary)
	translation.ContentJSON, translation.ContentHTML = slices.Clone(write.ContentJSON), clonePointer(write.ContentHTML)
	translation.ContentText, translation.UpdatedAt = clonePointer(write.ContentText), write.Now
	s.state.translations[key] = translation
	return nil
}

func (s *Store) InsertTranslation(_ context.Context, translation series.Translation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("InsertTranslation"); err != nil {
		return err
	}
	key := translationKey{translation.SeriesID, translation.Locale}
	if _, ok := s.state.translations[key]; ok {
		return errors.New("duplicate key value violates unique constraint series_translation_pkey")
	}
	s.state.translations[key] = translation
	return nil
}

func (s *Store) UpdateTranslation(_ context.Context, seriesID, locale string, title, summary, contentText *string, updatedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("UpdateTranslation"); err != nil {
		return err
	}
	key := translationKey{seriesID, locale}
	translation, ok := s.state.translations[key]
	if !ok {
		return series.ErrTranslationNotFound
	}
	translation.Title, translation.Summary = clonePointer(title), clonePointer(summary)
	translation.ContentText, translation.UpdatedAt = clonePointer(contentText), updatedAt
	s.state.translations[key] = translation
	return nil
}

func (s *Store) DeleteTranslation(_ context.Context, seriesID, locale string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := translationKey{seriesID, locale}
	if _, ok := s.state.translations[key]; !ok {
		return series.ErrTranslationNotFound
	}
	delete(s.state.translations, key)
	return nil
}

func (s *Store) SetSourceLocale(_ context.Context, seriesID, locale string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if root, ok := s.state.series[seriesID]; ok {
		root.SourceLocale, root.UpdatedAt = locale, &now
		s.state.series[seriesID] = root
	}
	return nil
}

func (s *Store) ContentDocumentRoot(_ context.Context, seriesID string, _ bool) (series.ContentDocumentRoot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, ok := s.state.series[seriesID]
	if !ok {
		return series.ContentDocumentRoot{}, series.ErrSeriesNotFound
	}
	var documentID *string
	if root.ContentDocumentID != "" {
		documentID = &root.ContentDocumentID
	}
	return series.ContentDocumentRoot{SourceLocale: root.SourceLocale, ContentDocumentID: clonePointer(documentID)}, nil
}

func (s *Store) ContentDocument(_ context.Context, documentID string, _ bool) (series.ContentDocumentRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.state.documents[documentID]
	if !ok {
		return series.ContentDocumentRow{}, series.ErrContentDocumentNotFound
	}
	return series.ContentDocumentRow{Profile: document.Profile, Revision: clonePointer(document.Revision)}, nil
}

func (s *Store) AdvanceContentDocument(_ context.Context, seriesID, documentID, expected, next string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("AdvanceContentDocument"); err != nil {
		return err
	}
	document, ok := s.state.documents[documentID]
	root, rooted := s.state.series[seriesID]
	if !ok || !rooted || root.ContentDocumentID != documentID || document.Revision == nil || *document.Revision != expected {
		return series.ErrContentDocumentRevisionChanged
	}
	document.Revision = &next
	s.state.documents[documentID] = document
	return nil
}

func (s *Store) AppendRequestAudit(ctx context.Context, appender domainaudit.Appender, action sharedtelemetry.AuditAction, build domainaudit.Builder) error {
	return domainaudit.AppendRequest(ctx, nil, appender, action, build)
}

func (s *Store) AppendMemberAudit(ctx context.Context, appender domainaudit.Appender, memberID string, action sharedtelemetry.AuditAction, build domainaudit.Builder) error {
	return domainaudit.AppendMember(ctx, nil, appender, memberID, action, build)
}

func (s *Store) Series(_ context.Context, seriesID string) (series.Series, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("Series"); err != nil {
		return series.Series{}, err
	}
	root, ok := s.state.series[seriesID]
	if !ok {
		return series.Series{}, series.ErrSeriesNotFound
	}
	return root, nil
}

func (s *Store) roots(seriesIDs []string) []series.Series {
	wanted := map[string]bool{}
	for _, id := range seriesIDs {
		wanted[id] = true
	}
	var roots []series.Series
	for id, root := range s.state.series {
		if wanted[id] {
			roots = append(roots, root)
		}
	}
	return roots
}

func page(roots []series.Series, limit, offset int32) []series.Series {
	start := min(int(offset), len(roots))
	end := min(start+int(limit), len(roots))
	return roots[start:end]
}

func (s *Store) SeriesPage(_ context.Context, seriesIDs []string, limit, offset int32) ([]series.Series, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	roots := s.roots(seriesIDs)
	slices.SortFunc(roots, func(a, b series.Series) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	return page(roots, limit, offset), int64(len(roots)), nil
}

// AdminSeriesPage applies the status filter and the title and creation
// sorts the management list uses; other filters and sorts are the SQL's.
func (s *Store) AdminSeriesPage(_ context.Context, query series.ListSeriesAdminQuery, limit, offset int32) ([]series.Series, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("AdminSeriesPage"); err != nil {
		return nil, 0, err
	}
	var roots []series.Series
	for _, root := range s.state.series {
		if s.state.matches(root, query) {
			roots = append(roots, root)
		}
	}
	byTitle := len(query.Sorts) != 0 && query.Sorts[0].GetField() == "title"
	slices.SortFunc(roots, func(a, b series.Series) int {
		if byTitle {
			return cmp.Or(strings.Compare(s.state.title(a), s.state.title(b)), strings.Compare(a.ID, b.ID))
		}
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	return page(roots, limit, offset), int64(len(roots)), nil
}

func (st state) title(root series.Series) string {
	if translation, ok := st.translations[translationKey{root.ID, root.SourceLocale}]; ok && translation.Title != nil {
		return *translation.Title
	}
	return ""
}

func (st state) matches(root series.Series, query series.ListSeriesAdminQuery) bool {
	for _, filter := range query.Filters {
		switch filter.GetField() {
		case "status":
			if string(root.Status) != filter.GetValue() {
				return false
			}
		case "search":
			if !strings.Contains(strings.ToLower(st.title(root)), strings.ToLower(filter.GetValue())) {
				return false
			}
		}
	}
	return true
}

func (s *Store) SeriesByCreation(_ context.Context, seriesIDs []string) ([]series.Series, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	roots := s.roots(seriesIDs)
	slices.SortStableFunc(roots, func(a, b series.Series) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	return roots, nil
}

func (s *Store) SlugTaken(_ context.Context, slug, excludeID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.slugTaken(slug, excludeID), nil
}

func (st state) slugTaken(slug, excludeID string) bool {
	for id, root := range st.series {
		if root.Slug == slug && id != excludeID {
			return true
		}
	}
	return false
}

func (s *Store) InsertSeries(_ context.Context, created series.Series) (series.Series, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("InsertSeries"); err != nil {
		return series.Series{}, err
	}
	if s.state.slugTaken(created.Slug, "") {
		return series.Series{}, series.ErrSlugTaken
	}
	created.ID = uuid.NewString()
	s.state.series[created.ID] = created
	return created, nil
}

func (s *Store) UpdateSeriesRoot(_ context.Context, seriesID string, slug *string, status *series.Status, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("UpdateSeriesRoot"); err != nil {
		return err
	}
	root, ok := s.state.series[seriesID]
	if !ok {
		return nil
	}
	if slug != nil {
		if s.state.slugTaken(*slug, seriesID) {
			return series.ErrSlugTaken
		}
		root.Slug = *slug
	}
	if status != nil {
		root.Status = *status
	}
	root.UpdatedAt = &now
	s.state.series[seriesID] = root
	return nil
}

func (s *Store) SetFeaturedImage(_ context.Context, seriesID string, fileID *string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if root, ok := s.state.series[seriesID]; ok {
		root.FeaturedImageFileID, root.UpdatedAt = clonePointer(fileID), &now
		s.state.series[seriesID] = root
	}
	return nil
}

// DeleteSeries deletes the root and, as the foreign keys cascade, its
// manager attributions and locale rows.
func (s *Store) DeleteSeries(_ context.Context, seriesID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state.series, seriesID)
	for key := range s.state.managers {
		if key.seriesID == seriesID {
			delete(s.state.managers, key)
		}
	}
	for key := range s.state.translations {
		if key.seriesID == seriesID {
			delete(s.state.translations, key)
		}
	}
	return nil
}

func (s *Store) CreateContentDocument(_ context.Context, documentID, revision string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.documents[documentID] = series.ContentDocumentRow{Profile: "compact", Revision: &revision}
	return nil
}

func (s *Store) DeleteContentDocument(_ context.Context, documentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.state.documents[documentID]; !ok {
		return series.ErrContentDocumentNotFound
	}
	delete(s.state.documents, documentID)
	return nil
}

func (s *Store) ManagerAttributions(_ context.Context, seriesID string) ([]series.ManagerAttribution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var attributions []series.ManagerAttribution
	for key, attribution := range s.state.managers {
		if key.seriesID == seriesID {
			attributions = append(attributions, series.ManagerAttribution{MemberID: key.memberID, CreatedAt: attribution.createdAt})
		}
	}
	slices.SortFunc(attributions, func(a, b series.ManagerAttribution) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.MemberID, b.MemberID))
	})
	return attributions, nil
}

func (s *Store) ManagerAttributed(_ context.Context, seriesID, memberID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.state.managers[managerKey{seriesID, memberID}]
	return ok, nil
}

func (s *Store) UpsertManagerAttribution(_ context.Context, seriesID, memberID string, updatedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := managerKey{seriesID, memberID}
	attribution, ok := s.state.managers[key]
	if !ok {
		attribution.createdAt = updatedAt
	}
	attribution.updatedAt = updatedAt
	s.state.managers[key] = attribution
	return nil
}

func (s *Store) DeleteManagerAttribution(_ context.Context, seriesID, memberID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state.managers, managerKey{seriesID, memberID})
	return nil
}

func (s *Store) target(memberID string, linked bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	member, ok := s.state.members[memberID]
	if !ok || (linked && member.Unlinked) {
		return "", domainerr.ResourceNotFound("member", memberID)
	}
	return member.IdentityID, nil
}

func (s *Store) LinkedMember(_ context.Context, memberID string) error {
	_, err := s.target(memberID, true)
	return err
}

func (s *Store) LockManagerTarget(_ context.Context, memberID string) (string, error) {
	return s.target(memberID, false)
}

func (s *Store) LockLinkedManagerTarget(_ context.Context, memberID string) (string, error) {
	return s.target(memberID, true)
}

func (s *Store) EnsureRouteAvailable(_ context.Context, slug string, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.occupied[slug] {
		return domainerr.Duplicate("series", "slug", slug)
	}
	return nil
}

func (s *Store) RouteAvailable(_ context.Context, slug string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.state.occupied[slug], nil
}

func (s *Store) Posts() series.Posts {
	return posts{s}
}

func (s *Store) RequireAttachableFile(_ context.Context, fileID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.state.files[fileID] {
		return series.ErrFileNotFound
	}
	return nil
}

func (s *Store) BindFeaturedImage(_ context.Context, seriesID, fileID string) (*series.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.featured[seriesID] = fileID
	return s.state.images[fileID], nil
}

func (s *Store) ReleaseFeaturedImage(_ context.Context, seriesID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state.featured, seriesID)
	return nil
}

func (s *Store) CancelAndReleaseOG(_ context.Context, seriesID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.ogReleased[seriesID] = true
	return nil
}

// RequestOG records the request and starts a run named after its order.
func (s *Store) RequestOG(_ context.Context, seriesID, locale string, allLocales bool, reason string) (*string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("RequestOG"); err != nil {
		return nil, err
	}
	s.state.ogRequests = append(s.state.ogRequests, OGRequest{SeriesID: seriesID, Locale: locale, AllLocales: allLocales, Reason: reason})
	s.state.runs++
	runID := "run-" + reason
	return &runID, nil
}

func (s *Store) RenameMenuTargets(_ context.Context, seriesID, oldSlug, newSlug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.menu = append(s.state.menu, MenuChange{SeriesID: seriesID, OldSlug: oldSlug, NewSlug: newSlug})
	return nil
}

func (s *Store) RemoveMenuTargets(_ context.Context, seriesID, slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.menu = append(s.state.menu, MenuChange{SeriesID: seriesID, OldSlug: slug})
	return nil
}

func (s *Store) RequireCurrentContributors(_ context.Context, seriesID string, _ []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.contributors[seriesID]
}

// PublishContentSignal records a changed signal, as only changes publish.
func (s *Store) PublishContentSignal(_ context.Context, signal series.ContentSignal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if signal.Changed {
		s.state.signals = append(s.state.signals, signal)
	}
	return nil
}

// posts is the Post port of the fake store.
type posts struct{ s *Store }

func (st state) seriesPostIDs(seriesID string) []string {
	var ids []string
	for id, post := range st.posts {
		if post.SeriesID != nil && *post.SeriesID == seriesID {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := st.posts[ids[i]].SeriesOrder, st.posts[ids[j]].SeriesOrder
		if left != nil && right != nil && *left != *right {
			return *left < *right
		}
		return ids[i] < ids[j]
	})
	return ids
}

func (p posts) SeriesPosts(_ context.Context, seriesID string) ([]series.SeriesPost, error) {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	listed := []series.SeriesPost{}
	for _, id := range p.s.state.seriesPostIDs(seriesID) {
		post := p.s.state.posts[id]
		listed = append(listed, post.SeriesPost)
		listed[len(listed)-1].ID = id
		listed[len(listed)-1].SeriesOrder = clonePointer(post.SeriesOrder)
	}
	return listed, nil
}

func (p posts) SeriesPostIDs(_ context.Context, seriesID string) ([]string, error) {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	return p.s.state.seriesPostIDs(seriesID), nil
}

func (p posts) SeriesOf(_ context.Context, postID string) (*string, error) {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	post, ok := p.s.state.posts[postID]
	if !ok {
		return nil, series.ErrPostNotFound
	}
	return clonePointer(post.SeriesID), nil
}

func (p posts) LockSeriesOf(ctx context.Context, postID string) (*string, error) {
	return p.SeriesOf(ctx, postID)
}

func (p posts) LockPosts(_ context.Context, postIDs []string) (int, error) {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	locked := 0
	for _, id := range postIDs {
		if _, ok := p.s.state.posts[id]; ok {
			locked++
		}
	}
	return locked, nil
}

func (p posts) NextOrder(_ context.Context, seriesID string) (int, error) {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	next := 0
	for _, post := range p.s.state.posts {
		if post.SeriesID != nil && *post.SeriesID == seriesID && post.SeriesOrder != nil && *post.SeriesOrder >= next {
			next = *post.SeriesOrder + 1
		}
	}
	return next, nil
}

func (p posts) Assign(_ context.Context, postID, seriesID string, order int, now time.Time) error {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	post, ok := p.s.state.posts[postID]
	if !ok {
		return series.ErrPostNotFound
	}
	post.SeriesID, post.SeriesOrder, post.UpdatedAt = &seriesID, &order, now
	p.s.state.posts[postID] = post
	return nil
}

func (p posts) Clear(_ context.Context, postID, seriesID string, now time.Time) error {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	post, ok := p.s.state.posts[postID]
	if !ok || post.SeriesID == nil || *post.SeriesID != seriesID {
		return series.ErrPostRelationChanged
	}
	post.SeriesID, post.SeriesOrder, post.UpdatedAt = nil, nil, now
	p.s.state.posts[postID] = post
	return nil
}

func (p posts) ClearAll(_ context.Context, seriesID string, now time.Time) error {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	for id, post := range p.s.state.posts {
		if post.SeriesID != nil && *post.SeriesID == seriesID {
			post.SeriesID, post.SeriesOrder, post.UpdatedAt = nil, nil, now
			p.s.state.posts[id] = post
		}
	}
	return nil
}

// Order moves each Post to its index; a Post that keeps its position keeps
// its updated_at.
func (p posts) Order(_ context.Context, seriesID string, postIDs []string, now time.Time) error {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	for index, id := range postIDs {
		post, ok := p.s.state.posts[id]
		if !ok || post.SeriesID == nil || *post.SeriesID != seriesID {
			return series.ErrPostRelationChanged
		}
		if post.SeriesOrder == nil || *post.SeriesOrder != index {
			post.UpdatedAt = now
		}
		order := index
		post.SeriesOrder = &order
		p.s.state.posts[id] = post
	}
	return nil
}

func (p posts) RequireLockedEdit(_ context.Context, postID string) error {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	return p.s.state.posts[postID].EditErr
}

var _ series.Store = (*Store)(nil)
