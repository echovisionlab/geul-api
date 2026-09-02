package contentblock

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Document is the durable aggregate root for one owning domain document.
type Document struct {
	ID        uuid.UUID `json:"id"`
	Profile   string    `json:"profile"`
	Revision  uuid.UUID `json:"revision"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// BaseBlock is one source-owned structural/shared Block value. Localized
// values are carried only by explicit locale overlays.
type BaseBlock struct {
	ID            uuid.UUID       `json:"id"`
	ParentID      *uuid.UUID      `json:"parentId,omitempty"`
	ContainerSlot string          `json:"containerSlot"`
	Position      int             `json:"position"`
	Kind          string          `json:"kind"`
	SharedData    json.RawMessage `json:"sharedData"`
}

// FullBlock is the validated internal merge of a base Block and one locale.
// Contract consumes it for final type, parent, File, and source-impact checks.
type FullBlock struct {
	BaseBlock
	LocalizedData  json.RawMessage `json:"localizedData"`
	FileReferences []FileReference `json:"fileReferences,omitempty"`
}

// LocaleBlockUpdate changes only locale-owned data for an existing Block.
type LocaleBlockUpdate struct {
	BlockID       uuid.UUID       `json:"blockId"`
	ExpectedKind  string          `json:"expectedKind,omitempty"`
	LocalizedData json.RawMessage `json:"localizedData"`
}

// LocaleMutationGroup applies one locale overlay atomically with every other
// base and locale group in the Batch.
type LocaleMutationGroup struct {
	Locale  string              `json:"locale"`
	Upserts []LocaleBlockUpdate `json:"upserts,omitempty"`
	Deletes []uuid.UUID         `json:"deletes,omitempty"`
}

// LocaleOverlay is the durable locale view returned in an aggregate Snapshot.
type LocaleOverlay struct {
	Locale string              `json:"locale"`
	Blocks []LocaleBlockUpdate `json:"blocks"`
}

// Reorder changes only the structural location of an existing Block.
type Reorder struct {
	BlockID       uuid.UUID  `json:"blockId"`
	ParentID      *uuid.UUID `json:"parentId,omitempty"`
	ContainerSlot string     `json:"containerSlot"`
	Position      int        `json:"position"`
}

// FileReference is extracted by Contract from a validated full Block. Callers
// cannot submit a separate, independently trusted File relation list.
type FileReference struct {
	BlockID             uuid.UUID `json:"blockId"`
	ReferencePath       string    `json:"referencePath"`
	FileID              uuid.UUID `json:"fileId"`
	Missing             bool      `json:"missing,omitempty"`
	MissingMediaKind    string    `json:"missingMediaKind,omitempty"`
	AllowedMIMETypes    []string  `json:"-"`
	AllowedMIMEPrefixes []string  `json:"-"`
}

// File is the locked File authority visible to the domain reuse hook.
type File struct {
	ID                uuid.UUID
	MIMEType          string
	DeleteRequestedAt *time.Time
}

// PublicationAttachment is one indexed selector needed by an owning domain's
// publication readiness policy. Missing selectors never expose their former
// File UUID; active selectors expose only the exact current File relation.
type PublicationAttachment struct {
	BlockID          uuid.UUID `json:"blockId"`
	ReferencePath    string    `json:"referencePath"`
	FileID           uuid.UUID `json:"fileId,omitempty"`
	MissingMediaKind string    `json:"missingMediaKind,omitempty"`
}

// Limits are the bounded structural limits owned by a generated profile.
type Limits struct {
	MaxBlocks int
	MaxDepth  int
}

// ValidatedPayload is the only result accepted from a Block Contract.
type ValidatedPayload struct {
	SharedData     json.RawMessage
	LocalizedData  json.RawMessage
	FileReferences []FileReference
}

// Contract is the narrow adapter around the generated content catalog. It
// must fail closed on unknown kinds, fields, runtime-only fields, and invalid
// payload values.
type Contract interface {
	Limits(profile string) (Limits, error)
	ValidateBlock(profile string, block FullBlock) (ValidatedPayload, error)
	ValidateLocale(profile, kind string, localizedData json.RawMessage) (json.RawMessage, error)
	BuildExplicitEmptyLocale(profile, kind string, localizedData json.RawMessage) (json.RawMessage, error)
	ValidateParent(profile string, parent *FullBlock, child FullBlock) error
	TranslationSourceChanged(profile string, before, after []FullBlock) (bool, error)
}

// DomainContext contains root-row authority derived by the owning domain.
type DomainContext struct {
	SourceLocale string
}

// DomainFence runs before Content Block rows are locked or changed. The owning
// domain must use the same tx to lock its root row and recheck lifecycle and
// permission. Store deliberately does not begin or commit a transaction.
type DomainFence func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (DomainContext, error)

// FileReuseAuthorizer validates an added or replaced File reference after all
// affected File UUIDs have been locked in sorted order.
type FileReuseAuthorizer interface {
	AuthorizeFileReuse(
		ctx context.Context,
		tx *gorm.DB,
		document Document,
		block FullBlock,
		reference FileReference,
		file File,
	) error
}

// CreateInput creates an empty document. ID may be omitted to let Store create
// it. Profile must be a generated contract profile.
type CreateInput struct {
	ID           uuid.UUID
	Profile      string
	SourceLocale string
}

// Batch applies one base graph plus every changed locale overlay under one
// document revision CAS. Source locale authority comes from DomainFence.
type Batch struct {
	DocumentID       uuid.UUID
	ExpectedRevision uuid.UUID
	Upserts          []BaseBlock
	Deletes          []uuid.UUID
	Reorders         []Reorder
	LocaleGroups     []LocaleMutationGroup
	// ContributorMemberIDs is canonical, sorted collaboration attribution.
	// Store never uses it as permission input; owning domains verify Member
	// existence and write audit attribution inside their transaction.
	ContributorMemberIDs []uuid.UUID
	// validatedProfile is set only by generated proto adapters. PostgreSQL hot
	// paths require this provenance and predicate the stored document profile;
	// manually assembled batches remain on the cold validation path.
	validatedProfile string
	// validatedBaseReferences is populated only by generated mutation adapters
	// from NormalizeContentStorageShared. It lets PostgreSQL apply shared/File
	// changes without reloading a document aggregate.
	validatedBaseReferences map[uuid.UUID][]FileReference
}

// SourceLocaleSwitchInput changes only source-locale authority under the
// shared Content Document revision. Missing locale overlays are created as
// explicit-empty values; existing locale values remain untouched.
type SourceLocaleSwitchInput struct {
	DocumentID           uuid.UUID
	ExpectedRevision     uuid.UUID
	RequestedLocale      string
	ContributorMemberIDs []uuid.UUID
}

// ReplaceInput replaces the base graph and authoritative source overlay. Other
// overlays on retained, kind-compatible Blocks remain for domain staling. It
// is used only by typed Version restore adapters.
type ReplaceInput struct {
	DocumentID       uuid.UUID
	ExpectedRevision uuid.UUID
	Blocks           []BaseBlock
	LocaleOverlays   []LocaleOverlay
}

// DeleteLocaleInput removes one complete non-source locale from an aggregate.
// ContributorMemberIDs uses the same canonical, sorted attribution boundary as
// Batch; the owning domain remains responsible for Member existence.
type DeleteLocaleInput struct {
	DocumentID           uuid.UUID
	ExpectedRevision     uuid.UUID
	Locale               string
	ContributorMemberIDs []uuid.UUID
}

// LocaleMetadataDeletion removes the owning domain's locale metadata inside
// the caller-owned transaction. It reports whether domain state changed.
type LocaleMetadataDeletion func(ctx context.Context, tx *gorm.DB) (bool, error)

// Snapshot is the aggregate base graph plus every locale overlay.
type Snapshot struct {
	Document       Document        `json:"document"`
	SourceLocale   string          `json:"sourceLocale"`
	SnapshotDigest string          `json:"snapshotDigest"`
	Blocks         []BaseBlock     `json:"blocks"`
	LocaleOverlays []LocaleOverlay `json:"localeOverlays"`
}

// Result is the minimal acknowledgement returned by accepted and semantic
// no-op writes. Hot writes never materialize the aggregate or a content hash.
type Result struct {
	DocumentRevision         uuid.UUID `json:"documentRevision"`
	Changed                  bool      `json:"changed"`
	ContentChanged           bool      `json:"contentChanged"`
	MetadataChanged          bool      `json:"metadataChanged"`
	TranslationSourceChanged bool      `json:"sourceChanged"`
	ChangedLocales           []string  `json:"changedLocales"`
}

// AdvanceResult reports the document CAS token after an owning-domain
// metadata callback. A semantic no-op preserves it.
type AdvanceResult struct {
	DocumentRevision         uuid.UUID `json:"documentRevision"`
	Changed                  bool      `json:"changed"`
	TranslationSourceChanged bool      `json:"sourceChanged"`
}

// MetadataEffect is derived by an owning-domain metadata callback.
type MetadataEffect struct {
	Changed                  bool
	AffectsTranslationSource bool
	SourceLocale             string
	ChangedLocales           []string
}

// MetadataMutation validates and applies one owning-domain metadata change in
// the same tx after Content Document CAS succeeds.
type MetadataMutation func(ctx context.Context, tx *gorm.DB) (MetadataEffect, error)

// TargetLocaleMetadataMutation applies the owning domain's exact target
// locale row after the Store has validated the locale-only content mutation.
// contentChanged lets the domain advance its target CAS timestamp only for an
// actual locale value change while keeping the shared document revision
// stable.
type TargetLocaleMetadataMutation func(
	ctx context.Context,
	tx *gorm.DB,
	contentChanged bool,
) (MetadataEffect, error)
