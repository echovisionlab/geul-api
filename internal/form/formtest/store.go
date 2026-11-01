// Package formtest holds the in-memory form.Store and public.Store the Form
// use-case tests run on, and the Store contracts they share with
// formpostgres and formpublicpostgres. The fake knows no SQL and takes no
// lock; it keeps the sentinels, orderings and row counts the PostgreSQL
// stores answer with, and a content document whose revision advances like
// the content store's.
package formtest

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/form/public"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// OGRequest is one OG generation the Store started.
type OGRequest struct {
	FormID     string
	Locale     string
	AllLocales bool
	Reason     string
}

// Binding is one featured image the Store bound.
type Binding struct {
	FormID string
	FileID string
}

// LocaleKey names one stored locale of a Form.
type LocaleKey struct{ FormID, Locale string }

// StoredLocale is one stored locale document.
type StoredLocale struct {
	Title       *string
	ContentJSON []byte
	ContentText *string
	OgAssetID   *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	version     int
}

type state struct {
	forms         map[string]form.Form
	locales       map[LocaleKey]StoredLocale
	submissions   map[string]form.Submission
	documents     map[uuid.UUID]uuid.UUID
	routes        map[string]bool
	files         map[string]bool
	images        map[string]*form.Asset
	readyAssets   map[string]*form.Asset
	preferred     map[string]*string
	shareLinks    map[string]public.ShareLink
	dispositions  map[LocaleKey]form.LocalizedOGDisposition
	updates       []form.ContentUpdate
	ogRequests    []OGRequest
	ogCancels     []string
	shareDeletes  []string
	released      []string
	bindings      []Binding
	lockedFiles   []string
	lockedRoutes  []string
	contributions []string
	version       int
}

func (s state) clone() state {
	clone := s
	clone.forms = maps.Clone(s.forms)
	clone.locales = maps.Clone(s.locales)
	clone.submissions = maps.Clone(s.submissions)
	clone.documents = maps.Clone(s.documents)
	clone.updates = slices.Clone(s.updates)
	clone.ogRequests = slices.Clone(s.ogRequests)
	clone.ogCancels = slices.Clone(s.ogCancels)
	clone.shareDeletes = slices.Clone(s.shareDeletes)
	clone.released = slices.Clone(s.released)
	clone.bindings = slices.Clone(s.bindings)
	clone.lockedFiles = slices.Clone(s.lockedFiles)
	clone.lockedRoutes = slices.Clone(s.lockedRoutes)
	clone.contributions = slices.Clone(s.contributions)
	return clone
}

// Store is the in-memory form.Store and public.Store. Seed it with the Put
// methods; read it back with the getters.
type Store struct {
	mu sync.Mutex
	state
	// Settings are the localization settings RuntimeSettings answers.
	Settings translation.RuntimeSettings
	// OGRunID is the run ID RequestOG returns.
	OGRunID string
	// Contributors fails RequireCurrentContributor for the Members it
	// names with their error.
	Contributors map[string]error
	// Fail fails the Store method of that name with its error.
	Fail map[string]error
}

var (
	_ form.Store               = (*Store)(nil)
	_ public.Store             = (*Store)(nil)
	_ mutationtest.Savepointer = (*Store)(nil)
)

// NewStore returns an empty fake Store.
func NewStore() *Store {
	return &Store{
		state: state{
			forms: map[string]form.Form{}, locales: map[LocaleKey]StoredLocale{}, submissions: map[string]form.Submission{},
			documents: map[uuid.UUID]uuid.UUID{}, routes: map[string]bool{}, files: map[string]bool{},
			images: map[string]*form.Asset{}, readyAssets: map[string]*form.Asset{}, preferred: map[string]*string{},
			shareLinks: map[string]public.ShareLink{}, dispositions: map[LocaleKey]form.LocalizedOGDisposition{},
		},
		Settings: translation.DefaultRuntimeSettings(),
		OGRunID:  "og-run",
	}
}

// New returns a fake Store and the unit of work the Form use cases run it
// under.
func New() (*Store, *mutationtest.UnitOfWork[form.Store]) {
	store := NewStore()
	return store, mutationtest.New[form.Store](store)
}

// NewPublic returns a fake Store and the unit of work the public Form use
// cases run it under.
func NewPublic() (*Store, *mutationtest.UnitOfWork[public.Store]) {
	store := NewStore()
	return store, mutationtest.New[public.Store](store)
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() func() {
	s.mu.Lock()
	saved := s.state.clone()
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.state = saved
		s.mu.Unlock()
	}
}

func (s *Store) fail(method string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Fail[method]
}

// PutForm stores a Form as it is, a new ID and content document when it
// has none, and returns it.
func (s *Store) PutForm(value form.Form) form.Form {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value.ID == "" {
		value.ID = uuid.NewString()
	}
	if value.ContentDocumentID == "" {
		value.ContentDocumentID = uuid.NewString()
	}
	if documentID, err := uuid.Parse(value.ContentDocumentID); err == nil {
		if _, exists := s.documents[documentID]; !exists {
			s.documents[documentID] = uuid.New()
		}
	}
	if value.Status == "" {
		value.Status = form.StatusDraft
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	}
	s.forms[value.ID] = value
	return value
}

// PutLocale stores a locale document.
func (s *Store) PutLocale(formID, locale string, value StoredLocale) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value.UpdatedAt.IsZero() {
		value.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = value.UpdatedAt
	}
	s.version++
	value.version = s.version
	s.locales[LocaleKey{formID, locale}] = value
}

// PutSubmission stores a submission, with a new ID when it has none, and
// returns it.
func (s *Store) PutSubmission(value form.Submission) form.Submission {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value.ID == "" {
		value.ID = uuid.NewString()
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	}
	s.submissions[value.ID] = value
	return value
}

// PutRoute marks slug as another resource's route.
func (s *Store) PutRoute(slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[slug] = true
}

// PutFile stores an attachable File whose ready image is image.
func (s *Store) PutFile(fileID string, image *form.Asset) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[fileID] = true
	s.images[fileID] = image
}

// PutReadyAsset stores a ready public asset.
func (s *Store) PutReadyAsset(asset *form.Asset) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readyAssets[asset.ID] = asset
}

// PutPreferredLocale stores a Member's preferred locale.
func (s *Store) PutPreferredLocale(memberID, locale string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preferred[memberID] = &locale
}

// PutShareLink stores the share link of token.
func (s *Store) PutShareLink(token string, link public.ShareLink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shareLinks[token] = link
}

// PutOGDisposition stores the disposition of a locale's own OG image.
func (s *Store) PutOGDisposition(formID, locale string, disposition form.LocalizedOGDisposition) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dispositions[LocaleKey{formID, locale}] = disposition
}

// Stored returns the stored Form and whether it exists.
func (s *Store) Stored(formID string) (form.Form, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.forms[formID]
	return value, ok
}

// StoredLocale returns the stored locale and whether it exists.
func (s *Store) StoredLocale(formID, locale string) (StoredLocale, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.locales[LocaleKey{formID, locale}]
	return value, ok
}

// StoredSubmissions returns the stored submissions of formID, newest first.
func (s *Store) StoredSubmissions(formID string) []form.Submission {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submissionsOf(formID)
}

// DocumentRevisionOf returns the current revision of a content document.
func (s *Store) DocumentRevisionOf(documentID string) uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.documents[uuid.MustParse(documentID)]
}

// ContentUpdates returns the content.updated signals scheduled so far.
func (s *Store) ContentUpdates() []form.ContentUpdate {
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

// OGCancels returns the Forms whose OG generations were cancelled.
func (s *Store) OGCancels() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ogCancels)
}

// ShareDeletes returns the Forms whose share links were deleted.
func (s *Store) ShareDeletes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.shareDeletes)
}

// Released returns the Forms whose featured image was released.
func (s *Store) Released() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.released)
}

// Bindings returns the featured images bound so far.
func (s *Store) Bindings() []Binding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.bindings)
}

// LockedRoutes returns the routes locked so far.
func (s *Store) LockedRoutes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.lockedRoutes)
}

// Contributions returns the contributors whose authority was rechecked.
func (s *Store) Contributions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.contributions)
}

// FormRows

func (s *Store) FindForm(_ context.Context, locator form.Locator, value string) (form.Form, error) {
	if err := s.fail("FindForm"); err != nil {
		return form.Form{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if locator == form.ByID {
		if found, ok := s.forms[strings.ToLower(value)]; ok {
			return found, nil
		}
		return form.Form{}, form.ErrFormNotFound
	}
	for _, found := range s.sortedForms() {
		if found.Slug != nil && *found.Slug == value {
			return found, nil
		}
	}
	return form.Form{}, form.ErrFormNotFound
}

// sortedForms lists the Forms by ID, the order of a first match.
func (s *Store) sortedForms() []form.Form {
	forms := slices.Collect(maps.Values(s.forms))
	sort.Slice(forms, func(i, j int) bool { return forms[i].ID < forms[j].ID })
	return forms
}

func (s *Store) FormExists(_ context.Context, formID string) (bool, error) {
	if err := s.fail("FormExists"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.forms[strings.ToLower(formID)]
	return ok, nil
}

func (s *Store) LockForm(ctx context.Context, formID string) (form.Form, error) {
	if err := s.fail("LockForm"); err != nil {
		return form.Form{}, err
	}
	return s.FindForm(ctx, form.ByID, formID)
}

func (s *Store) LockFormRoot(ctx context.Context, formID string) (string, error) {
	if err := s.fail("LockFormRoot"); err != nil {
		return "", err
	}
	found, err := s.FindForm(ctx, form.ByID, formID)
	return found.ID, err
}

func (s *Store) LockFeaturedImage(ctx context.Context, formID string) (*string, error) {
	found, err := s.FindForm(ctx, form.ByID, formID)
	return found.FeaturedImageFileID, err
}

func (s *Store) DocumentRoot(ctx context.Context, formID string, _ form.RowLock) (form.DocumentRoot, error) {
	if err := s.fail("DocumentRoot"); err != nil {
		return form.DocumentRoot{}, err
	}
	found, err := s.FindForm(ctx, form.ByID, formID)
	if err != nil {
		return form.DocumentRoot{}, err
	}
	return form.DocumentRoot{Status: found.Status, SourceLocale: found.SourceLocale, DocumentID: found.ContentDocumentID}, nil
}

func (s *Store) SourceLocale(ctx context.Context, formID string) (string, error) {
	found, err := s.FindForm(ctx, form.ByID, formID)
	return found.SourceLocale, err
}

func (s *Store) ListForms(_ context.Context, list form.ListForms) ([]form.Form, int64, error) {
	if err := s.fail("ListForms"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	matched := make([]form.Form, 0, len(s.forms))
	for _, found := range s.forms {
		if s.matches(found, list.Filters) {
			matched = append(matched, found)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].ID < matched[j].ID
	})
	total := int64(len(matched))
	offset := min(int(list.Offset), len(matched))
	end := min(offset+int(list.Limit), len(matched))
	return slices.Clone(matched[offset:end]), total, nil
}

// matches applies the search and status filters of the admin list.
func (s *Store) matches(found form.Form, filters []*commonv1.FilterSpec) bool {
	for _, filter := range filters {
		switch filter.GetField() {
		case "status":
			if string(found.Status) != filter.GetValue() {
				return false
			}
		case "search":
			title := form.DefaultTitle
			if stored, ok := s.locales[LocaleKey{found.ID, found.SourceLocale}]; ok && stored.Title != nil && strings.TrimSpace(*stored.Title) != "" {
				title = *stored.Title
			}
			if !strings.Contains(strings.ToLower(title), strings.ToLower(filter.GetValue())) {
				return false
			}
		}
	}
	return true
}

func (s *Store) SourceTitles(_ context.Context, formIDs []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	titles := make(map[string]string, len(formIDs))
	for _, formID := range formIDs {
		titles[formID] = form.DefaultTitle
		found, ok := s.forms[formID]
		if !ok {
			continue
		}
		if stored, ok := s.locales[LocaleKey{formID, found.SourceLocale}]; ok && stored.Title != nil {
			if title := strings.TrimSpace(*stored.Title); title != "" {
				titles[formID] = title
			}
		}
	}
	return titles, nil
}

func (s *Store) SlugTaken(_ context.Context, slug, excludeID string) (bool, error) {
	if err := s.fail("SlugTaken"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, found := range s.forms {
		if found.Slug != nil && *found.Slug == slug && found.ID != excludeID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) RouteAvailable(_ context.Context, slug string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.routes[slug], nil
}

func (s *Store) LockRoute(_ context.Context, slug string) error {
	if err := s.fail("LockRoute"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lockedRoutes = append(s.lockedRoutes, slug)
	return nil
}

func (s *Store) InsertForm(_ context.Context, value *form.Form) error {
	if err := s.fail("InsertForm"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value.ID = uuid.NewString()
	value.CreatedAt = value.CreatedAt.UTC().Truncate(time.Microsecond)
	updatedAt := value.CreatedAt
	value.UpdatedAt = &updatedAt
	s.forms[value.ID] = *value
	return nil
}

func (s *Store) UpdateSettings(_ context.Context, next form.Form, settings []form.Setting, now time.Time) error {
	if err := s.fail("UpdateSettings"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.forms[next.ID]
	if !ok {
		return nil
	}
	for _, setting := range settings {
		switch setting {
		case form.SettingSlug:
			stored.Slug = next.Slug
		case form.SettingIsPublic:
			stored.IsPublic = next.IsPublic
		case form.SettingRequireAuth:
			stored.RequireAuth = next.RequireAuth
		case form.SettingAllowDuplicateSubmission:
			stored.AllowDuplicateSubmission = next.AllowDuplicateSubmission
		case form.SettingAccessPassword:
			stored.AccessPassword = next.AccessPassword
		case form.SettingMaxSubmissions:
			stored.MaxSubmissions = next.MaxSubmissions
		case form.SettingOpensAt:
			stored.OpensAt = next.OpensAt
		case form.SettingClosesAt:
			stored.ClosesAt = next.ClosesAt
		case form.SettingStatus:
			stored.Status = next.Status
		case form.SettingAllowedRoles:
			stored.AllowedRoles = slices.Clone(next.AllowedRoles)
		}
	}
	at := now.UTC().Truncate(time.Microsecond)
	stored.UpdatedAt = &at
	s.forms[next.ID] = stored
	return nil
}

func (s *Store) SetFeaturedImage(_ context.Context, formID string, fileID *string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.forms[formID]
	if !ok {
		return nil
	}
	if fileID != nil {
		value := strings.ToLower(*fileID)
		fileID = &value
	}
	stored.FeaturedImageFileID = fileID
	at := now.UTC().Truncate(time.Microsecond)
	stored.UpdatedAt = &at
	s.forms[formID] = stored
	return nil
}

func (s *Store) TouchForm(_ context.Context, formID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored, ok := s.forms[formID]; ok {
		at := now.UTC().Truncate(time.Microsecond)
		stored.UpdatedAt = &at
		s.forms[formID] = stored
	}
	return nil
}

func (s *Store) DeleteForm(_ context.Context, formID string) error {
	if err := s.fail("DeleteForm"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.forms, formID)
	for key := range s.locales {
		if key.FormID == formID {
			delete(s.locales, key)
		}
	}
	for id, submission := range s.submissions {
		if submission.FormID == formID {
			delete(s.submissions, id)
		}
	}
	return nil
}

// FormLocales

func (s *Store) Locale(_ context.Context, formID, locale string, _ bool) (form.Locale, bool, error) {
	if err := s.fail("Locale"); err != nil {
		return form.Locale{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.locales[LocaleKey{strings.ToLower(formID), locale}]
	if !ok {
		return form.Locale{}, false, nil
	}
	return form.Locale{
		Revision: strconv.Itoa(stored.version), Title: stored.Title, Schema: stored.ContentJSON,
		ContentText: stored.ContentText, UpdatedAt: stored.UpdatedAt,
	}, true, nil
}

func (s *Store) LocaleState(_ context.Context, formID, locale string) (form.TranslationDocumentState, error) {
	if err := s.fail("LocaleState"); err != nil {
		return form.TranslationDocumentState{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.locales[LocaleKey{strings.ToLower(formID), locale}]
	if !ok {
		return form.TranslationDocumentState{}, form.ErrLocaleNotFound
	}
	return form.TranslationDocumentState{
		Title: stored.Title, ContentJSON: stored.ContentJSON, ContentText: stored.ContentText, OgAssetID: stored.OgAssetID,
	}, nil
}

func (s *Store) LocalizationRows(_ context.Context, formID string, locales []string) ([]form.LocalizationRow, error) {
	if err := s.fail("LocalizationRows"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]form.LocalizationRow, 0)
	for key, stored := range s.locales {
		if key.FormID != formID || (len(locales) > 0 && !slices.Contains(locales, key.Locale)) {
			continue
		}
		rows = append(rows, form.LocalizationRow{
			Locale: key.Locale, Title: stored.Title, ContentJSON: stored.ContentJSON,
			ContentText: stored.ContentText, OgAssetID: stored.OgAssetID,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Locale < rows[j].Locale })
	return rows, nil
}

func (s *Store) SaveSourceLocale(_ context.Context, formID, locale string, title *string, schema []byte, contentText *string, now time.Time) error {
	if err := s.fail("SaveSourceLocale"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := LocaleKey{formID, locale}
	at := now.UTC().Truncate(time.Microsecond)
	stored, exists := s.locales[key]
	if !exists {
		stored.CreatedAt = at
	}
	if title != nil {
		stored.Title = new(*title)
	}
	if schema != nil {
		stored.ContentJSON = slices.Clone(schema)
	}
	stored.ContentText = contentText
	stored.UpdatedAt = at
	s.version++
	stored.version = s.version
	s.locales[key] = stored
	return nil
}

func (s *Store) CreateEmptyLocale(_ context.Context, formID, locale string, schema []byte, now time.Time) (bool, error) {
	if err := s.fail("CreateEmptyLocale"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := LocaleKey{strings.ToLower(formID), locale}
	if _, exists := s.locales[key]; exists {
		return false, nil
	}
	at := now.UTC().Truncate(time.Microsecond)
	s.version++
	s.locales[key] = StoredLocale{ContentJSON: slices.Clone(schema), CreatedAt: at, UpdatedAt: at, version: s.version}
	return true, nil
}

func (s *Store) InsertLocale(_ context.Context, formID, locale string, change form.LocaleChange, createdAt time.Time) error {
	if err := s.fail("InsertLocale"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version++
	s.locales[LocaleKey{strings.ToLower(formID), locale}] = StoredLocale{
		Title: change.Title, ContentJSON: slices.Clone(change.Schema), ContentText: change.ContentText,
		CreatedAt: createdAt.UTC().Truncate(time.Microsecond), UpdatedAt: change.UpdatedAt.UTC().Truncate(time.Microsecond),
		version: s.version,
	}
	return nil
}

func (s *Store) UpdateLocale(_ context.Context, formID, locale string, change form.LocaleChange) (bool, error) {
	if err := s.fail("UpdateLocale"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := LocaleKey{strings.ToLower(formID), locale}
	stored, ok := s.locales[key]
	if !ok {
		return false, nil
	}
	if change.SetTitle {
		stored.Title = change.Title
	}
	if change.SetSchema {
		stored.ContentJSON = slices.Clone(change.Schema)
		stored.ContentText = change.ContentText
	}
	stored.UpdatedAt = change.UpdatedAt.UTC().Truncate(time.Microsecond)
	s.version++
	stored.version = s.version
	s.locales[key] = stored
	return true, nil
}

func (s *Store) DeleteLocale(_ context.Context, formID, locale string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := LocaleKey{strings.ToLower(formID), locale}
	_, ok := s.locales[key]
	delete(s.locales, key)
	return ok, nil
}

// FormSubmissions

func (s *Store) submissionsOf(formID string) []form.Submission {
	submissions := make([]form.Submission, 0)
	for _, submission := range s.submissions {
		if submission.FormID == formID {
			submissions = append(submissions, submission)
		}
	}
	sort.Slice(submissions, func(i, j int) bool {
		if !submissions[i].CreatedAt.Equal(submissions[j].CreatedAt) {
			return submissions[i].CreatedAt.After(submissions[j].CreatedAt)
		}
		return submissions[i].ID > submissions[j].ID
	})
	return submissions
}

func (s *Store) ListSubmissions(_ context.Context, list form.SubmissionList) ([]form.Submission, int64, error) {
	if err := s.fail("ListSubmissions"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	matched := make([]form.Submission, 0)
	for _, submission := range s.submissionsOf(list.FormID) {
		if list.Search != "" && !strings.Contains(strings.ToLower(compactJSON(submission.Data)), strings.ToLower(list.Search)) {
			continue
		}
		if list.CountryCode != "" && (submission.CountryCode == nil || *submission.CountryCode != list.CountryCode) {
			continue
		}
		if list.CreatedAtFrom != nil && submission.CreatedAt.Before(*list.CreatedAtFrom) {
			continue
		}
		if list.CreatedAtBefore != nil && !submission.CreatedAt.Before(*list.CreatedAtBefore) {
			continue
		}
		matched = append(matched, submission)
	}
	if len(list.Order) != 0 {
		sort.SliceStable(matched, func(i, j int) bool {
			left, right := matched[i], matched[j]
			if !left.CreatedAt.Equal(right.CreatedAt) {
				if list.Order[0].Descending {
					return left.CreatedAt.After(right.CreatedAt)
				}
				return left.CreatedAt.Before(right.CreatedAt)
			}
			return left.ID > right.ID
		})
	}
	total := int64(len(matched))
	offset := min(int(list.Offset), len(matched))
	end := min(offset+int(list.Limit), len(matched))
	return slices.Clone(matched[offset:end]), total, nil
}

// compactJSON is data as jsonb renders it as text, near enough for a
// substring search.
func compactJSON(data []byte) string {
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, data); err != nil {
		return string(data)
	}
	return buffer.String()
}

func (s *Store) SubmissionCounts(_ context.Context, formIDs []string) (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := make(map[string]int64, len(formIDs))
	for _, submission := range s.submissions {
		if slices.Contains(formIDs, submission.FormID) {
			counts[submission.FormID]++
		}
	}
	return counts, nil
}

func (s *Store) SubmissionData(_ context.Context, formID string) ([]form.SubmissionData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	submissions := s.submissionsOf(formID)
	data := make([]form.SubmissionData, len(submissions))
	for i, submission := range submissions {
		data[i] = form.SubmissionData{CreatedAt: submission.CreatedAt, Data: submission.Data}
	}
	return data, nil
}

func (s *Store) FindSubmission(_ context.Context, submissionID string) (form.Submission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	submission, ok := s.submissions[strings.ToLower(submissionID)]
	if !ok {
		return form.Submission{}, form.ErrSubmissionNotFound
	}
	return submission, nil
}

func (s *Store) DeleteSubmission(_ context.Context, submissionID string) (bool, error) {
	if err := s.fail("DeleteSubmission"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.submissions[submissionID]
	delete(s.submissions, submissionID)
	return ok, nil
}

func (s *Store) CountSubmissions(_ context.Context, formID string) (int64, error) {
	if err := s.fail("CountSubmissions"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.submissionsOf(formID))), nil
}

func (s *Store) HasMemberSubmission(_ context.Context, formID, memberID string) (bool, error) {
	if err := s.fail("HasMemberSubmission"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, submission := range s.submissionsOf(formID) {
		if submission.MemberID != nil && *submission.MemberID == memberID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) InsertSubmission(_ context.Context, submission *form.Submission) error {
	if err := s.fail("InsertSubmission"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	submission.ID = uuid.NewString()
	submission.CreatedAt = submission.CreatedAt.UTC().Truncate(time.Microsecond)
	s.submissions[submission.ID] = *submission
	return nil
}

func (s *Store) Submissions(_ context.Context, formID string) ([]form.Submission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submissionsOf(formID), nil
}

// ContentDocuments

func (s *Store) CreateContentDocument(_ context.Context, input contentblock.CreateInput) (uuid.UUID, error) {
	if err := s.fail("CreateContentDocument"); err != nil {
		return uuid.Nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	documentID := input.ID
	if documentID == uuid.Nil {
		documentID = uuid.New()
	}
	s.documents[documentID] = uuid.New()
	return documentID, nil
}

func (s *Store) DocumentRevision(_ context.Context, documentID uuid.UUID) (uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	revision, ok := s.documents[documentID]
	if !ok {
		return uuid.Nil, form.ErrContentDocumentNotFound
	}
	return revision, nil
}

func (s *Store) AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence form.ContentFence, effect form.MetadataEffect) (contentblock.AdvanceResult, error) {
	if err := s.fail("AdvanceContentRevision"); err != nil {
		return contentblock.AdvanceResult{}, err
	}
	if _, err := fence(ctx, s, input.DocumentID); err != nil {
		return contentblock.AdvanceResult{}, err
	}
	s.mu.Lock()
	current, ok := s.documents[input.DocumentID]
	s.mu.Unlock()
	if !ok {
		return contentblock.AdvanceResult{}, contentblock.ErrDocumentNotFound
	}
	if current != input.ExpectedRevision {
		return contentblock.AdvanceResult{}, &contentblock.StaleRevisionError{CurrentRevision: current}
	}
	metadata, err := effect(ctx, s)
	if err != nil {
		return contentblock.AdvanceResult{}, err
	}
	if !metadata.Changed {
		return contentblock.AdvanceResult{DocumentRevision: current}, nil
	}
	next := uuid.New()
	s.mu.Lock()
	s.documents[input.DocumentID] = next
	s.mu.Unlock()
	return contentblock.AdvanceResult{DocumentRevision: next, Changed: true, TranslationSourceChanged: metadata.AffectsTranslationSource}, nil
}

// FormMedia

func (s *Store) LockAttachableFiles(_ context.Context, fileIDs []string) error {
	if err := s.fail("LockAttachableFiles"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lockedFiles = append(s.lockedFiles, fileIDs...)
	return nil
}

func (s *Store) FileID(_ context.Context, fileID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.files[strings.ToLower(fileID)] {
		return "", form.ErrFileNotFound
	}
	return strings.ToLower(fileID), nil
}

func (s *Store) BindFeaturedImage(_ context.Context, formID, fileID string) (*form.Asset, error) {
	if err := s.fail("BindFeaturedImage"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings = append(s.bindings, Binding{FormID: formID, FileID: fileID})
	return s.images[fileID], nil
}

func (s *Store) ReleaseFeaturedImage(_ context.Context, formID string) error {
	if err := s.fail("ReleaseFeaturedImage"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, formID)
	return nil
}

func (s *Store) FeaturedImage(_ context.Context, formID string) *form.Asset {
	s.mu.Lock()
	defer s.mu.Unlock()
	found, ok := s.forms[formID]
	if !ok || found.FeaturedImageFileID == nil {
		return nil
	}
	return s.images[*found.FeaturedImageFileID]
}

func (s *Store) ReadyOGAsset(_ context.Context, localizedID, sourceID *string) (*form.Asset, error) {
	if err := s.fail("ReadyOGAsset"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, candidate := range []*string{localizedID, sourceID} {
		if candidate == nil {
			continue
		}
		if asset, ok := s.readyAssets[strings.TrimSpace(*candidate)]; ok {
			return asset, nil
		}
	}
	return nil, nil
}

func (s *Store) LocalizedOGDisposition(_ context.Context, formID, locale string) (form.LocalizedOGDisposition, error) {
	if err := s.fail("LocalizedOGDisposition"); err != nil {
		return form.LocalizedOGUnavailable, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dispositions[LocaleKey{formID, locale}], nil
}

func (s *Store) RequestOG(_ context.Context, formID, locale string, allLocales bool, reason string) (string, error) {
	if err := s.fail("RequestOG"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ogRequests = append(s.ogRequests, OGRequest{FormID: formID, Locale: locale, AllLocales: allLocales, Reason: reason})
	return s.OGRunID, nil
}

func (s *Store) CancelOG(_ context.Context, formID string) error {
	if err := s.fail("CancelOG"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ogCancels = append(s.ogCancels, formID)
	return nil
}

// Other domains

func (s *Store) RequireCurrentContributor(_ context.Context, _, memberID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contributions = append(s.contributions, memberID)
	return s.Contributors[memberID]
}

func (s *Store) PreferredLocale(_ context.Context, memberID string) (*string, error) {
	if err := s.fail("PreferredLocale"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preferred[memberID], nil
}

func (s *Store) DeleteShareLinks(_ context.Context, formID string) error {
	if err := s.fail("DeleteShareLinks"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shareDeletes = append(s.shareDeletes, formID)
	return nil
}

func (s *Store) PublishContentUpdate(_ context.Context, update form.ContentUpdate) error {
	if err := s.fail("PublishContentUpdate"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, update)
	return nil
}

func (s *Store) RuntimeSettings(context.Context) (translation.RuntimeSettings, error) {
	if err := s.fail("RuntimeSettings"); err != nil {
		return translation.RuntimeSettings{}, err
	}
	return s.Settings, nil
}

func (s *Store) ShareLink(_ context.Context, token string) (public.ShareLink, error) {
	if err := s.fail("ShareLink"); err != nil {
		return public.ShareLink{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	link, ok := s.shareLinks[token]
	if !ok {
		return public.ShareLink{}, public.ErrShareLinkNotFound
	}
	return link, nil
}
