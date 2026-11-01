package referencecatalog

import (
	"context"
	"errors"
	"time"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// ReferenceKind names one reference catalog: its value is the resource name
// the API messages and the kind's table use.
type ReferenceKind string

const (
	KindCategory ReferenceKind = "category"
	KindTag      ReferenceKind = "tag"
	KindGenre    ReferenceKind = "genre"
	KindStyle    ReferenceKind = "style"
	KindFormat   ReferenceKind = "format"
)

// ReferenceKinds lists every reference kind.
var ReferenceKinds = []ReferenceKind{KindCategory, KindTag, KindGenre, KindStyle, KindFormat}

// Reference is one row of a reference kind. Description is nil for a kind
// without one (tag, format); CreatedAt is zero for format, which keeps no
// creation time; only a category keeps UpdatedAt.
type Reference struct {
	ID          string
	Name        string
	Slug        string
	Description *string
	CreatedAt   time.Time
	UpdatedAt   *time.Time
}

// ReferenceUsage is a reference with how many rows of other domains point at
// it.
type ReferenceUsage struct {
	Reference Reference
	Count     int32
}

// ReferenceChanges are the fields an update stores; nil leaves a field as
// it is.
type ReferenceChanges struct {
	Name        *string
	Slug        *string
	Description *string
}

// Client is a stored Client.
type Client struct {
	ID              string
	Name            string
	Website         *string
	LogoLightFileID *string
	LogoDarkFileID  *string
	CreatedAt       time.Time
}

// ClientChanges are the fields an update stores; nil leaves a field as it
// is.
type ClientChanges struct {
	Name    *string
	Website *string
}

// AddressComponents are the parsed parts of a Map Place address.
type AddressComponents struct {
	Street     *string
	City       *string
	Region     *string
	Country    *string
	PostalCode *string
}

// MapPlace is a stored Map Place. UpdatedAt is the time the last save
// stamped, as that save returned it.
type MapPlace struct {
	ID                string
	Name              string
	Address           string
	AddressComponents *AddressComponents
	Lat               float64
	Lng               float64
	GooglePlaceID     *string
	ImageFileID       *string
	CreatedByMemberID *string
	UpdatedByMemberID *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// MemberSummary is how a Map Place shows the Member who created or last
// updated it. Avatar is the Member's ready avatar asset, if any.
type MemberSummary struct {
	ID       string
	Nickname string
	Avatar   *commonv1.AssetRef
	Deleted  bool
}

// ListPage selects one page of a list: the shared query language's filters
// and sorts, and a limit and offset the use case already defaulted.
type ListPage struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Limit   int32
	Offset  int32
}

var (
	// ErrReferenceNotFound answers a lock of a reference that does not
	// exist.
	ErrReferenceNotFound = errors.New("reference not found")
	// ErrNameTaken answers a write of a reference name another reference
	// of the kind holds.
	ErrNameTaken = errors.New("reference name is taken")
	// ErrSlugTaken answers a write of a reference slug another reference of
	// the kind holds.
	ErrSlugTaken = errors.New("reference slug is taken")
	// ErrReferenceTaken answers a write another unique constraint of the
	// kind refused.
	ErrReferenceTaken = errors.New("reference name or slug is taken")
	// ErrClientNotFound answers a read, lock or logo write of a Client that
	// does not exist.
	ErrClientNotFound = errors.New("client not found")
	// ErrClientNameTaken answers a write of a Client name another Client
	// holds.
	ErrClientNameTaken = errors.New("client name is taken")
	// ErrMapPlaceNotFound answers a read or lock of a Map Place that does
	// not exist.
	ErrMapPlaceNotFound = errors.New("map place not found")
)

// Store is what the Reference Catalog use cases read and write, bound to one
// transaction or read snapshot. referencecatalogpostgres implements it over
// the catalog tables and the media, Member, Menu and Work adapters the
// catalog transaction calls; referencecatalogtest is the in-memory fake.
type Store interface {
	References
	Clients
	MapPlaces
	Media
}

// References reads and writes the rows of the reference kinds.
type References interface {
	// ListReferences lists one page of the kind and counts every match.
	ListReferences(ctx context.Context, kind ReferenceKind, page ListPage) ([]Reference, int64, error)
	// ReferenceUsage counts, for each of ids, the rows of other domains
	// that point at it; an unused reference is absent.
	ReferenceUsage(ctx context.Context, kind ReferenceKind, ids []string) (map[string]int32, error)
	// InsertReference stores a new reference with value's name, slug and
	// description and returns it as stored. A taken name or slug is
	// ErrNameTaken, ErrSlugTaken or ErrReferenceTaken.
	InsertReference(ctx context.Context, kind ReferenceKind, value Reference) (Reference, error)
	// LockReference locks the reference for update and reads it, or answers
	// ErrReferenceNotFound.
	LockReference(ctx context.Context, kind ReferenceKind, id string) (Reference, error)
	// UpdateReference stores changes and returns the reference as stored,
	// with the errors of InsertReference.
	UpdateReference(ctx context.Context, kind ReferenceKind, id string, changes ReferenceChanges) (Reference, error)
	// ReferenceInUse reports whether a row of another domain points at the
	// reference.
	ReferenceInUse(ctx context.Context, kind ReferenceKind, id string) (bool, error)
	// DeleteReference deletes the reference.
	DeleteReference(ctx context.Context, kind ReferenceKind, id string) error
	// UpdateMenuTargetSlug and RemoveMenuTarget keep the menu items that
	// link to a reference consistent with its slug change or deletion.
	UpdateMenuTargetSlug(ctx context.Context, change MenuTargetSlugChange) error
	RemoveMenuTarget(ctx context.Context, target MenuTarget) error
}

// Clients reads and writes the client table. A missing Client is
// ErrClientNotFound and a taken name ErrClientNameTaken.
type Clients interface {
	// Client reads one Client.
	Client(ctx context.Context, id string) (Client, error)
	// ListClients lists one page of Clients and counts every match.
	ListClients(ctx context.Context, page ListPage) ([]Client, int64, error)
	// SearchClients lists at most limit Clients whose name contains query,
	// ignoring case, by name; an empty query matches every Client.
	SearchClients(ctx context.Context, query string, limit int) ([]Client, error)
	// CountClientWorks counts the Works of each Client; a Client without
	// Works is absent.
	CountClientWorks(ctx context.Context, clientIDs []string) (map[string]int32, error)
	// InsertClient stores a new Client.
	InsertClient(ctx context.Context, name string, website *string) (Client, error)
	// LockClient locks the Client for update and reads it.
	LockClient(ctx context.Context, id string) (Client, error)
	// UpdateClient stores changes and returns the Client as stored.
	UpdateClient(ctx context.Context, id string, changes ClientChanges) (Client, error)
	// ClientInUse reports whether a Work or Program Event references the
	// Client.
	ClientInUse(ctx context.Context, id string) (bool, error)
	// DeleteClient deletes the Client.
	DeleteClient(ctx context.Context, id string) error
	// ClientLogoFiles reads the files the Client selects as logos.
	ClientLogoFiles(ctx context.Context, id string) (light, dark *string, err error)
	// LockClientLogo locks the Client for update and reads the file the
	// slot selects.
	LockClientLogo(ctx context.Context, id string, slot ClientLogoSlot) (*string, error)
	// SetClientLogo selects fileID in the slot, or clears it when nil.
	SetClientLogo(ctx context.Context, id string, slot ClientLogoSlot, fileID *string) error
}

// MapPlaces reads and writes the map_place table. A missing Map Place is
// ErrMapPlaceNotFound.
type MapPlaces interface {
	// MapPlace reads one Map Place.
	MapPlace(ctx context.Context, id string) (MapPlace, error)
	// MapPlacesByID reads those of ids that exist, in no particular order.
	MapPlacesByID(ctx context.Context, ids []string) ([]MapPlace, error)
	// SearchMapPlaces lists at most limit Map Places whose name matches
	// text, by name.
	SearchMapPlaces(ctx context.Context, text string, limit int) ([]MapPlace, error)
	// ListMapPlaces lists one page of Map Places and counts every match.
	ListMapPlaces(ctx context.Context, page ListPage) ([]MapPlace, int64, error)
	// LockMapPlace locks the Map Place for update and reads it.
	LockMapPlace(ctx context.Context, id string) (MapPlace, error)
	// InsertMapPlace stores place as a new Map Place and returns it as
	// stored.
	InsertMapPlace(ctx context.Context, place MapPlace) (MapPlace, error)
	// SaveMapPlace stores every field of place and returns it with the
	// update time it stamped.
	SaveMapPlace(ctx context.Context, place MapPlace) (MapPlace, error)
	// MapPlaceInUse reports whether a Post, Work or Program Event
	// references the Map Place.
	MapPlaceInUse(ctx context.Context, id string) (bool, error)
	// DeleteMapPlace deletes the Map Place.
	DeleteMapPlace(ctx context.Context, id string) error
	// MemberSummaries resolves the onboarded Members among memberIDs; any
	// other ID is absent.
	MemberSummaries(ctx context.Context, memberIDs []string) (map[string]MemberSummary, error)
}

// Media is the file-owned asset lifecycle a catalog transaction drives.
type Media interface {
	// LockFilesForAttachment locks the files about to be attached; an empty
	// ID is skipped.
	LockFilesForAttachment(ctx context.Context, fileIDs []string) error
	// BindReadyAsset binds the ready asset of a source file to its owner.
	BindReadyAsset(ctx context.Context, binding AssetBinding) (*commonv1.AssetRef, error)
	// ReleaseAssets releases the owner's bindings under a key prefix.
	ReleaseAssets(ctx context.Context, release AssetRelease) error
	// ReadyAsset resolves the ready asset of a source file.
	ReadyAsset(ctx context.Context, source AssetSource) (*commonv1.AssetRef, error)
}
