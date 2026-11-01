// Package emailtest is the in-memory email.Store and the Store contract it
// shares with emailpostgres.
package emailtest

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// Store is the email Store in memory, with the orderings and filters of the
// SQL. It takes no lock.
type Store struct {
	mu           sync.Mutex
	templates    map[string]model.EmailTemplate
	templateRows map[string]map[string]email.TemplateContent
	layouts      map[string]string // layout ID: source locale
	layoutRows   map[string]map[string]email.LayoutEntry
	suppressions []suppression
	adapters     []model.MailAdapter
}

type suppression struct {
	address  string
	released bool
}

var _ email.Store = (*Store)(nil)

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{
		templates:    map[string]model.EmailTemplate{},
		templateRows: map[string]map[string]email.TemplateContent{},
		layouts:      map[string]string{},
		layoutRows:   map[string]map[string]email.LayoutEntry{},
	}
}

// New returns a Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[email.Store]) {
	store := NewStore()
	return store, mutationtest.New[email.Store](store)
}

func cloneRows[V any](rows map[string]map[string]V) map[string]map[string]V {
	clone := make(map[string]map[string]V, len(rows))
	for id, locales := range rows {
		clone[id] = maps.Clone(locales)
	}
	return clone
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	templates, templateRows := maps.Clone(s.templates), cloneRows(s.templateRows)
	layouts, layoutRows := maps.Clone(s.layouts), cloneRows(s.layoutRows)
	suppressions, adapters := slices.Clone(s.suppressions), slices.Clone(s.adapters)
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.templates, s.templateRows, s.layouts, s.layoutRows = templates, templateRows, layouts, layoutRows
		s.suppressions, s.adapters = suppressions, adapters
	}
}

// SeedTemplate stores a template.
func (s *Store) SeedTemplate(template model.EmailTemplate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.templates[template.ID] = template
}

// SeedTemplateContent stores one locale row of a template.
func (s *Store) SeedTemplateContent(templateID string, content email.TemplateContent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.templateRows[templateID] == nil {
		s.templateRows[templateID] = map[string]email.TemplateContent{}
	}
	s.templateRows[templateID][content.Locale] = content
}

// SeedLayout stores a layout with its source locale.
func (s *Store) SeedLayout(layoutID, sourceLocale string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.layouts[layoutID] = sourceLocale
}

// SeedSuppression stores a suppression of address.
func (s *Store) SeedSuppression(address string, released bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.suppressions = append(s.suppressions, suppression{address: address, released: released})
}

// SeedMailAdapter stores a mail adapter.
func (s *Store) SeedMailAdapter(adapter model.MailAdapter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adapters = append(s.adapters, adapter)
}

// LayoutRows returns the stored rows of a layout by locale.
func (s *Store) LayoutRows(layoutID string) map[string]email.LayoutEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.layoutRows[layoutID])
}

func (s *Store) template(templateID string, match func(model.EmailTemplate) bool) (model.EmailTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := slices.Sorted(maps.Keys(s.templates))
	for _, id := range ids {
		if template := s.templates[id]; (templateID == "" || id == templateID) && match(template) {
			return template, nil
		}
	}
	return model.EmailTemplate{}, email.ErrTemplateNotFound
}

func (s *Store) ActiveTemplateByEventKey(_ context.Context, eventKey string) (model.EmailTemplate, error) {
	return s.template("", func(template model.EmailTemplate) bool {
		return template.IsActive && template.EventKey != nil && *template.EventKey == eventKey
	})
}

func (s *Store) TemplateByID(_ context.Context, templateID string) (model.EmailTemplate, error) {
	return s.template(templateID, func(model.EmailTemplate) bool { return true })
}

func (s *Store) TemplateSourceLocale(_ context.Context, templateID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	template, ok := s.templates[templateID]
	return template.SourceLocale, ok, nil
}

func (s *Store) TemplateContent(_ context.Context, templateID, locale string) (*email.TemplateContent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content, ok := s.templateRows[templateID][locale]
	if !ok || content.ContentHTML == nil {
		return nil, nil
	}
	return &content, nil
}

func (s *Store) TemplateTranslations(_ context.Context, templateID string, locales []string) ([]email.TemplateContent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var contents []email.TemplateContent
	for _, locale := range slices.Sorted(maps.Keys(s.templateRows[templateID])) {
		if slices.Contains(locales, locale) {
			contents = append(contents, s.templateRows[templateID][locale])
		}
	}
	return contents, nil
}

func (s *Store) LayoutSourceLocale(_ context.Context, layoutID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	locale, ok := s.layouts[layoutID]
	return locale, ok, nil
}

func (s *Store) LayoutContent(_ context.Context, layoutID, locale string) (*string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.layoutRows[layoutID][locale].HTMLContent, nil
}

func (s *Store) content(layoutID string, entry email.LayoutEntry) email.LayoutContent {
	return email.LayoutContent{
		LayoutID: layoutID, Locale: entry.Locale, HTMLContent: entry.HTMLContent,
		IsSourceLocale: s.layouts[layoutID] == entry.Locale,
	}
}

func (s *Store) LayoutTranslations(_ context.Context, layoutID string, locales []string) ([]email.LayoutContent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var contents []email.LayoutContent
	for _, locale := range slices.Sorted(maps.Keys(s.layoutRows[layoutID])) {
		if slices.Contains(locales, locale) {
			contents = append(contents, s.content(layoutID, s.layoutRows[layoutID][locale]))
		}
	}
	return contents, nil
}

func (s *Store) SourceLayouts(_ context.Context, layoutIDs []string) ([]email.LayoutContent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var contents []email.LayoutContent
	for _, layoutID := range layoutIDs {
		sourceLocale, ok := s.layouts[layoutID]
		if !ok {
			continue
		}
		if entry, ok := s.layoutRows[layoutID][sourceLocale]; ok {
			contents = append(contents, s.content(layoutID, entry))
		}
	}
	return contents, nil
}

func (s *Store) LayoutLocales(_ context.Context, layoutID string) ([]email.LayoutContent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.layouts[layoutID]; !ok {
		return nil, nil
	}
	contents := make([]email.LayoutContent, 0, len(s.layoutRows[layoutID]))
	for _, locale := range slices.Sorted(maps.Keys(s.layoutRows[layoutID])) {
		contents = append(contents, s.content(layoutID, s.layoutRows[layoutID][locale]))
	}
	slices.SortStableFunc(contents, func(a, b email.LayoutContent) int {
		switch {
		case a.IsSourceLocale == b.IsSourceLocale:
			return 0
		case a.IsSourceLocale:
			return -1
		default:
			return 1
		}
	})
	return contents, nil
}

func (s *Store) LayoutEntry(_ context.Context, layoutID, locale string) (*email.LayoutEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.layoutRows[layoutID][locale]
	if !ok {
		return nil, nil
	}
	return &entry, nil
}

func (s *Store) LayoutTargets(_ context.Context, layoutID, sourceLocale string) ([]email.LayoutEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []email.LayoutEntry
	for _, locale := range slices.Sorted(maps.Keys(s.layoutRows[layoutID])) {
		if locale != sourceLocale {
			entries = append(entries, s.layoutRows[layoutID][locale])
		}
	}
	return entries, nil
}

func clonePointer(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func (s *Store) put(layoutID, locale string, html, text *string, updatedAt time.Time) {
	if s.layoutRows[layoutID] == nil {
		s.layoutRows[layoutID] = map[string]email.LayoutEntry{}
	}
	s.layoutRows[layoutID][locale] = email.LayoutEntry{
		Locale: locale, HTMLContent: clonePointer(html), ContentText: clonePointer(text), UpdatedAt: updatedAt,
	}
}

func (s *Store) UpsertLayoutEntry(_ context.Context, layoutID, locale string, html, text *string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.put(layoutID, locale, html, text, now)
	return nil
}

func (s *Store) UpdateLayoutEntry(_ context.Context, layoutID, locale string, html, text *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.layoutRows[layoutID][locale]; ok {
		s.put(layoutID, locale, html, text, entry.UpdatedAt)
	}
	return nil
}

func (s *Store) InsertLayoutEntry(_ context.Context, layoutID, locale string, html, text *string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.layoutRows[layoutID][locale]; !ok {
		s.put(layoutID, locale, html, text, now)
	}
	return nil
}

func (s *Store) ActiveSuppressionCount(_ context.Context, address string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, suppressed := range s.suppressions {
		if !suppressed.released && strings.ToLower(suppressed.address) == address {
			count++
		}
	}
	return count, nil
}

func (s *Store) ActiveMailAdapterCount(ctx context.Context) (int64, error) {
	adapters, err := s.ActiveMailAdapters(ctx)
	return int64(len(adapters)), err
}

func (s *Store) ActiveMailAdapters(context.Context) ([]model.MailAdapter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var active []model.MailAdapter
	for _, adapter := range s.adapters {
		if adapter.IsActive {
			active = append(active, adapter)
		}
	}
	slices.SortStableFunc(active, func(a, b model.MailAdapter) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return active, nil
}
