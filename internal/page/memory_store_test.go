package page

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/translation"
)

// memoryStore is the few rows a focused internal test reads; pagetest holds
// the full fake, which package page cannot import. A method it does not
// implement panics through the nil embedded Store.
type memoryStore struct {
	Store
	pages     map[string]Page
	locales   map[[2]string]Locale
	preferred *string
	settings  translation.RuntimeSettings
}

func newMemoryStore() *memoryStore {
	return &memoryStore{pages: map[string]Page{}, locales: map[[2]string]Locale{}, settings: translation.DefaultRuntimeSettings()}
}

// putPage stores a Page on documentID with its source-locale title.
func (s *memoryStore) putPage(pageID, sourceLocale, title string, documentID uuid.UUID) {
	id := documentID.String()
	s.pages[pageID] = Page{ID: pageID, SourceLocale: sourceLocale, ContentDocumentID: &id, DocumentLayout: DefaultDocumentLayout()}
	s.locales[[2]string{pageID, sourceLocale}] = Locale{Locale: sourceLocale, Title: &title, IncarnationID: uuid.New(), UpdatedAt: time.Now().UTC()}
}

func (s *memoryStore) FindPage(_ context.Context, _ Locator, value string, _ RowLock) (Page, error) {
	value2, ok := s.pages[value]
	if !ok {
		return Page{}, ErrPageNotFound
	}
	return value2, nil
}

func (s *memoryStore) DocumentRoot(ctx context.Context, pageID string, lock RowLock) (DocumentRoot, error) {
	value, err := s.FindPage(ctx, ByID, pageID, lock)
	if err != nil {
		return DocumentRoot{}, err
	}
	documentID := uuid.MustParse(*value.ContentDocumentID)
	return DocumentRoot{ID: pageID, ContentDocumentID: &documentID, SourceLocale: value.SourceLocale}, nil
}

func (s *memoryStore) DocumentLayout(ctx context.Context, pageID string) (DocumentLayout, error) {
	value, err := s.FindPage(ctx, ByID, pageID, Unlocked)
	return value.DocumentLayout, err
}

func (s *memoryStore) SetDocumentLayout(_ context.Context, pageID string, layout DocumentLayout, at time.Time) error {
	value := s.pages[pageID]
	value.DocumentLayout, value.UpdatedAt = layout, at
	s.pages[pageID] = value
	return nil
}

func (s *memoryStore) Locale(_ context.Context, pageID, locale string, _ RowLock) (Locale, bool, error) {
	value, ok := s.locales[[2]string{pageID, locale}]
	return value, ok, nil
}

func (s *memoryStore) UpsertLocale(_ context.Context, pageID, locale string, title, summary *string, at time.Time) error {
	value, ok := s.locales[[2]string{pageID, locale}]
	if !ok {
		value = Locale{Locale: locale, IncarnationID: uuid.New()}
	}
	value.Title, value.Summary, value.UpdatedAt = title, summary, at
	s.locales[[2]string{pageID, locale}] = value
	return nil
}

func (s *memoryStore) PreferredLocale(context.Context, string) (*string, error) {
	return s.preferred, nil
}

func (s *memoryStore) RuntimeSettings(context.Context) (translation.RuntimeSettings, error) {
	return s.settings, nil
}
