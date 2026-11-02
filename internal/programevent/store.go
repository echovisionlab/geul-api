package programevent

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/translation"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

// The outcomes a Store answers with in place of a driver error.
var (
	ErrEventNotFound  = errors.New("program event not found")
	ErrSeriesNotFound = errors.New("program event series not found")
	ErrTypeNotFound   = errors.New("program event type not found")
	ErrMediaNotFound  = errors.New("program event media not found")
	ErrCreditNotFound = errors.New("program event credit not found")
	ErrLocaleNotFound = errors.New("program event locale not found")
	// ErrSlugTaken is a unique violation of an event, series or type row.
	ErrSlugTaken = errors.New("program event slug taken")
	// ErrTypeInUse is a foreign key violation of a deleted type.
	ErrTypeInUse = errors.New("program event type in use")
)

// Store is what the Program Event, series and type use cases read and
// write, bound to one transaction or read snapshot. programeventpostgres
// implements it over the program_event tables and the shared content,
// media, route, session and Member runtimes the Program Event transaction
// drives; programeventtest is the in-memory fake.
type Store interface {
	EventRows
	EventLocales
	EventMedia
	EventCredits
	EventRelations
	SeriesRows
	TypeRows
	ContentDocuments
	MediaAssets
	Collaboration
}

// RowLock is the lock a read takes on the rows it returns.
type RowLock uint8

const (
	NoLock RowLock = iota
	ShareLock
	UpdateLock
)

// RouteKind is the public route namespace of a slug.
type RouteKind uint8

const (
	EventRoute RouteKind = iota + 1
	SeriesRoute
)

// ListPage is one filtered, sorted page of a list.
type ListPage struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Limit   int32
	Offset  int32
}

// Nullable is an update to a nullable column. An unset column is left
// alone; a set one with a nil Value is written as NULL.
type Nullable[T any] struct {
	Set   bool
	Value *T
}

func setNullable[T any](value *T) Nullable[T] {
	return Nullable[T]{Set: true, Value: value}
}

// SettingsUpdate is a validated event settings update; nil and unset fields
// are left alone, and UpdatedAt is always written.
type SettingsUpdate struct {
	TypeID       *string
	Timezone     *string
	AllDay       *bool
	Slug         *string
	SeriesID     Nullable[string]
	SeriesOrder  Nullable[int32]
	TicketURL    Nullable[string]
	StreamURL    Nullable[string]
	ExternalURL  Nullable[string]
	StartsAt     *time.Time
	EndsAt       Nullable[time.Time]
	LocationMode *LocationMode
	MapPlaceID   Nullable[string]
	UpdatedAt    time.Time
}

// EventRows reads and writes the program_event table. A missing event is
// ErrEventNotFound.
type EventRows interface {
	// Event reads the event row with lock.
	Event(ctx context.Context, eventID string, lock RowLock) (Event, error)
	// RequireEvent reads only the event's ID.
	RequireEvent(ctx context.Context, eventID string) error
	// LockEventID locks the event row for update.
	LockEventID(ctx context.Context, eventID string) error
	// EventStatus reads the event's lifecycle status with lock.
	EventStatus(ctx context.Context, eventID string, lock RowLock) (Status, error)
	// LockLifecycle locks the event row for update and reads its lifecycle.
	LockLifecycle(ctx context.Context, eventID string) (Lifecycle, error)
	// SetLifecycle stores status, published_at when publishedAt is set, and
	// updatedAt, and returns the stored lifecycle.
	SetLifecycle(ctx context.Context, eventID string, status Status, publishedAt *time.Time, updatedAt time.Time) (Lifecycle, error)
	// InsertEvent stores a new event and fills its ID and timestamps. A
	// unique violation is ErrSlugTaken.
	InsertEvent(ctx context.Context, event *Event) error
	// UpdateSettings writes update to the event. A unique violation is
	// ErrSlugTaken.
	UpdateSettings(ctx context.Context, eventID string, update SettingsUpdate) error
	// TouchEvent moves updated_at.
	TouchEvent(ctx context.Context, eventID string, at time.Time) error
	// EventUpdatedAt reads updated_at.
	EventUpdatedAt(ctx context.Context, eventID string) (time.Time, error)
	// DeleteEvent deletes the event row and reports whether it existed.
	DeleteEvent(ctx context.Context, eventID string) (bool, error)
	// ListEvents lists one page of events, ordered by the page's sorts and
	// then ID, and counts every match.
	ListEvents(ctx context.Context, page ListPage) ([]Event, int64, error)
	// ContentDocumentID reads the event's content document reference, nil
	// when unset, with lock.
	ContentDocumentID(ctx context.Context, eventID string, lock RowLock) (*string, error)
	// SourceLocale reads the event's source locale with lock.
	SourceLocale(ctx context.Context, eventID string, lock RowLock) (string, error)
	// DocumentRoot reads the root of an AI document with lock.
	DocumentRoot(ctx context.Context, eventID string, lock RowLock) (DocumentRoot, error)
	// LockTitle locks the event row for update and reads its title and
	// source locale.
	LockTitle(ctx context.Context, eventID string) (title, sourceLocale string, err error)
	// Title reads the event's title.
	Title(ctx context.Context, eventID string) (string, error)
	// SetTitle stores the title and updatedAt.
	SetTitle(ctx context.Context, eventID, title string, updatedAt time.Time) error
	// TranslationSource reads the title and the summary of locale, and
	// reports whether that locale exists.
	TranslationSource(ctx context.Context, eventID, locale string) (title string, summary *string, found bool, err error)
	// CheckRoute requires that no Page uses the slug's route, and LockRoute
	// locks the route first.
	CheckRoute(ctx context.Context, kind RouteKind, slug string) error
	LockRoute(ctx context.Context, kind RouteKind, slug string) error
	// SeriesOrderTaken reports whether an event other than excludeEventID
	// holds order in the series.
	SeriesOrderTaken(ctx context.Context, seriesID string, order int32, excludeEventID string) (bool, error)
	// LockSeriesEvents locks the events of the series for update and
	// returns their IDs.
	LockSeriesEvents(ctx context.Context, seriesID string) ([]string, error)
	// DetachFromSeries clears the event's series and order.
	DetachFromSeries(ctx context.Context, eventID string, updatedAt time.Time) error
	// CountTypeEvents counts the events of a type.
	CountTypeEvents(ctx context.Context, typeID string) (int64, error)
}

// EventLocales reads and writes the program_event_translation table.
type EventLocales interface {
	// InsertSourceLocale stores the source locale of a new event.
	InsertSourceLocale(ctx context.Context, eventID, locale string, summary *string, at time.Time) error
	// LocaleMetadata reads one locale, locked for update when forUpdate is
	// set, and reports whether it exists.
	LocaleMetadata(ctx context.Context, eventID, locale string, forUpdate bool) (LocaleMetadata, bool, error)
	// LockLocale locks one locale row for update and reads it. A missing
	// locale is ErrLocaleNotFound.
	LockLocale(ctx context.Context, eventID, locale string) (LocaleMetadata, error)
	// SetLocaleSummary stores the locale's summary and updatedAt.
	SetLocaleSummary(ctx context.Context, eventID, locale string, summary *string, updatedAt time.Time) error
	// InsertTargetLocale creates a target locale and reports how many rows
	// it stored.
	InsertTargetLocale(ctx context.Context, eventID, locale string, summary *string, at time.Time) (int64, error)
	// UpdateTargetLocale stores a target locale's summary and revision clock
	// and reports how many rows it changed.
	UpdateTargetLocale(ctx context.Context, eventID, locale string, summary *string, updatedAt time.Time) (int64, error)
	// DeleteTargetLocale deletes a target locale and reports how many rows
	// it deleted.
	DeleteTargetLocale(ctx context.Context, eventID, locale string) (int64, error)
	// LocaleSummaries lists every locale, the source locale first and then
	// by locale.
	LocaleSummaries(ctx context.Context, eventID string) ([]LocaleSummary, error)
	// LocaleSummary reads the summary of one locale and reports whether the
	// locale exists.
	LocaleSummary(ctx context.Context, eventID, locale string) (*string, bool, error)
}

// EventMedia reads and writes the program_event_media table. Lists of a role
// are ordered by sort order, then creation.
type EventMedia interface {
	// MediaOfFile reads the relation of fileID under role and reports
	// whether it exists.
	MediaOfFile(ctx context.Context, eventID, role, fileID string) (Media, bool, error)
	// MediaByID reads one relation. A missing relation is ErrMediaNotFound.
	MediaByID(ctx context.Context, eventID, mediaID string) (Media, error)
	// PrimaryMediaFileID is the File of the role's primary relation, else
	// its first one, else empty.
	PrimaryMediaFileID(ctx context.Context, eventID, role string) (string, error)
	// PosterFileIDs maps each event to its primary poster File.
	PosterFileIDs(ctx context.Context, eventIDs []string) (map[string]string, error)
	// HasPrimaryMedia reports whether the role has a primary relation.
	HasPrimaryMedia(ctx context.Context, eventID, role string) (bool, error)
	// ClearPrimaryMedia makes no relation of the role primary.
	ClearPrimaryMedia(ctx context.Context, eventID, role string, updatedAt time.Time) error
	// UpdateMedia stores the relation's alt text and caption, and makes it
	// primary when makePrimary is set.
	UpdateMedia(ctx context.Context, mediaID string, alt, caption *string, makePrimary bool, updatedAt time.Time) error
	// NextMediaSortOrder is one past the role's highest sort order.
	NextMediaSortOrder(ctx context.Context, eventID, role string) (int32, error)
	// InsertMedia stores a new relation and fills its ID.
	InsertMedia(ctx context.Context, media *Media) error
	// DeleteMedia deletes one relation and reports whether it existed.
	DeleteMedia(ctx context.Context, eventID, mediaID string) (bool, error)
	// FirstMedia reads the role's first relation and reports whether one
	// exists.
	FirstMedia(ctx context.Context, eventID, role string) (Media, bool, error)
	// MarkMediaPrimary makes the relation primary.
	MarkMediaPrimary(ctx context.Context, mediaID string, updatedAt time.Time) error
	// RoleMedia lists the relations of a role.
	RoleMedia(ctx context.Context, eventID, role string) ([]Media, error)
	// PlaceMedia moves a relation of the role to position, primary when it
	// is first, and reports whether it belongs to the role.
	PlaceMedia(ctx context.Context, eventID, role, mediaID string, position int32, updatedAt time.Time) (bool, error)
	// DeleteRoleMedia deletes every relation of a role and returns them.
	DeleteRoleMedia(ctx context.Context, eventID, role string) ([]Media, error)
	// EventMedia lists every relation by role, primary first.
	EventMedia(ctx context.Context, eventID string) ([]Media, error)
}

// CreditUpdate is an update to one credit row; unset and nil fields are
// left alone.
type CreditUpdate struct {
	ArtistID    Nullable[string]
	MemberID    Nullable[string]
	DisplayName Nullable[string]
	CreditRole  Nullable[string]
	Description Nullable[string]
	SortOrder   *int32
}

// CreditMemberReference is a credited Member and the request field that
// names it.
type CreditMemberReference struct {
	MemberID string
	Field    string
}

// EventCredits reads and writes the program_event_credit table. Lists are
// ordered by sort order, then creation.
type EventCredits interface {
	// NextCreditSortOrder is one past the event's highest credit position.
	NextCreditSortOrder(ctx context.Context, eventID string) (int32, error)
	// InsertCredit stores a new credit and fills its ID.
	InsertCredit(ctx context.Context, credit *CreditRecord) error
	// Credit reads one credit. A missing credit is ErrCreditNotFound.
	Credit(ctx context.Context, eventID, creditID string) (CreditRecord, error)
	// UpdateCredit writes update to the credit.
	UpdateCredit(ctx context.Context, creditID string, update CreditUpdate) error
	// DeleteCredit deletes one credit of the event and reports whether it
	// existed.
	DeleteCredit(ctx context.Context, eventID, creditID string) (bool, error)
	// RemoveCredit deletes the credit row.
	RemoveCredit(ctx context.Context, creditID string) error
	// Credits lists the credits, LockCredits locked for update.
	Credits(ctx context.Context, eventID string) ([]CreditRecord, error)
	LockCredits(ctx context.Context, eventID string) ([]CreditRecord, error)
	// CreditsByID lists the credits ordered by sort order, then ID.
	CreditsByID(ctx context.Context, eventID string) ([]CreditRecord, error)
	// CountCredits counts the credits of the event among creditIDs.
	CountCredits(ctx context.Context, eventID string, creditIDs []string) (int64, error)
	// PlaceCredit moves the credit to position.
	PlaceCredit(ctx context.Context, eventID, creditID string, position int) error
	// LockCreditMembers protects the credited Members from racing identity
	// deletion.
	LockCreditMembers(ctx context.Context, references []CreditMemberReference) error
	// CreditArtist reads the source-locale name and slug of a credited
	// artist; an empty ID means it does not exist. ArtistImageFileID is its
	// first image, or empty.
	CreditArtist(ctx context.Context, artistID string) (CreditArtist, error)
	ArtistImageFileID(ctx context.Context, artistID string) (string, error)
}

// EventRelations reads and writes the artists, labels and clients of an
// event, ordered by sort order, then related ID.
type EventRelations interface {
	Relations(ctx context.Context, kind RelationKind, eventID string) ([]Relation, error)
	// DeleteRelations removes every relation of kind.
	DeleteRelations(ctx context.Context, kind RelationKind, eventID string) error
	// InsertRelations stores relations created at once.
	InsertRelations(ctx context.Context, kind RelationKind, eventID string, relations []Relation, createdAt time.Time) error
	// InsertRelation stores one relation.
	InsertRelation(ctx context.Context, kind RelationKind, eventID string, relation Relation) error
	// DeleteRelation and SetRelationRole report how many rows they changed.
	DeleteRelation(ctx context.Context, kind RelationKind, eventID, relatedID string) (int64, error)
	SetRelationRole(ctx context.Context, kind RelationKind, eventID, relatedID string, role *string) (int64, error)
}

// SeriesChange is a validated series update; nil fields are left alone.
// PosterChanged with a nil PosterFileID removes the poster.
type SeriesChange struct {
	Title         *string
	Summary       *string
	Description   *string
	Slug          *string
	Status        *Status
	PosterChanged bool
	PosterFileID  *string
}

// SeriesRows reads and writes the program_event_series table. A missing
// series is ErrSeriesNotFound.
type SeriesRows interface {
	// InsertSeries stores a new series and fills its ID. A unique violation
	// is ErrSlugTaken.
	InsertSeries(ctx context.Context, series *Series) error
	// Series reads the series with lock, SeriesBySlug by slug.
	Series(ctx context.Context, seriesID string, lock RowLock) (Series, error)
	SeriesBySlug(ctx context.Context, slug string) (Series, error)
	// LockSeriesID locks the series row for update.
	LockSeriesID(ctx context.Context, seriesID string) error
	// UpdateSeries writes change and updatedAt. A unique violation is
	// ErrSlugTaken.
	UpdateSeries(ctx context.Context, seriesID string, change SeriesChange, updatedAt time.Time) error
	// DeleteSeries deletes the series and reports whether it existed.
	DeleteSeries(ctx context.Context, seriesID string) (bool, error)
	// ListSeries lists one page of series and counts every match; the
	// filters name stored statuses.
	ListSeries(ctx context.Context, page ListPage) ([]Series, int64, error)
}

// TypeChange is a validated type configuration update; nil fields are left
// alone.
type TypeChange struct {
	Slug              *string
	Status            *TypeStatus
	SortOrder         *int32
	RequiresPlace     *bool
	RequiresStreamURL *bool
}

// TypeLocaleChange is a changed name or description of a type locale.
type TypeLocaleChange struct {
	Name        *string
	Description Nullable[string]
}

// TypeRows reads and writes the program_event_type tables. A missing type
// is ErrTypeNotFound.
type TypeRows interface {
	// InsertType stores a new type and fills its ID. A unique violation is
	// ErrSlugTaken.
	InsertType(ctx context.Context, eventType *Type) error
	InsertTypeLocale(ctx context.Context, locale TypeLocale) error
	// Type reads the type with lock.
	Type(ctx context.Context, typeID string, lock RowLock) (Type, error)
	// UpdateType writes change and updatedAt. A unique violation is
	// ErrSlugTaken.
	UpdateType(ctx context.Context, typeID string, change TypeChange, updatedAt time.Time) error
	// TypeLocale reads one locale and reports whether it exists.
	TypeLocale(ctx context.Context, typeID, locale string) (TypeLocale, bool, error)
	UpdateTypeLocale(ctx context.Context, typeID, locale string, change TypeLocaleChange, updatedAt time.Time) error
	// DeleteType deletes the type. A foreign key violation is ErrTypeInUse.
	DeleteType(ctx context.Context, typeID string) error
	// TypeLocales lists the type's locales by locale.
	TypeLocales(ctx context.Context, typeID string) ([]TypeLocale, error)
	// ListTypes lists one page of types and counts every match.
	ListTypes(ctx context.Context, page ListPage) ([]Type, int64, error)
}

// ContentFence is the Program Event check the content store runs before it
// locks a document's rows, over the Store of that transaction.
type ContentFence func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error)

// MetadataEffect writes the event metadata of a source revision, in its
// transaction.
type MetadataEffect func(ctx context.Context, store Store) (contentblock.MetadataEffect, error)

// TargetMetadataEffect writes the locale row of a target-locale batch, in
// its transaction.
type TargetMetadataEffect func(ctx context.Context, store Store, contentChanged bool) (contentblock.MetadataEffect, error)

// ContentDocuments is the event's content document in the shared content
// store (contentblock.Store), bound to the event transaction.
type ContentDocuments interface {
	CreateContentDocument(ctx context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error)
	// ContentSnapshot reads the document in the transaction.
	ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	// MediaReferences lists the document's active File usages.
	MediaReferences(ctx context.Context, documentID uuid.UUID) ([]*contentv1.ContentBlockMediaItem, error)
	ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence ContentFence) (contentblock.Result, error)
	ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence ContentFence, effect MetadataEffect) (contentblock.Result, error)
	ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence ContentFence, effect TargetMetadataEffect) (contentblock.Result, error)
	AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence ContentFence, effect MetadataEffect) (contentblock.AdvanceResult, error)
	DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence ContentFence) error
	// ContentFileMIMEType reads the MIME type of the active File a Block
	// reference names, and reports whether exactly one exists.
	ContentFileMIMEType(ctx context.Context, blockID uuid.UUID, referencePath string, fileID uuid.UUID) (string, bool, error)
	// SiteVariables reads the site settings a document's variables render.
	SiteVariables(ctx context.Context) (sitevariable.Context, error)
}

// MediaAssets is the shared File and public asset lifecycle of event media
// and series posters.
type MediaAssets interface {
	LockAttachableFiles(ctx context.Context, fileIDs []string) error
	// BindPosterMedia binds the ready poster asset of fileID to a poster
	// relation; ReleaseMediaBinding releases it, ReleaseEventMedia every
	// media binding of the event.
	BindPosterMedia(ctx context.Context, eventID, mediaID, fileID string) error
	ReleaseMediaBinding(ctx context.Context, eventID, mediaID string) error
	ReleaseEventMedia(ctx context.Context, eventID string) error
	// BindSeriesPoster binds the ready poster asset of fileID to the series;
	// ReleaseSeriesPoster releases it.
	BindSeriesPoster(ctx context.Context, seriesID, fileID string) error
	ReleaseSeriesPoster(ctx context.Context, seriesID string) error
	// ReadyImageAsset resolves the ready image asset of a File.
	ReadyImageAsset(ctx context.Context, fileID string) (*mediaasset.Reference, error)
	// InlineImageAsset resolves the single ready inline image asset of a
	// Block File.
	InlineImageAsset(ctx context.Context, fileID string) (*mediaasset.Reference, error)
	// HydrateBlockMedia adds the principal's delivery to authorized Block
	// media.
	HydrateBlockMedia(ctx context.Context, eventID string, documentID uuid.UUID, principal *auth.UserInfo, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error)
}

// UpdateSource names the surface a locale change came from.
type UpdateSource uint8

const (
	SourceCollaboration UpdateSource = iota + 1
	SourceAI
)

// ContentUpdate is one content.updated signal of a locale change. Source
// writes advance the shared document revision; target writes carry only
// their opaque target revision and a target deletion carries
// LocaleDeleted.
type ContentUpdate struct {
	EventID string
	// Fields names what changed: "title", "summary", "content" or a
	// configuration field.
	Fields               []string
	DocumentRevision     string
	Contributors         []string
	Source               UpdateSource
	Locale               string
	LocaleDeleted        bool
	TargetRevision       *string
	DocumentStateChanged bool
}

// Collaboration is the shared session, identity, Member, translation and
// signal authority a Program Event transaction checks.
type Collaboration interface {
	// ResolveSessionPrincipal resolves a collaboration session's principal;
	// an invalid session is auth.ErrSessionPrincipalInvalid.
	ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error)
	// LockSession locks the session for its principal; an invalid session
	// is auth.ErrSessionPrincipalInvalid.
	LockSession(ctx context.Context, sessionID string, principal *auth.UserInfo) error
	// LockActivePrincipal locks the principal's Member and Identity and
	// reports whether they are active.
	LockActivePrincipal(ctx context.Context, principal *auth.UserInfo) (bool, error)
	// RequireCurrentContributors requires that the contributors may still
	// save the event's room.
	RequireCurrentContributors(ctx context.Context, eventID string, contributors []string) error
	// LockMembers key-share-locks the Members and returns how many exist.
	LockMembers(ctx context.Context, memberIDs []string) (int, error)
	// PreferredLocale reads the live Member's preferred locale, or nil.
	PreferredLocale(ctx context.Context, memberID string) (*string, error)
	// TranslationSettings reads the translation runtime settings, storing
	// the defaults first.
	TranslationSettings(ctx context.Context) (translation.RuntimeSettings, error)
	// PublishContentUpdate schedules the content.updated signal of update.
	PublishContentUpdate(ctx context.Context, update ContentUpdate) error
}

// Joined is a transaction a pipeline outside Program Event opened, which a
// Program Event step joins instead of opening its own: a translation apply,
// an interchange read, or an attachment check. Store and Audit are both
// bound to that transaction, so the step passes a nil transaction to
// domainaudit. programeventpostgres.Bind binds a gorm transaction; a test
// joins the fake Store and an audittest.Recorder.
type Joined struct {
	Store Store
	Audit domainaudit.Appender
}
