// Package emailauthoringtest holds the in-memory emailauthoring.Store the
// Email Template and Email Layout use-case tests run on, and the Store
// contract it shares with emailauthoringpostgres. The fake knows no SQL and
// takes no lock; it keeps the sentinels, orderings and row counts the
// PostgreSQL store answers with.
package emailauthoringtest

import (
	"context"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/emailcontent/emailcontenttest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/translation"
)

type layoutLocaleKey struct{ layoutID, locale string }

type state struct {
	templates          map[string]emailauthoring.Template
	layouts            map[string]emailauthoring.Layout
	layoutTranslations map[layoutLocaleKey]email.LayoutTranslationEntry
	deliveryRuns       map[string]int32
	layoutReferences   map[string]emailauthoring.LayoutExternalReferenceCounts
	frozen             map[string]bool
	detached           []string
	members            map[string]bool
	sessions           map[string]*auth.UserInfo
}

func (s state) clone() state {
	clone := s
	clone.templates = mutationtest.ClonePersisted(s.templates)
	clone.layouts = mutationtest.ClonePersisted(s.layouts)
	clone.layoutTranslations = mutationtest.ClonePersisted(s.layoutTranslations)
	clone.deliveryRuns = mutationtest.ClonePersisted(s.deliveryRuns)
	clone.layoutReferences = mutationtest.ClonePersisted(s.layoutReferences)
	clone.frozen = mutationtest.ClonePersisted(s.frozen)
	clone.detached = mutationtest.ClonePersisted(s.detached)
	clone.members = mutationtest.ClonePersisted(s.members)
	clone.sessions = mutationtest.ClonePersisted(s.sessions)
	return clone
}

// Store is the in-memory emailauthoring.Store. Its embedded email content
// holds the templates' documents and locale rows, and the layouts'
// documents. Seed it with the Put methods; read it back with the getters.
type Store struct {
	*emailcontenttest.Store
	mu sync.Mutex
	state
	failures map[string]error
	// StaleContributors, when set, answers the collaboration contributor
	// check.
	StaleContributors error
	// TestSendReadiness answers CheckTestSendReadiness.
	TestSendReadiness error
	// RenderData is added to every preview's render data.
	RenderData map[string]string
}

var _ emailauthoring.Store = (*Store)(nil)

// New returns an empty Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[emailauthoring.Store]) {
	store := &Store{Store: emailcontenttest.New(), state: state{
		templates: map[string]emailauthoring.Template{}, layouts: map[string]emailauthoring.Layout{},
		layoutTranslations: map[layoutLocaleKey]email.LayoutTranslationEntry{},
		deliveryRuns:       map[string]int32{}, layoutReferences: map[string]emailauthoring.LayoutExternalReferenceCounts{},
		frozen: map[string]bool{}, members: map[string]bool{}, sessions: map[string]*auth.UserInfo{},
	}}
	store.Touched = func(id string, now time.Time) {
		store.mu.Lock()
		defer store.mu.Unlock()
		if template, ok := store.templates[id]; ok {
			touched := now
			template.UpdatedAt = &touched
			store.templates[id] = template
		}
	}
	return store, mutationtest.New[emailauthoring.Store](store)
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	content := s.Store.Savepoint()
	s.mu.Lock()
	saved := s.state.clone()
	s.mu.Unlock()
	return mutationtest.Checkpoint{
		State: map[string]any{
			"templates":          saved.templates,
			"layouts":            saved.layouts,
			"layoutTranslations": saved.layoutTranslations,
			"deliveryRuns":       saved.deliveryRuns,
			"layoutReferences":   saved.layoutReferences,
			"frozen":             saved.frozen,
			"detached":           saved.detached,
			"members":            saved.members,
			"sessions":           saved.sessions,
			"content":            content.State,
		},
		Restore: func() {
			content.Restore()
			s.mu.Lock()
			s.state = saved
			s.mu.Unlock()
		},
	}
}

// Fail makes method answer err; the email content methods fail through
// FailOn.
func (s *Store) Fail(method string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failures == nil {
		s.failures = map[string]error{}
	}
	s.failures[method] = err
}

func (s *Store) fail(method string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures[method]
}

// PutTemplate stores template with a source document and its source locale
// row holding subject, and returns it with its ID and document.
func (s *Store) PutTemplate(template emailauthoring.Template, subject string) emailauthoring.Template {
	if template.ID == "" {
		template.ID = uuid.NewString()
	}
	if template.SourceLocale == "" {
		template.SourceLocale = "en"
	}
	if template.ContentDocumentID == nil {
		document := s.PutDocument(emailcontenttest.Document{})
		documentID := document.ID.String()
		template.ContentDocumentID = &documentID
	}
	if template.CreatedAt.IsZero() {
		now := time.Now().UTC()
		template.CreatedAt, template.UpdatedAt = now, &now
	}
	s.PutLocale(template.ID, emailcontenttest.Locale{LocaleMetadata: emailcontent.LocaleMetadata{
		Locale: template.SourceLocale, Subject: &subject, UpdatedAt: template.CreatedAt,
	}})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.templates[template.ID] = template
	return template
}

// Template returns the stored template.
func (s *Store) Template(id string) (emailauthoring.Template, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	template, ok := s.templates[id]
	return template, ok
}

// PutLayout stores layout with a compact source document and its source
// locale row holding html, and returns it with its ID and document.
func (s *Store) PutLayout(layout emailauthoring.Layout, html string) emailauthoring.Layout {
	if layout.ID == "" {
		layout.ID = uuid.NewString()
	}
	if layout.SourceLocale == "" {
		layout.SourceLocale = "en"
	}
	if layout.ContentDocumentID == "" {
		layout.ContentDocumentID = s.PutDocument(emailcontenttest.Document{Profile: "compact"}).ID.String()
	}
	if layout.CreatedAt.IsZero() {
		layout.CreatedAt = time.Now().UTC()
		layout.UpdatedAt = layout.CreatedAt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.layouts[layout.ID] = layout
	text := email.StripHTML(html)
	s.layoutTranslations[layoutLocaleKey{layout.ID, layout.SourceLocale}] = email.LayoutTranslationEntry{
		LayoutTranslationDocument: email.LayoutTranslationDocument{ContentHTML: &html, ContentText: &text, UpdatedAt: layout.CreatedAt},
	}
	return layout
}

// Layout returns the stored layout.
func (s *Store) Layout(id string) (emailauthoring.Layout, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	layout, ok := s.layouts[id]
	return layout, ok
}

// PutLayoutTranslation stores the layout row of locale.
func (s *Store) PutLayoutTranslation(layoutID, locale string, entry email.LayoutTranslationEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.layoutTranslations[layoutLocaleKey{layoutID, locale}] = entry
}

// LayoutTranslation returns the stored layout row of locale.
func (s *Store) LayoutTranslation(layoutID, locale string) (email.LayoutTranslationEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.layoutTranslations[layoutLocaleKey{layoutID, locale}]
	return entry, ok
}

// Freeze makes an active delivery run render the template or layout id.
func (s *Store) Freeze(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frozen[id] = true
}

// PutDeliveryRuns sets how many delivery runs render template id.
func (s *Store) PutDeliveryRuns(id string, count int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliveryRuns[id] = count
}

// PutLayoutReferences sets the Campaign references of layout id.
func (s *Store) PutLayoutReferences(id string, counts emailauthoring.LayoutExternalReferenceCounts) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.layoutReferences[id] = counts
}

// Detached lists the resources whose terminal history was detached.
func (s *Store) Detached() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.detached)
}

// PutMember makes the Member exist.
func (s *Store) PutMember(memberID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[memberID] = true
}

// PutSession makes sessionID resolve to principal.
func (s *Store) PutSession(sessionID string, principal *auth.UserInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = principal
}

// ListTemplates implements emailauthoring.Store, ordered by name.
func (s *Store) ListTemplates(_ context.Context, query emailauthoring.ListQuery) ([]emailauthoring.Template, int64, error) {
	if err := s.fail("ListTemplates"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	templates := slices.Collect(maps.Values(s.templates))
	sort.Slice(templates, func(i, j int) bool { return templates[i].Name < templates[j].Name })
	return page(templates, query.Page.Limit, query.Page.Offset), int64(len(templates)), nil
}

func page[T any](values []T, limit, offset int32) []T {
	if int(offset) >= len(values) {
		return []T{}
	}
	values = values[offset:]
	if int(limit) < len(values) {
		values = values[:limit]
	}
	return values
}

// FindTemplate implements emailauthoring.Store.
func (s *Store) FindTemplate(_ context.Context, templateID string) (emailauthoring.Template, error) {
	if err := s.fail("FindTemplate"); err != nil {
		return emailauthoring.Template{}, err
	}
	template, ok := s.Template(templateID)
	if !ok {
		return emailauthoring.Template{}, emailauthoring.ErrTemplateNotFound
	}
	return template, nil
}

// LockTemplate implements emailauthoring.Store.
func (s *Store) LockTemplate(ctx context.Context, templateID string) (emailauthoring.Template, error) {
	if err := s.fail("LockTemplate"); err != nil {
		return emailauthoring.Template{}, err
	}
	return s.FindTemplate(ctx, templateID)
}

// TemplateLayoutID implements emailauthoring.Store.
func (s *Store) TemplateLayoutID(ctx context.Context, templateID string) (*string, error) {
	template, err := s.FindTemplate(ctx, templateID)
	return template.LayoutID, err
}

// TemplateDocument implements emailauthoring.Store.
func (s *Store) TemplateDocument(ctx context.Context, templateID string, _ bool) (*string, error) {
	template, err := s.FindTemplate(ctx, templateID)
	return template.ContentDocumentID, err
}

// TemplateSourceLocale implements emailauthoring.Store.
func (s *Store) TemplateSourceLocale(ctx context.Context, templateID string, _ bool) (string, error) {
	template, err := s.FindTemplate(ctx, templateID)
	return template.SourceLocale, err
}

// InsertTemplate implements emailauthoring.Store.
func (s *Store) InsertTemplate(_ context.Context, template *emailauthoring.Template) error {
	if err := s.fail("InsertTemplate"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.templates {
		if existing.Key == template.Key {
			return emailauthoring.ErrTemplateKeyTaken
		}
	}
	template.ID = uuid.NewString()
	s.templates[template.ID] = *template
	return nil
}

// UpdateTemplate implements emailauthoring.Store.
func (s *Store) UpdateTemplate(_ context.Context, templateID string, change emailauthoring.TemplateChange) error {
	if err := s.fail("UpdateTemplate"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	template, ok := s.templates[templateID]
	if !ok {
		return nil
	}
	if change.Name != nil {
		template.Name = *change.Name
	}
	if change.Description != nil {
		description := *change.Description
		template.Description = &description
	}
	if change.IsActive != nil {
		template.IsActive = *change.IsActive
	}
	if change.SetLayout {
		template.LayoutID = change.LayoutID
	}
	updatedAt := change.UpdatedAt
	template.UpdatedAt = &updatedAt
	s.templates[templateID] = template
	return nil
}

// SetCustomTemplateVariables implements emailauthoring.Store.
func (s *Store) SetCustomTemplateVariables(_ context.Context, templateID string, variables []emailauthoring.TemplateVariable) error {
	if err := s.fail("SetCustomTemplateVariables"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	template, ok := s.templates[templateID]
	if !ok || template.IsSystem {
		return nil
	}
	template.Variables = slices.Clone(variables)
	s.templates[templateID] = template
	return nil
}

// DeleteTemplate implements emailauthoring.Store.
func (s *Store) DeleteTemplate(_ context.Context, templateID string) error {
	if err := s.fail("DeleteTemplate"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.templates, templateID)
	return nil
}

// MappedTemplates implements emailauthoring.Store.
func (s *Store) MappedTemplates(context.Context) ([]emailauthoring.Template, error) {
	if err := s.fail("MappedTemplates"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var templates []emailauthoring.Template
	for _, template := range s.templates {
		if template.EventKey != nil {
			templates = append(templates, template)
		}
	}
	sort.Slice(templates, func(i, j int) bool { return *templates[i].EventKey < *templates[j].EventKey })
	return templates, nil
}

// LockEventMappingTemplates implements emailauthoring.Store.
func (s *Store) LockEventMappingTemplates(_ context.Context, eventKey, targetID string) ([]emailauthoring.Template, error) {
	if err := s.fail("LockEventMappingTemplates"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var templates []emailauthoring.Template
	for _, template := range s.templates {
		if (template.EventKey != nil && *template.EventKey == eventKey) || (targetID != "" && template.ID == targetID) {
			templates = append(templates, template)
		}
	}
	sort.Slice(templates, func(i, j int) bool { return templates[i].ID < templates[j].ID })
	return templates, nil
}

// ClearEventMapping implements emailauthoring.Store.
func (s *Store) ClearEventMapping(_ context.Context, eventKey string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, template := range s.templates {
		if template.EventKey != nil && *template.EventKey == eventKey {
			template.EventKey, template.UpdatedAt = nil, &now
			s.templates[id] = template
		}
	}
	return nil
}

// SetEventMapping implements emailauthoring.Store.
func (s *Store) SetEventMapping(_ context.Context, templateID, eventKey string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if template, ok := s.templates[templateID]; ok {
		key := eventKey
		template.EventKey, template.UpdatedAt = &key, &now
		s.templates[templateID] = template
	}
	return nil
}

// LockTranslationSource implements emailauthoring.Store.
func (s *Store) LockTranslationSource(_ context.Context, entityType, entityID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entityType == "email_layout" {
		if _, ok := s.layouts[entityID]; !ok {
			return emailauthoring.ErrLayoutNotFound
		}
		return nil
	}
	if _, ok := s.templates[entityID]; !ok {
		return emailauthoring.ErrTemplateNotFound
	}
	return nil
}

// ListLayouts implements emailauthoring.Store, ordered by name and ID.
func (s *Store) ListLayouts(_ context.Context, query emailauthoring.ListQuery) ([]emailauthoring.Layout, int64, error) {
	if err := s.fail("ListLayouts"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	layouts := slices.Collect(maps.Values(s.layouts))
	sort.Slice(layouts, func(i, j int) bool {
		if layouts[i].Name != layouts[j].Name {
			return layouts[i].Name < layouts[j].Name
		}
		return layouts[i].ID < layouts[j].ID
	})
	return page(layouts, query.Page.Limit, query.Page.Offset), int64(len(layouts)), nil
}

// FindLayout implements emailauthoring.Store; its HTML is left to the
// layout's translations.
func (s *Store) FindLayout(_ context.Context, layoutID string) (emailauthoring.Layout, error) {
	if err := s.fail("FindLayout"); err != nil {
		return emailauthoring.Layout{}, err
	}
	layout, ok := s.Layout(layoutID)
	if !ok {
		return emailauthoring.Layout{}, emailauthoring.ErrLayoutNotFound
	}
	layout.HTMLContent = ""
	return layout, nil
}

// LockLayout implements emailauthoring.Store.
func (s *Store) LockLayout(ctx context.Context, layoutID string) (emailauthoring.Layout, error) {
	return s.FindLayout(ctx, layoutID)
}

// LockLayoutRoot implements emailauthoring.Store.
func (s *Store) LockLayoutRoot(ctx context.Context, layoutID string, _ emailcontent.RowLock) error {
	_, err := s.FindLayout(ctx, layoutID)
	return err
}

// LockLayouts implements emailauthoring.Store.
func (s *Store) LockLayouts(_ context.Context, layoutIDs []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	locked := []string{}
	for _, id := range layoutIDs {
		if _, ok := s.layouts[id]; ok {
			locked = append(locked, id)
		}
	}
	slices.Sort(locked)
	return locked, nil
}

// LayoutAuthority implements emailauthoring.Store.
func (s *Store) LayoutAuthority(ctx context.Context, layoutID string, _ emailcontent.RowLock) (emailauthoring.LayoutAuthority, error) {
	layout, err := s.FindLayout(ctx, layoutID)
	if err != nil {
		return emailauthoring.LayoutAuthority{}, err
	}
	documentID, err := uuid.Parse(layout.ContentDocumentID)
	if err != nil {
		return emailauthoring.LayoutAuthority{}, emailauthoring.ErrLayoutNotFound
	}
	document, ok := s.Document(documentID)
	if !ok {
		return emailauthoring.LayoutAuthority{}, emailauthoring.ErrLayoutNotFound
	}
	return emailauthoring.LayoutAuthority{
		LayoutID: layout.ID, SourceLocale: layout.SourceLocale, DocumentID: document.ID, DocumentRevision: document.Revision,
	}, nil
}

// InsertLayout implements emailauthoring.Store.
func (s *Store) InsertLayout(_ context.Context, layout *emailauthoring.Layout) error {
	if err := s.fail("InsertLayout"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.layouts {
		if existing.Key == layout.Key {
			return emailauthoring.ErrLayoutKeyTaken
		}
	}
	layout.ID = uuid.NewString()
	stored := *layout
	stored.HTMLContent = ""
	s.layouts[layout.ID] = stored
	return nil
}

// UpdateLayout implements emailauthoring.Store.
func (s *Store) UpdateLayout(_ context.Context, layoutID string, change emailauthoring.LayoutChange) error {
	if err := s.fail("UpdateLayout"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	layout, ok := s.layouts[layoutID]
	if !ok {
		return nil
	}
	if change.Key != nil {
		for id, existing := range s.layouts {
			if id != layoutID && existing.Key == *change.Key {
				return emailauthoring.ErrLayoutKeyTaken
			}
		}
		layout.Key = *change.Key
	}
	if change.Name != nil {
		layout.Name = *change.Name
	}
	layout.UpdatedAt = change.UpdatedAt
	s.layouts[layoutID] = layout
	return nil
}

// DeleteLayout implements emailauthoring.Store.
func (s *Store) DeleteLayout(_ context.Context, layoutID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.layouts, layoutID)
	for key := range s.layoutTranslations {
		if key.layoutID == layoutID {
			delete(s.layoutTranslations, key)
		}
	}
	return nil
}

// TemplateCounts implements emailauthoring.Store.
func (s *Store) TemplateCounts(_ context.Context, layoutIDs []string) (map[string]int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int32{}
	for _, template := range s.templates {
		if template.LayoutID != nil && slices.Contains(layoutIDs, *template.LayoutID) {
			counts[*template.LayoutID]++
		}
	}
	return counts, nil
}

// LayoutSourceLocale implements emailauthoring.Store.
func (s *Store) LayoutSourceLocale(_ context.Context, layoutID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	layout, ok := s.layouts[layoutID]
	if !ok || layout.SourceLocale == "" {
		return "", false, nil
	}
	return layout.SourceLocale, true, nil
}

// CanonicalLayoutSource implements emailauthoring.Store.
func (s *Store) CanonicalLayoutSource(_ context.Context, layoutID, sourceLocale string) (email.LayoutTranslationDocument, error) {
	entry, ok := s.LayoutTranslation(layoutID, sourceLocale)
	if !ok {
		return email.LayoutTranslationDocument{}, domainerr.ResourceNotFound("email layout source translation", layoutID)
	}
	return entry.LayoutTranslationDocument, nil
}

// CanonicalLayoutSources implements emailauthoring.Store.
func (s *Store) CanonicalLayoutSources(_ context.Context, layoutIDs []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sources := map[string]string{}
	for _, id := range layoutIDs {
		layout, ok := s.layouts[id]
		if !ok {
			continue
		}
		if entry, ok := s.layoutTranslations[layoutLocaleKey{id, layout.SourceLocale}]; ok && entry.ContentHTML != nil {
			sources[id] = *entry.ContentHTML
		}
	}
	return sources, nil
}

// LayoutTranslationEntry implements emailauthoring.Store.
func (s *Store) LayoutTranslationEntry(_ context.Context, layoutID, locale string) (*email.LayoutTranslationEntry, error) {
	entry, ok := s.LayoutTranslation(layoutID, locale)
	if !ok {
		return nil, nil
	}
	return &entry, nil
}

// LockLayoutTranslationEntry implements emailauthoring.Store.
func (s *Store) LockLayoutTranslationEntry(ctx context.Context, layoutID, locale string, _ emailcontent.RowLock) (*email.LayoutTranslationEntry, error) {
	return s.LayoutTranslationEntry(ctx, layoutID, locale)
}

// SaveLayoutSource implements emailauthoring.Store.
func (s *Store) SaveLayoutSource(_ context.Context, layoutID, locale string, document email.LayoutTranslationDocument, now time.Time) error {
	if err := s.fail("SaveLayoutSource"); err != nil {
		return err
	}
	document.UpdatedAt = now
	s.PutLayoutTranslation(layoutID, locale, email.LayoutTranslationEntry{LayoutTranslationDocument: document})
	return nil
}

// UpsertLayoutTranslation implements emailauthoring.Store.
func (s *Store) UpsertLayoutTranslation(_ context.Context, layoutID, locale string, input translation.EntryWrite) error {
	if err := s.fail("UpsertLayoutTranslation"); err != nil {
		return err
	}
	s.PutLayoutTranslation(layoutID, locale, email.LayoutTranslationEntry{LayoutTranslationDocument: email.LayoutTranslationDocument{
		ContentHTML: input.ContentHTML, ContentText: input.ContentText, UpdatedAt: input.Now,
	}})
	return nil
}

// SeedLayoutTranslation implements emailauthoring.Store.
func (s *Store) SeedLayoutTranslation(_ context.Context, layoutID, locale, sourceLocale string, now time.Time) error {
	source, ok := s.LayoutTranslation(layoutID, sourceLocale)
	if !ok {
		return domainerr.ResourceNotFound("email layout source translation", layoutID)
	}
	source.UpdatedAt = now
	s.PutLayoutTranslation(layoutID, locale, source)
	return nil
}

// DeleteLayoutTranslation implements emailauthoring.Store.
func (s *Store) DeleteLayoutTranslation(_ context.Context, layoutID, locale string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := layoutLocaleKey{layoutID, locale}
	if _, ok := s.layoutTranslations[key]; !ok {
		return false, nil
	}
	delete(s.layoutTranslations, key)
	return true, nil
}

// LocalizedLayoutHTML implements emailauthoring.Store: the locale's HTML,
// falling back to the source.
func (s *Store) LocalizedLayoutHTML(_ context.Context, layoutID, locale string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	layout, ok := s.layouts[layoutID]
	if !ok {
		return "", domainerr.ResourceNotFound("email layout", layoutID)
	}
	for _, candidate := range []string{locale, layout.SourceLocale} {
		if entry, ok := s.layoutTranslations[layoutLocaleKey{layoutID, candidate}]; ok && entry.ContentHTML != nil {
			return *entry.ContentHTML, nil
		}
	}
	return "", domainerr.ResourceNotFound("email layout source translation", layoutID)
}

// TemplateDeliveryRunCounts implements emailauthoring.Store.
func (s *Store) TemplateDeliveryRunCounts(_ context.Context, templateIDs []string) (map[string]int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int32{}
	for _, id := range templateIDs {
		if count, ok := s.deliveryRuns[id]; ok {
			counts[id] = count
		}
	}
	return counts, nil
}

// LayoutExternalReferenceCounts implements emailauthoring.Store.
func (s *Store) LayoutExternalReferenceCounts(_ context.Context, layoutIDs []string) (map[string]emailauthoring.LayoutExternalReferenceCounts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]emailauthoring.LayoutExternalReferenceCounts{}
	for _, id := range layoutIDs {
		counts[id] = s.layoutReferences[id]
	}
	return counts, nil
}

func (s *Store) requireMutable(id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen[id] {
		return domainerr.Precondition(name + " is frozen by an active delivery run")
	}
	return nil
}

// RequireTemplateMutable implements emailauthoring.Store.
func (s *Store) RequireTemplateMutable(_ context.Context, templateID string) error {
	return s.requireMutable(templateID, "email template")
}

// RequireLayoutMutable implements emailauthoring.Store.
func (s *Store) RequireLayoutMutable(_ context.Context, layoutID string) error {
	return s.requireMutable(layoutID, "email layout")
}

// DetachTemplateHistory implements emailauthoring.Store.
func (s *Store) DetachTemplateHistory(_ context.Context, templateID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detached = append(s.detached, templateID)
	return nil
}

// DetachLayoutHistory implements emailauthoring.Store.
func (s *Store) DetachLayoutHistory(_ context.Context, layoutID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detached = append(s.detached, layoutID)
	return nil
}

// ResolveSessionPrincipal implements emailauthoring.Store.
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

// LockMembers implements emailauthoring.Store.
func (s *Store) LockMembers(_ context.Context, memberIDs []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	locked := []string{}
	for _, id := range memberIDs {
		if s.members[id] {
			locked = append(locked, id)
		}
	}
	return locked, nil
}

// RequireCurrentTemplateContributors implements emailauthoring.Store.
func (s *Store) RequireCurrentTemplateContributors(context.Context, string, []string) error {
	return s.StaleContributors
}

// RequireCurrentLayoutContributors implements emailauthoring.Store.
func (s *Store) RequireCurrentLayoutContributors(context.Context, string, []string) error {
	return s.StaleContributors
}

// LocalizedTemplate implements emailauthoring.Store: the locale's subject
// and content, falling back to the source.
func (s *Store) LocalizedTemplate(_ context.Context, template emailauthoring.Template, locale string) (emailauthoring.Template, error) {
	if err := s.fail("LocalizedTemplate"); err != nil {
		return emailauthoring.Template{}, err
	}
	for _, candidate := range []string{strings.TrimSpace(locale), template.SourceLocale} {
		row, ok := s.Locale(template.ID, candidate)
		if !ok || candidate == "" {
			continue
		}
		if row.Subject != nil {
			template.Subject = *row.Subject
		}
		template.ContentHTML = row.HTML
		return template, nil
	}
	return template, nil
}

// EmailRenderData implements emailauthoring.Store.
func (s *Store) EmailRenderData(_ context.Context, _, _, _ string, input map[string]string) map[string]string {
	data := maps.Clone(input)
	maps.Copy(data, s.RenderData)
	return data
}

// CheckTestSendReadiness implements emailauthoring.Store.
func (s *Store) CheckTestSendReadiness(context.Context, string) error {
	return s.TestSendReadiness
}
