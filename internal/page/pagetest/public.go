package pagetest

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/google/uuid"

	pagepublic "github.com/echovisionlab/geul-api/internal/page/public"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// PublicStore is the fake pagepublic.Store over the rows of a Store.
type PublicStore struct {
	*Store
}

var _ pagepublic.Store = PublicStore{}

// NewPublic returns a fake Store and the unit of work of the public read
// over it.
func NewPublic() (*Store, *mutationtest.UnitOfWork[pagepublic.Store]) {
	store := NewStore()
	return store, mutationtest.New[pagepublic.Store](PublicStore{store})
}

// FindPage implements pagepublic.Store.
func (s PublicStore) FindPage(ctx context.Context, locator page.Locator, value string) (page.Page, error) {
	return s.Store.FindPage(ctx, locator, value, page.ShareLock)
}

// DocumentRoot implements pagepublic.Store.
func (s PublicStore) DocumentRoot(ctx context.Context, pageID string) (page.DocumentRoot, error) {
	return s.Store.DocumentRoot(ctx, pageID, page.ShareLock)
}

// SourceMetadata implements pagepublic.Store.
func (s PublicStore) SourceMetadata(ctx context.Context, pageID string) (page.SourceMetadata, bool, error) {
	states, err := s.Store.SourceMetadata(ctx, []string{pageID})
	state, ok := states[pageID]
	return state, ok, err
}

// ContentSnapshot implements pagepublic.Store.
func (s PublicStore) ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.Store.LockedContentSnapshot(ctx, documentID, sourceLocale)
}

// MediaReferences implements pagepublic.Store.
func (s PublicStore) MediaReferences(ctx context.Context, documentID uuid.UUID) ([]*fileref.BlockMediaItem, error) {
	return s.Store.MediaReferences(ctx, documentID)
}

// HomepagePageID implements pagepublic.Store.
func (s PublicStore) HomepagePageID(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.homepage, nil
}

// RequireShareLink implements pagepublic.Store; an unknown link answers
// not found, as the share link validator does.
func (s PublicStore) RequireShareLink(_ context.Context, token, password, pageID string) (*mediaasset.ContentDownloadShareLinkWitness, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	witness, ok := s.shareLinks[pageID+"\x00"+token+"\x00"+password]
	if !ok {
		return nil, domainerr.New(domainerr.NotFound, "", "page not found")
	}
	return witness, nil
}

// ResolveLocalization implements pagepublic.Store: the fake displays the
// source locale.
func (s PublicStore) ResolveLocalization(_ context.Context, pageID, acceptLanguage string, _ map[string]struct{}, _ translation.RuntimeSettings) (publiccontent.Selection, error) {
	value, err := s.page(pageID)
	if err != nil {
		return publiccontent.Selection{}, err
	}
	return publiccontent.Selection{
		RequestedLocale: acceptLanguage, DisplayedLocale: value.SourceLocale, SourceLocale: value.SourceLocale,
		IsOriginal: true, AvailableLocales: []string{value.SourceLocale},
	}, nil
}

// ResolveOGConsistency implements pagepublic.Store; the fake keeps the
// selection.
func (s PublicStore) ResolveOGConsistency(_ context.Context, _ string, selection publiccontent.Selection, _ func(context.Context, string) (bool, error)) (publiccontent.Selection, error) {
	return selection, nil
}
