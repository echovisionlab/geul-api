package legaltest

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/legal/public"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
)

// ShareLink is a stored Share Link of a version.
type ShareLink struct {
	Token      string
	EntityType string
	EntityID   string
	ExpiresAt  time.Time
	Password   string
}

// PublicStore is the fake public.Store over the rows of a Store.
type PublicStore struct {
	*Store
	links *shareLinks
}

type shareLinks struct {
	mu    sync.Mutex
	links map[string]public.ShareLink
	of    map[string]ShareLink
}

var _ public.Store = PublicStore{}

// NewPublic returns a fake Store, the public read over it and its unit of
// work.
func NewPublic() (*Store, PublicStore, *mutationtest.UnitOfWork[public.Store]) {
	store := NewStore()
	reads := PublicStore{Store: store, links: &shareLinks{links: map[string]public.ShareLink{}, of: map[string]ShareLink{}}}
	return store, reads, mutationtest.New[public.Store](reads)
}

// PutShareLink stores a Share Link, hashing its password when it has one.
func (s PublicStore) PutShareLink(link ShareLink) {
	stored := public.ShareLink{ExpiresAt: &link.ExpiresAt}
	if link.Password != "" {
		hash, err := crypto.NewPasswordHasher(nil).Hash(link.Password)
		if err != nil {
			panic(err)
		}
		stored.PasswordHash = &hash
	}
	s.links.mu.Lock()
	defer s.links.mu.Unlock()
	s.links.links[link.Token] = stored
	s.links.of[link.Token] = link
}

func (s PublicStore) versions(kind legal.PolicyKind, keep func(Record) bool) []public.Version {
	s.mu.Lock()
	defer s.mu.Unlock()
	var versions []public.Version
	for key, record := range s.state.versions {
		if key.kind == kind && keep(record) {
			versions = append(versions, publicVersion(record))
		}
	}
	return versions
}

func publicVersion(record Record) public.Version {
	return public.Version{
		ID: record.ID, Version: record.Version.Version, Title: record.Title, Status: record.Status,
		EffectiveFrom: record.EffectiveFrom, EffectiveUntil: record.EffectiveUntil,
	}
}

func one(versions []public.Version) (public.Version, bool, error) {
	if len(versions) == 0 {
		return public.Version{}, false, nil
	}
	return versions[0], true, nil
}

func effective(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

// PublishedVersion implements public.Store.
func (s PublicStore) PublishedVersion(_ context.Context, kind legal.PolicyKind, id string) (public.Version, bool, error) {
	return one(s.versions(kind, func(record Record) bool {
		return record.ID == id && (record.Status == status(kind, "active") || record.Status == status(kind, "archived"))
	}))
}

// VersionInStatus implements public.Store.
func (s PublicStore) VersionInStatus(_ context.Context, kind legal.PolicyKind, id string, state legal.Status) (public.Version, bool, error) {
	return one(s.versions(kind, func(record Record) bool { return record.ID == id && record.Status == state }))
}

// EffectiveVersion implements public.Store.
func (s PublicStore) EffectiveVersion(_ context.Context, kind legal.PolicyKind, now time.Time) (public.Version, bool, error) {
	versions := s.versions(kind, func(record Record) bool {
		return record.Status == status(kind, "active") && record.EffectiveFrom != nil && !record.EffectiveFrom.After(now) &&
			(record.EffectiveUntil == nil || record.EffectiveUntil.After(now))
	})
	sort.Slice(versions, func(i, j int) bool { return versions[i].Version > versions[j].Version })
	return one(versions)
}

// NextScheduledVersion implements public.Store.
func (s PublicStore) NextScheduledVersion(_ context.Context, kind legal.PolicyKind, now time.Time) (public.Version, bool, error) {
	if err := s.fail("NextScheduledVersion"); err != nil {
		return public.Version{}, false, err
	}
	versions := s.versions(kind, func(record Record) bool {
		return record.Status == status(kind, "scheduled") && record.EffectiveFrom != nil && record.EffectiveFrom.After(now)
	})
	sort.Slice(versions, func(i, j int) bool {
		return effective(versions[i].EffectiveFrom).Before(effective(versions[j].EffectiveFrom))
	})
	return one(versions)
}

func (s PublicStore) archived(kind legal.PolicyKind) []public.Version {
	versions := s.versions(kind, func(record Record) bool { return record.Status == status(kind, "archived") })
	sort.Slice(versions, func(i, j int) bool {
		return effective(versions[i].EffectiveFrom).After(effective(versions[j].EffectiveFrom))
	})
	return versions
}

// CountArchived implements public.Store.
func (s PublicStore) CountArchived(_ context.Context, kind legal.PolicyKind) (int64, error) {
	if err := s.fail("CountArchived"); err != nil {
		return 0, err
	}
	return int64(len(s.archived(kind))), nil
}

// ListArchived implements public.Store.
func (s PublicStore) ListArchived(_ context.Context, kind legal.PolicyKind, limit, offset int32) ([]public.Version, error) {
	versions := s.archived(kind)
	start := min(int(offset), len(versions))
	end := min(start+int(limit), len(versions))
	return versions[start:end], nil
}

// ShareLink implements public.Store.
func (s PublicStore) ShareLink(_ context.Context, token, entityType, entityID string, now time.Time) (public.ShareLink, bool, error) {
	s.links.mu.Lock()
	defer s.links.mu.Unlock()
	link, ok := s.links.of[token]
	if !ok || link.EntityType != entityType || link.EntityID != entityID || !link.ExpiresAt.After(now) {
		return public.ShareLink{}, false, nil
	}
	return s.links.links[token], true, nil
}

// Lifecycle implements public.Store.
func (s PublicStore) Lifecycle(_ context.Context, kind legal.PolicyKind, id string) (legal.Lifecycle, bool, error) {
	record, found := s.Record(kind, id)
	if !found {
		return legal.Lifecycle{}, false, nil
	}
	return legal.Lifecycle{Status: record.Status, EffectiveFrom: record.EffectiveFrom}, true, nil
}

// DocumentRoot implements public.Store.
func (s PublicStore) DocumentRoot(_ context.Context, kind legal.PolicyKind, id string) (public.DocumentRoot, error) {
	record, err := s.record(kind, id)
	if err != nil {
		return public.DocumentRoot{}, err
	}
	root := public.DocumentRoot{SourceLocale: record.SourceLocale}
	if record.ContentDocumentID != nil {
		documentID := uuid.MustParse(*record.ContentDocumentID)
		root.ContentDocumentID = &documentID
	}
	return root, nil
}

// ContentSnapshot implements public.Store.
func (s PublicStore) ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.LockedContentSnapshot(ctx, documentID, sourceLocale)
}

// Localizer is the fake public.Localizer: a reader sees the target locale
// their Accept-Language names when the version has a row in it, and the
// source locale otherwise.
type Localizer struct {
	Store *Store
}

var _ public.Localizer = Localizer{}

// Resolve implements public.Localizer.
func (l Localizer) Resolve(_ context.Context, kind legal.PolicyKind, entityID, acceptLanguage string) (publiccontent.Selection, error) {
	record, err := l.Store.record(kind, entityID)
	if err != nil {
		return publiccontent.Selection{}, err
	}
	selection := publiccontent.Selection{
		RequestedLocale: acceptLanguage, DisplayedLocale: record.SourceLocale, SourceLocale: record.SourceLocale,
		IsOriginal: true, AvailableLocales: []string{record.SourceLocale},
	}
	if translation, ok := l.Store.StoredTranslation(kind, entityID, acceptLanguage); ok {
		selection.DisplayedLocale, selection.IsOriginal, selection.Title = acceptLanguage, false, translation.Title
		selection.AvailableLocales = append(selection.AvailableLocales, acceptLanguage)
	}
	return selection, nil
}

// FallbackToSource implements public.Localizer.
func (l Localizer) FallbackToSource(_ context.Context, _ legal.PolicyKind, _ string, selection publiccontent.Selection) (publiccontent.Selection, error) {
	selection.DisplayedLocale, selection.IsFallback, selection.IsOriginal, selection.Title = selection.SourceLocale, true, true, nil
	return selection, nil
}

// OGImages is the fake public.OGImages: the disposition and ready image of
// each locale of a kind's route.
type OGImages struct {
	Dispositions map[string]public.OGDisposition
	Images       map[string]*public.Asset
}

var _ public.OGImages = OGImages{}

// LocalizedOGDisposition implements public.OGImages.
func (o OGImages) LocalizedOGDisposition(_ context.Context, kind legal.PolicyKind, locale string) (public.OGDisposition, error) {
	return o.Dispositions[string(kind)+"/"+locale], nil
}

// ReadyLocalizedOGImage implements public.OGImages.
func (o OGImages) ReadyLocalizedOGImage(_ context.Context, kind legal.PolicyKind, locale string) (*public.Asset, error) {
	return o.Images[string(kind)+"/"+locale], nil
}
