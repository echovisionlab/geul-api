// Package referencecatalogtest provides the in-memory Reference Catalog
// Store, its unit of work and the Store contract the PostgreSQL store keeps
// too.
package referencecatalogtest

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

type kindID struct {
	kind referencecatalog.ReferenceKind
	id   string
}

type assetKey struct {
	fileID string
	kind   string
}

type bindingKey struct {
	owner referencecatalog.AssetOwner
	key   string
}

// Store implements referencecatalog.Store in memory. It takes no lock: its
// unit of work serializes transactions. Rows of other domains (the
// relations that use a reference, Client or Map Place, Members, files and
// their ready assets) are seeded; the menu target calls and asset bindings
// are recorded.
type Store struct {
	mu         sync.Mutex
	references map[kindID]referencecatalog.Reference
	usage      map[kindID]int32
	clients    map[string]referencecatalog.Client
	works      map[string]int32
	clientUse  map[string]bool
	places     map[string]referencecatalog.MapPlace
	placeUse   map[string]bool
	members    map[string]referencecatalog.MemberSummary
	files      map[string]bool
	assets     map[assetKey]*commonv1.AssetRef
	bindings   map[bindingKey]string
	slugMoves  []referencecatalog.MenuTargetSlugChange
	removals   []referencecatalog.MenuTarget
	failures   map[string]error
}

var _ referencecatalog.Store = (*Store)(nil)

// New returns an empty Store and the unit of work over it.
func New() (*Store, *mutationtest.UnitOfWork[referencecatalog.Store]) {
	store := &Store{
		references: map[kindID]referencecatalog.Reference{}, usage: map[kindID]int32{},
		clients: map[string]referencecatalog.Client{}, works: map[string]int32{}, clientUse: map[string]bool{},
		places: map[string]referencecatalog.MapPlace{}, placeUse: map[string]bool{},
		members: map[string]referencecatalog.MemberSummary{}, files: map[string]bool{},
		assets: map[assetKey]*commonv1.AssetRef{}, bindings: map[bindingKey]string{}, failures: map[string]error{},
	}
	return store, mutationtest.New[referencecatalog.Store](store)
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	references, usage := maps.Clone(s.references), maps.Clone(s.usage)
	clients, works, clientUse := maps.Clone(s.clients), maps.Clone(s.works), maps.Clone(s.clientUse)
	places, placeUse, bindings := maps.Clone(s.places), maps.Clone(s.placeUse), maps.Clone(s.bindings)
	slugMoves, removals := slices.Clone(s.slugMoves), slices.Clone(s.removals)
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.references, s.usage = references, usage
		s.clients, s.works, s.clientUse = clients, works, clientUse
		s.places, s.placeUse, s.bindings = places, placeUse, bindings
		s.slugMoves, s.removals = slugMoves, removals
	}
}

// now is a stored timestamp: PostgreSQL keeps microseconds and no
// monotonic reading.
func now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// Fail makes every later call of the Store method named method answer err;
// nil restores it.
func (s *Store) Fail(method string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.failures, method)
		return
	}
	s.failures[method] = err
}

func (s *Store) fail(method string) error {
	return s.failures[method]
}

// SeedReference stores value as a reference of kind, outside any
// transaction, and returns it as stored.
func (s *Store) SeedReference(kind referencecatalog.ReferenceKind, value referencecatalog.Reference) referencecatalog.Reference {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insertReference(kind, value)
}

// SeedReferenceUse makes count rows of other domains point at the
// reference.
func (s *Store) SeedReferenceUse(kind referencecatalog.ReferenceKind, id string, count int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage[kindID{kind, id}] += count
}

// StoredReference returns the stored reference.
func (s *Store) StoredReference(kind referencecatalog.ReferenceKind, id string) (referencecatalog.Reference, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.references[kindID{kind, id}]
	return value, ok
}

// MenuTargetSlugChanges and RemovedMenuTargets return the menu target
// calls of committed transactions, in order.
func (s *Store) MenuTargetSlugChanges() []referencecatalog.MenuTargetSlugChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.slugMoves)
}

func (s *Store) RemovedMenuTargets() []referencecatalog.MenuTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.removals)
}

// SeedClient stores client as a new Client, outside any transaction.
func (s *Store) SeedClient(client referencecatalog.Client) referencecatalog.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	client.ID, client.CreatedAt = uuid.NewString(), now()
	s.clients[client.ID] = client
	return client
}

// SeedClientWork makes a Work reference the Client.
func (s *Store) SeedClientWork(clientID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.works[clientID]++
	s.clientUse[clientID] = true
}

// StoredClient returns the stored Client.
func (s *Store) StoredClient(id string) (referencecatalog.Client, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	client, ok := s.clients[id]
	return client, ok
}

// SeedMapPlace stores place as a new Map Place, outside any transaction;
// its timestamps default to now.
func (s *Store) SeedMapPlace(place referencecatalog.MapPlace) referencecatalog.MapPlace {
	s.mu.Lock()
	defer s.mu.Unlock()
	place.ID = uuid.NewString()
	if place.CreatedAt.IsZero() {
		place.CreatedAt = now()
	}
	if place.UpdatedAt.IsZero() {
		place.UpdatedAt = place.CreatedAt
	}
	s.places[place.ID] = place
	return place
}

// SeedMapPlaceUse makes a Post, Work or Program Event reference the place.
func (s *Store) SeedMapPlaceUse(placeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.placeUse[placeID] = true
}

// StoredMapPlace returns the stored Map Place.
func (s *Store) StoredMapPlace(id string) (referencecatalog.MapPlace, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	place, ok := s.places[id]
	return place, ok
}

// SeedMember makes an onboarded Member with nickname and returns its ID.
func (s *Store) SeedMember(nickname string) string {
	id := uuid.NewString()
	s.SeedMemberWithID(id, nickname)
	return id
}

// SeedMemberWithID makes id an onboarded Member with nickname, such as the
// principal of a request.
func (s *Store) SeedMemberWithID(id, nickname string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[id] = referencecatalog.MemberSummary{ID: id, Nickname: nickname}
}

// SeedReadyFile stores an attachable file with a ready asset of kind and
// returns the file ID and the asset.
func (s *Store) SeedReadyFile(kind string) (string, *commonv1.AssetRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fileID, assetID := uuid.NewString(), uuid.NewString()
	asset := &commonv1.AssetRef{AssetId: assetID, Url: "https://cdn.example.test/" + assetID + ".webp"}
	s.files[fileID] = true
	s.assets[assetKey{fileID, kind}] = asset
	return fileID, asset
}

// Bindings returns the source file of each binding key of owner.
func (s *Store) Bindings(owner referencecatalog.AssetOwner) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	bound := map[string]string{}
	for key, fileID := range s.bindings {
		if key.owner == owner {
			bound[key.key] = fileID
		}
	}
	return bound
}

// page applies the shared list language the fake understands: the "search"
// filter matches any of fields, ignoring case, and the only sort is
// "name". Any other field is invalid, as the PostgreSQL configurations
// reject it.
func page[T any](values []T, request referencecatalog.ListPage, fields func(T) []string, name func(T) string, byDefault func(a, b T) int) ([]T, int64, error) {
	for _, filter := range request.Filters {
		if filter.GetField() != "search" {
			return nil, 0, errs.InvalidFilterField(filter.GetField())
		}
	}
	var matched []T
	for _, value := range values {
		ok := true
		for _, filter := range request.Filters {
			ok = ok && slices.ContainsFunc(fields(value), func(field string) bool {
				return strings.Contains(strings.ToLower(field), strings.ToLower(filter.GetValue()))
			})
		}
		if ok {
			matched = append(matched, value)
		}
	}
	order := byDefault
	for _, sort := range request.Sorts {
		if sort.GetField() != "name" {
			return nil, 0, errs.InvalidSortField(sort.GetField())
		}
		descending := sort.GetOrder() == commonv1.SortOrder_SORT_ORDER_DESC
		order = func(a, b T) int {
			if descending {
				return cmp.Or(cmp.Compare(name(b), name(a)), byDefault(a, b))
			}
			return cmp.Or(cmp.Compare(name(a), name(b)), byDefault(a, b))
		}
	}
	slices.SortStableFunc(matched, order)
	start := min(int(request.Offset), len(matched))
	end := min(start+int(request.Limit), len(matched))
	return slices.Clone(matched[start:end]), int64(len(matched)), nil
}

func (s *Store) insertReference(kind referencecatalog.ReferenceKind, value referencecatalog.Reference) referencecatalog.Reference {
	value.ID = uuid.NewString()
	if kind != referencecatalog.KindFormat {
		value.CreatedAt = now()
	}
	if kind == referencecatalog.KindCategory {
		updatedAt := value.CreatedAt
		value.UpdatedAt = &updatedAt
	}
	if kind == referencecatalog.KindTag || kind == referencecatalog.KindFormat {
		value.Description = nil
	}
	s.references[kindID{kind, value.ID}] = value
	return value
}

// taken answers ErrNameTaken or ErrSlugTaken when another reference of kind
// than id holds name or slug.
func (s *Store) taken(kind referencecatalog.ReferenceKind, id, name, slug string) error {
	for key, other := range s.references {
		if key.kind != kind || key.id == id {
			continue
		}
		if other.Name == name {
			return referencecatalog.ErrNameTaken
		}
		if other.Slug == slug {
			return referencecatalog.ErrSlugTaken
		}
	}
	return nil
}

// ListReferences implements referencecatalog.Store.
func (s *Store) ListReferences(_ context.Context, kind referencecatalog.ReferenceKind, request referencecatalog.ListPage) ([]referencecatalog.Reference, int64, error) {
	if err := s.fail("ListReferences"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var values []referencecatalog.Reference
	for key, value := range s.references {
		if key.kind == kind {
			values = append(values, value)
		}
	}
	return page(values, request,
		func(value referencecatalog.Reference) []string { return []string{value.Name} },
		func(value referencecatalog.Reference) string { return value.Name },
		func(a, b referencecatalog.Reference) int {
			return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
		})
}

// ReferenceUsage implements referencecatalog.Store.
func (s *Store) ReferenceUsage(_ context.Context, kind referencecatalog.ReferenceKind, ids []string) (map[string]int32, error) {
	if err := s.fail("ReferenceUsage"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	usage := map[string]int32{}
	for _, id := range ids {
		if count := s.usage[kindID{kind, id}]; count != 0 {
			usage[id] = count
		}
	}
	return usage, nil
}

// InsertReference implements referencecatalog.Store.
func (s *Store) InsertReference(_ context.Context, kind referencecatalog.ReferenceKind, value referencecatalog.Reference) (referencecatalog.Reference, error) {
	if err := s.fail("InsertReference"); err != nil {
		return referencecatalog.Reference{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.taken(kind, "", value.Name, value.Slug); err != nil {
		return referencecatalog.Reference{}, err
	}
	return s.insertReference(kind, value), nil
}

// LockReference implements referencecatalog.Store.
func (s *Store) LockReference(_ context.Context, kind referencecatalog.ReferenceKind, id string) (referencecatalog.Reference, error) {
	if err := s.fail("LockReference"); err != nil {
		return referencecatalog.Reference{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.references[kindID{kind, id}]
	if !ok {
		return referencecatalog.Reference{}, referencecatalog.ErrReferenceNotFound
	}
	return value, nil
}

// UpdateReference implements referencecatalog.Store; a category insert
// and update stamp UpdatedAt, as gorm does for its updated_at column.
func (s *Store) UpdateReference(_ context.Context, kind referencecatalog.ReferenceKind, id string, changes referencecatalog.ReferenceChanges) (referencecatalog.Reference, error) {
	if err := s.fail("UpdateReference"); err != nil {
		return referencecatalog.Reference{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.references[kindID{kind, id}]
	if !ok {
		return referencecatalog.Reference{}, referencecatalog.ErrReferenceNotFound
	}
	if changes.Name != nil {
		value.Name = *changes.Name
	}
	if changes.Slug != nil {
		value.Slug = *changes.Slug
	}
	if changes.Description != nil {
		description := *changes.Description
		value.Description = &description
	}
	if err := s.taken(kind, id, value.Name, value.Slug); err != nil {
		return referencecatalog.Reference{}, err
	}
	if kind == referencecatalog.KindCategory {
		updatedAt := now()
		value.UpdatedAt = &updatedAt
	}
	s.references[kindID{kind, id}] = value
	return value, nil
}

// ReferenceInUse implements referencecatalog.Store.
func (s *Store) ReferenceInUse(_ context.Context, kind referencecatalog.ReferenceKind, id string) (bool, error) {
	if err := s.fail("ReferenceInUse"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage[kindID{kind, id}] != 0, nil
}

// DeleteReference implements referencecatalog.Store.
func (s *Store) DeleteReference(_ context.Context, kind referencecatalog.ReferenceKind, id string) error {
	if err := s.fail("DeleteReference"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.references, kindID{kind, id})
	return nil
}

// UpdateMenuTargetSlug implements referencecatalog.Store.
func (s *Store) UpdateMenuTargetSlug(_ context.Context, change referencecatalog.MenuTargetSlugChange) error {
	if err := s.fail("UpdateMenuTargetSlug"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slugMoves = append(s.slugMoves, change)
	return nil
}

// RemoveMenuTarget implements referencecatalog.Store.
func (s *Store) RemoveMenuTarget(_ context.Context, target referencecatalog.MenuTarget) error {
	if err := s.fail("RemoveMenuTarget"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removals = append(s.removals, target)
	return nil
}

// Client implements referencecatalog.Store.
func (s *Store) Client(_ context.Context, id string) (referencecatalog.Client, error) {
	if err := s.fail("Client"); err != nil {
		return referencecatalog.Client{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	client, ok := s.clients[id]
	if !ok {
		return referencecatalog.Client{}, referencecatalog.ErrClientNotFound
	}
	return client, nil
}

func clientsByName(a, b referencecatalog.Client) int {
	return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
}

// ListClients implements referencecatalog.Store.
func (s *Store) ListClients(_ context.Context, request referencecatalog.ListPage) ([]referencecatalog.Client, int64, error) {
	if err := s.fail("ListClients"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return page(slices.Collect(maps.Values(s.clients)), request,
		func(client referencecatalog.Client) []string { return []string{client.Name} },
		func(client referencecatalog.Client) string { return client.Name },
		clientsByName)
}

// SearchClients implements referencecatalog.Store.
func (s *Store) SearchClients(_ context.Context, query string, limit int) ([]referencecatalog.Client, error) {
	if err := s.fail("SearchClients"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []referencecatalog.Client
	for _, client := range s.clients {
		if strings.Contains(strings.ToLower(client.Name), strings.ToLower(query)) {
			found = append(found, client)
		}
	}
	slices.SortFunc(found, clientsByName)
	return found[:min(limit, len(found))], nil
}

// CountClientWorks implements referencecatalog.Store.
func (s *Store) CountClientWorks(_ context.Context, clientIDs []string) (map[string]int32, error) {
	if err := s.fail("CountClientWorks"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int32{}
	for _, id := range clientIDs {
		if count := s.works[id]; count != 0 {
			counts[id] = count
		}
	}
	return counts, nil
}

func (s *Store) clientNameTaken(id, name string) bool {
	for _, other := range s.clients {
		if other.ID != id && other.Name == name {
			return true
		}
	}
	return false
}

// InsertClient implements referencecatalog.Store.
func (s *Store) InsertClient(_ context.Context, name string, website *string) (referencecatalog.Client, error) {
	if err := s.fail("InsertClient"); err != nil {
		return referencecatalog.Client{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clientNameTaken("", name) {
		return referencecatalog.Client{}, referencecatalog.ErrClientNameTaken
	}
	client := referencecatalog.Client{ID: uuid.NewString(), Name: name, Website: website, CreatedAt: now()}
	s.clients[client.ID] = client
	return client, nil
}

// LockClient implements referencecatalog.Store.
func (s *Store) LockClient(ctx context.Context, id string) (referencecatalog.Client, error) {
	if err := s.fail("LockClient"); err != nil {
		return referencecatalog.Client{}, err
	}
	return s.Client(ctx, id)
}

// UpdateClient implements referencecatalog.Store.
func (s *Store) UpdateClient(_ context.Context, id string, changes referencecatalog.ClientChanges) (referencecatalog.Client, error) {
	if err := s.fail("UpdateClient"); err != nil {
		return referencecatalog.Client{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	client, ok := s.clients[id]
	if !ok {
		return referencecatalog.Client{}, referencecatalog.ErrClientNotFound
	}
	if changes.Name != nil {
		if s.clientNameTaken(id, *changes.Name) {
			return referencecatalog.Client{}, referencecatalog.ErrClientNameTaken
		}
		client.Name = *changes.Name
	}
	if changes.Website != nil {
		website := *changes.Website
		client.Website = &website
	}
	s.clients[id] = client
	return client, nil
}

// ClientInUse implements referencecatalog.Store.
func (s *Store) ClientInUse(_ context.Context, id string) (bool, error) {
	if err := s.fail("ClientInUse"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clientUse[id], nil
}

// DeleteClient implements referencecatalog.Store.
func (s *Store) DeleteClient(_ context.Context, id string) error {
	if err := s.fail("DeleteClient"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, id)
	return nil
}

// ClientLogoFiles implements referencecatalog.Store.
func (s *Store) ClientLogoFiles(ctx context.Context, id string) (light, dark *string, err error) {
	if err := s.fail("ClientLogoFiles"); err != nil {
		return nil, nil, err
	}
	client, err := s.Client(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return client.LogoLightFileID, client.LogoDarkFileID, nil
}

// LockClientLogo implements referencecatalog.Store.
func (s *Store) LockClientLogo(ctx context.Context, id string, slot referencecatalog.ClientLogoSlot) (*string, error) {
	if err := s.fail("LockClientLogo"); err != nil {
		return nil, err
	}
	client, err := s.Client(ctx, id)
	if err != nil {
		return nil, err
	}
	if slot == referencecatalog.ClientLogoDark {
		return client.LogoDarkFileID, nil
	}
	return client.LogoLightFileID, nil
}

// SetClientLogo implements referencecatalog.Store.
func (s *Store) SetClientLogo(_ context.Context, id string, slot referencecatalog.ClientLogoSlot, fileID *string) error {
	if err := s.fail("SetClientLogo"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	client, ok := s.clients[id]
	if !ok {
		return referencecatalog.ErrClientNotFound
	}
	if slot == referencecatalog.ClientLogoDark {
		client.LogoDarkFileID = fileID
	} else {
		client.LogoLightFileID = fileID
	}
	s.clients[id] = client
	return nil
}

// MapPlace implements referencecatalog.Store.
func (s *Store) MapPlace(_ context.Context, id string) (referencecatalog.MapPlace, error) {
	if err := s.fail("MapPlace"); err != nil {
		return referencecatalog.MapPlace{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	place, ok := s.places[id]
	if !ok {
		return referencecatalog.MapPlace{}, referencecatalog.ErrMapPlaceNotFound
	}
	return place, nil
}

// MapPlacesByID implements referencecatalog.Store, by ID.
func (s *Store) MapPlacesByID(_ context.Context, ids []string) ([]referencecatalog.MapPlace, error) {
	if err := s.fail("MapPlacesByID"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []referencecatalog.MapPlace
	for _, place := range s.places {
		if slices.Contains(ids, place.ID) {
			found = append(found, place)
		}
	}
	slices.SortFunc(found, func(a, b referencecatalog.MapPlace) int { return cmp.Compare(a.ID, b.ID) })
	return found, nil
}

// SearchMapPlaces implements referencecatalog.Store: the name contains
// text, ignoring case.
func (s *Store) SearchMapPlaces(_ context.Context, text string, limit int) ([]referencecatalog.MapPlace, error) {
	if err := s.fail("SearchMapPlaces"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []referencecatalog.MapPlace
	for _, place := range s.places {
		if strings.Contains(strings.ToLower(place.Name), strings.ToLower(text)) {
			found = append(found, place)
		}
	}
	slices.SortFunc(found, func(a, b referencecatalog.MapPlace) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return found[:min(limit, len(found))], nil
}

// ListMapPlaces implements referencecatalog.Store: newest update first.
func (s *Store) ListMapPlaces(_ context.Context, request referencecatalog.ListPage) ([]referencecatalog.MapPlace, int64, error) {
	if err := s.fail("ListMapPlaces"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return page(slices.Collect(maps.Values(s.places)), request,
		func(place referencecatalog.MapPlace) []string { return []string{place.Name, place.Address} },
		func(place referencecatalog.MapPlace) string { return place.Name },
		func(a, b referencecatalog.MapPlace) int {
			return cmp.Or(b.UpdatedAt.Compare(a.UpdatedAt), cmp.Compare(a.ID, b.ID))
		})
}

// LockMapPlace implements referencecatalog.Store.
func (s *Store) LockMapPlace(ctx context.Context, id string) (referencecatalog.MapPlace, error) {
	if err := s.fail("LockMapPlace"); err != nil {
		return referencecatalog.MapPlace{}, err
	}
	return s.MapPlace(ctx, id)
}

// InsertMapPlace implements referencecatalog.Store.
func (s *Store) InsertMapPlace(_ context.Context, place referencecatalog.MapPlace) (referencecatalog.MapPlace, error) {
	if err := s.fail("InsertMapPlace"); err != nil {
		return referencecatalog.MapPlace{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	place.ID = uuid.NewString()
	place.CreatedAt = now()
	place.UpdatedAt = place.CreatedAt
	s.places[place.ID] = place
	return place, nil
}

// SaveMapPlace implements referencecatalog.Store.
func (s *Store) SaveMapPlace(_ context.Context, place referencecatalog.MapPlace) (referencecatalog.MapPlace, error) {
	if err := s.fail("SaveMapPlace"); err != nil {
		return referencecatalog.MapPlace{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.places[place.ID]; !ok {
		return referencecatalog.MapPlace{}, referencecatalog.ErrMapPlaceNotFound
	}
	place.UpdatedAt = now()
	s.places[place.ID] = place
	return place, nil
}

// MapPlaceInUse implements referencecatalog.Store.
func (s *Store) MapPlaceInUse(_ context.Context, id string) (bool, error) {
	if err := s.fail("MapPlaceInUse"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.placeUse[id], nil
}

// DeleteMapPlace implements referencecatalog.Store.
func (s *Store) DeleteMapPlace(_ context.Context, id string) error {
	if err := s.fail("DeleteMapPlace"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.places, id)
	return nil
}

// MemberSummaries implements referencecatalog.Store.
func (s *Store) MemberSummaries(_ context.Context, memberIDs []string) (map[string]referencecatalog.MemberSummary, error) {
	if err := s.fail("MemberSummaries"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	members := map[string]referencecatalog.MemberSummary{}
	for _, id := range memberIDs {
		if member, ok := s.members[id]; ok {
			members[id] = member
		}
	}
	return members, nil
}

// LockFilesForAttachment implements referencecatalog.Store: every
// non-empty ID must be a seeded file.
func (s *Store) LockFilesForAttachment(_ context.Context, fileIDs []string) error {
	if err := s.fail("LockFilesForAttachment"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range fileIDs {
		if id = strings.TrimSpace(id); id != "" && !s.files[id] {
			return domainerr.Precondition("one or more files no longer exist")
		}
	}
	return nil
}

func (s *Store) readyAsset(source referencecatalog.AssetSource) (*commonv1.AssetRef, error) {
	asset, ok := s.assets[assetKey{source.FileID, source.Kind}]
	if !ok {
		return nil, domainerr.ResourceNotFound("public_asset_for_source_file", source.FileID)
	}
	return asset, nil
}

// BindReadyAsset implements referencecatalog.Store.
func (s *Store) BindReadyAsset(_ context.Context, binding referencecatalog.AssetBinding) (*commonv1.AssetRef, error) {
	if err := s.fail("BindReadyAsset"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, err := s.readyAsset(referencecatalog.AssetSource{FileID: binding.SourceFileID, Kind: binding.Kind})
	if err != nil {
		return nil, err
	}
	s.bindings[bindingKey{binding.Owner, binding.Key}] = binding.SourceFileID
	return asset, nil
}

// ReleaseAssets implements referencecatalog.Store.
func (s *Store) ReleaseAssets(_ context.Context, release referencecatalog.AssetRelease) error {
	if err := s.fail("ReleaseAssets"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.DeleteFunc(s.bindings, func(key bindingKey, _ string) bool {
		return key.owner == release.Owner && strings.HasPrefix(key.key, release.BindingPrefix)
	})
	return nil
}

// ReadyAsset implements referencecatalog.Store.
func (s *Store) ReadyAsset(_ context.Context, source referencecatalog.AssetSource) (*commonv1.AssetRef, error) {
	if err := s.fail("ReadyAsset"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readyAsset(source)
}
