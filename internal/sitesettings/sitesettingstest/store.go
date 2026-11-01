// Package sitesettingstest provides the in-memory Site Settings Store, its
// unit of work and the Store contract the PostgreSQL store keeps too.
package sitesettingstest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
)

// Store implements sitesettings.Store in memory. Like the schema it starts
// with the default settings row. Its File, Page, Menu and OG answers stand in
// for the adapters behind the PostgreSQL store's ports: Files have the ready
// assets SeedFile gave them, Pages and Menus exist once seeded, and an OG
// request plans one run whenever the change affects an OG image. It takes no
// lock: its unit of work serializes transactions.
type Store struct {
	mu       sync.Mutex
	state    state
	ogRuns   int
	ogErr    error
	requests [][]string
	locked   []string
}

// state is what a transaction can change and a rollback restores.
type state struct {
	settings sitesettings.Settings
	loaders  []string
	files    map[string]file
	bindings map[string]Binding
	pages    map[string]bool // published
	menus    map[string]struct{}
}

type file struct {
	assets   map[string]sitesettings.Asset // by kind
	rejected error
}

// Binding is a public asset binding of the settings: the File and the asset
// kind it binds.
type Binding struct {
	SourceFileID string
	Kind         string
}

var _ sitesettings.Store = (*Store)(nil)

// New returns a Store with the default settings, and the unit of work over
// it.
func New() (*Store, *mutationtest.UnitOfWork[sitesettings.Store]) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := &Store{state: state{
		settings: sitesettings.Settings{
			SocialLinks: []byte("{}"), PrimaryColor: "#b02d23", DefaultCommentsEnabled: true,
			DefaultMapThemeID: uuid.NewString(), CreatedAt: now, UpdatedAt: now,
		},
		files: map[string]file{}, bindings: map[string]Binding{}, pages: map[string]bool{}, menus: map[string]struct{}{},
	}}
	return store, mutationtest.New[sitesettings.Store](store)
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved := s.state.clone()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.state = saved
	}
}

func (st state) clone() state {
	files := make(map[string]file, len(st.files))
	for id, f := range st.files {
		files[id] = file{assets: maps.Clone(f.assets), rejected: f.rejected}
	}
	return state{
		settings: st.settings.Clone(), loaders: slices.Clone(st.loaders), files: files,
		bindings: maps.Clone(st.bindings), pages: maps.Clone(st.pages), menus: maps.Clone(st.menus),
	}
}

// SeedFile stores a File with a ready asset of each kind and returns its ID.
// An asset's URL names its File and kind.
func (s *Store) SeedFile(kinds ...string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.NewString()
	assets := make(map[string]sitesettings.Asset, len(kinds))
	for _, kind := range kinds {
		assets[kind] = Asset(id, kind)
	}
	s.state.files[id] = file{assets: assets}
	return id
}

// Asset is the ready asset SeedFile gives File fileID for kind.
func Asset(fileID, kind string) sitesettings.Asset {
	return sitesettings.Asset{
		ID: fileID + "-" + kind, URL: "https://cdn.example.test/asset/" + fileID + "/" + kind,
		Extension: "png", MIMEType: "image/png", FileSize: 1024,
	}
}

// RejectFile makes ValidateAttachment answer err for File fileID.
func (s *Store) RejectFile(fileID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.state.files[fileID]
	f.rejected = err
	s.state.files[fileID] = f
}

// SeedPage stores a Page, published or not, and returns its ID.
func (s *Store) SeedPage(published bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.NewString()
	s.state.pages[id] = published
	return id
}

// SeedMenu stores a Menu and returns its ID.
func (s *Store) SeedMenu() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.NewString()
	s.state.menus[id] = struct{}{}
	return id
}

// SetStored replaces the stored settings, as a migration or another domain
// would.
func (s *Store) SetStored(settings sitesettings.Settings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.settings = settings.Clone()
}

// FailOG makes every OG request answer err.
func (s *Store) FailOG(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ogErr = err
}

// Stored returns the stored settings.
func (s *Store) Stored() sitesettings.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.settings.Clone()
}

// Loaders returns the loader Files in order.
func (s *Store) Loaders() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.state.loaders)
}

// Bindings returns the asset bindings by key.
func (s *Store) Bindings() map[string]Binding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.state.bindings)
}

// OGRequests returns the changed keys of every OG request, in order,
// including those a rollback undid.
func (s *Store) OGRequests() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// Locked returns every File locked for attachment, in order.
func (s *Store) Locked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.locked)
}

func (s *Store) Settings(context.Context) (sitesettings.Settings, error) {
	return s.Stored(), nil
}

func (s *Store) LockSettings(context.Context) (sitesettings.Settings, error) {
	return s.Stored(), nil
}

func (s *Store) SaveSettings(_ context.Context, settings sitesettings.Settings) error {
	s.SetStored(settings)
	return nil
}

func (s *Store) TouchSettings(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.settings.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	return nil
}

func (s *Store) LoaderFileIDs(context.Context) ([]string, error) {
	return s.Loaders(), nil
}

func (s *Store) AddLoaderFile(_ context.Context, fileID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.state.files[fileID]; !exists {
		return false, fmt.Errorf("site_setting_loader_file references missing file %s", fileID)
	}
	if slices.Contains(s.state.loaders, fileID) {
		return false, nil
	}
	s.state.loaders = append(s.state.loaders, fileID)
	return true, nil
}

func (s *Store) RemoveLoaderFile(_ context.Context, fileID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := slices.Index(s.state.loaders, fileID)
	if index < 0 {
		return false, nil
	}
	s.state.loaders = slices.Delete(s.state.loaders, index, index+1)
	return true, nil
}

func (s *Store) LockFilesForAttachment(_ context.Context, fileIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locked = append(s.locked, fileIDs...)
	return nil
}

func (s *Store) ValidateAttachment(_ context.Context, _, fileID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, exists := s.state.files[strings.TrimSpace(fileID)]
	if !exists {
		return domainerr.Precondition("file no longer exists")
	}
	return f.rejected
}

func (s *Store) BindAsset(_ context.Context, binding sitesettings.AssetBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.readyAsset(binding.SourceFileID, binding.Kind); err != nil {
		return err
	}
	s.state.bindings[binding.Key] = Binding{SourceFileID: binding.SourceFileID, Kind: binding.Kind}
	return nil
}

func (s *Store) ReleaseAssets(_ context.Context, prefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.state.bindings {
		if strings.HasPrefix(key, prefix) {
			delete(s.state.bindings, key)
		}
	}
	return nil
}

func (s *Store) ReplaceFavicon(ctx context.Context, fileID *string) error {
	if err := s.ReleaseAssets(ctx, "favicon"); err != nil {
		return err
	}
	if fileID == nil || strings.TrimSpace(*fileID) == "" {
		return nil
	}
	return s.BindAsset(ctx, sitesettings.AssetBinding{SourceFileID: *fileID, Key: "favicon", Kind: "favicon"})
}

func (s *Store) ReadyAsset(_ context.Context, fileID, kind string) (*sitesettings.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readyAsset(fileID, kind)
}

func (s *Store) readyAsset(fileID, kind string) (*sitesettings.Asset, error) {
	asset, ready := s.state.files[strings.TrimSpace(fileID)].assets[kind]
	if !ready {
		return nil, domainerr.ResourceNotFound("public_asset_for_source_file", fileID)
	}
	return &asset, nil
}

func (s *Store) Favicon(_ context.Context, fileID string) (*sitesettings.Asset, *sitesettings.FaviconSet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, err := s.readyAsset(fileID, "favicon")
	if err != nil {
		return nil, nil
	}
	return asset, nil
}

func (s *Store) ValidateReference(_ context.Context, key string, id *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == nil || strings.TrimSpace(*id) == "" {
		return nil
	}
	switch key {
	case "homepage_page_id":
		if !s.state.pages[strings.TrimSpace(*id)] {
			return domainerr.InvalidField(key, "must reference a published page")
		}
	case "menu_header_id", "menu_secondary_id", "menu_footer_id", "menu_avatar_dropdown_id":
		if _, exists := s.state.menus[strings.TrimSpace(*id)]; !exists {
			return domainerr.InvalidField(key, "must reference an existing menu")
		}
	}
	return nil
}

func (s *Store) RequestOGRegeneration(_ context.Context, before, after sitesettings.Settings, changedKeys []string) (*string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, slices.Clone(changedKeys))
	if s.ogErr != nil {
		return nil, s.ogErr
	}
	if sitesettings.ClassifyOGInvalidation(&before, &after, changedKeys) == (sitesettings.OGInvalidation{}) {
		return nil, nil
	}
	s.ogRuns++
	runID := fmt.Sprintf("og-run-%d", s.ogRuns)
	return &runID, nil
}
