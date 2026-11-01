package og

import (
	"context"
	"time"
)

// GenerationStatus is a stored og_generation status.
type GenerationStatus string

const (
	StatusQueued     GenerationStatus = "queued"
	StatusProcessing GenerationStatus = "processing"
	StatusReady      GenerationStatus = "ready"
	StatusFailed     GenerationStatus = "failed"
	StatusSuperseded GenerationStatus = "superseded"
	StatusCancelled  GenerationStatus = "cancelled"
)

// Terminal reports whether no further transition leaves status.
func (status GenerationStatus) Terminal() bool {
	switch status {
	case StatusReady, StatusFailed, StatusSuperseded, StatusCancelled:
		return true
	default:
		return false
	}
}

// RunStatus is a stored og_generation_run status.
type RunStatus string

const (
	RunQueued        RunStatus = "queued"
	RunRunning       RunStatus = "running"
	RunReady         RunStatus = "ready"
	RunPartialFailed RunStatus = "partial_failed"
	RunFailed        RunStatus = "failed"
	RunCancelled     RunStatus = "cancelled"
)

// Target kinds of a stored generation target.
const (
	KindEntity = "entity"
	KindLocale = "locale"
)

// GenerationTarget is one entity or entity locale that OG renders for. Its
// LatestGenerationID is the compare-and-swap token completion checks.
type GenerationTarget struct {
	ID                 string
	EntityType         string
	EntityID           string
	TargetKind         string
	Locale             *string
	LatestGenerationID *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Generation is one render of a target, keyed by the ID of its output asset.
// EntitySnapshot is the encoded snapshot the renderer reads.
type Generation struct {
	ID              string
	RunID           string
	TargetID        string
	RequestSequence int64
	Status          GenerationStatus
	EntitySnapshot  []byte
	ProcessingAt    *time.Time
	LeaseToken      *string
	LeaseExpiresAt  *time.Time
	DeadlineAt      time.Time
	LastErrorCode   *string
	ReadyAt         *time.Time
	FailedAt        *time.Time
	SupersededAt    *time.Time
	SupersededByID  *string
	CancelledAt     *time.Time
	CompletedAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Run groups the generations of one request and pins their render config.
type Run struct {
	ID                   string
	TriggerKind          string
	Reason               string
	RenderConfigSnapshot []byte
	ConfigRevision       string
	Status               RunStatus
	StartedAt            *time.Time
	CompletedAt          *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// Output is the public asset a new generation allocates for its raster.
type Output struct {
	AssetID   string
	ObjectKey string
	Extension string
	MimeType  string
}

// NewGeneration is a queued generation with its allocated output asset.
type NewGeneration struct {
	Generation
	Output Output
}

// TargetPointer moves a target's latest generation pointer.
type TargetPointer struct {
	TargetID     string
	GenerationID string
}

// TargetKey identifies a stored target by its entity, kind and locale.
type TargetKey struct {
	EntityType string
	EntityID   string
	Kind       string
	Locale     *string
}

// AssetStatus is a stored public_asset status.
type AssetStatus string

const (
	AssetAllocated     AssetStatus = "allocated"
	AssetReady         AssetStatus = "ready"
	AssetFailed        AssetStatus = "failed"
	AssetDeletePending AssetStatus = "delete_pending"
	AssetDeleted       AssetStatus = "deleted"
)

// assetKindOG is the public asset kind of every OG raster.
const assetKindOG = "og"

// Asset is the part of a public asset OG reads.
type Asset struct {
	ID           string
	SourceFileID *string
	Kind         string
	Extension    string
	MimeType     string
	Disposition  string
	FileSize     *int64
	SHA256       []byte
	Status       AssetStatus
	ReadyAt      *time.Time
	CreatedAt    time.Time
}

// LifecycleEvent is the notification of one generation transition.
type LifecycleEvent struct {
	Generation    Generation
	Target        GenerationTarget
	Status        GenerationStatus
	ReplacementID *string
	Asset         *AssetRef
	OccurredAt    time.Time
}

// Store is OG's persistence and the ports it reaches inside one
// transaction: entity request sources, projections, render config, media
// assets and the job queue.
type Store interface {
	// LockTargets inserts the targets of proposals that do not exist yet and
	// locks every one FOR UPDATE, in the order of proposals.
	LockTargets(ctx context.Context, proposals []GenerationTarget) ([]GenerationTarget, error)
	// PointTargets moves each target's latest generation and reports how
	// many targets it updated.
	PointTargets(ctx context.Context, pointers []TargetPointer, now time.Time) (int64, error)
	// ClaimLatestTarget touches the target only while generationID is its
	// latest generation.
	ClaimLatestTarget(ctx context.Context, targetID, generationID string, now time.Time) (bool, error)
	ClearTargetPointer(ctx context.Context, targetID string, now time.Time) error
	// LockEntityTargets locks every target of one entity FOR UPDATE.
	LockEntityTargets(ctx context.Context, entityType, entityID string) ([]GenerationTarget, error)
	Target(ctx context.Context, targetID string) (GenerationTarget, error)
	FindTarget(ctx context.Context, key TargetKey) (GenerationTarget, error)

	// LockGeneration locks the generation's target, then the generation,
	// FOR UPDATE.
	LockGeneration(ctx context.Context, generationID string) (Generation, GenerationTarget, error)
	Generation(ctx context.Context, generationID string) (Generation, error)
	RunGenerations(ctx context.Context, runID string) ([]Generation, error)
	// InsertGenerations allocates each output asset, then stores the
	// generations.
	InsertGenerations(ctx context.Context, generations []NewGeneration) error
	// LockActiveGenerations locks the queued and processing generations of a
	// target FOR UPDATE.
	LockActiveGenerations(ctx context.Context, targetID string) ([]Generation, error)
	// SupersedeReplacedGeneration supersedes the target's active generation
	// by replacementID, keeping its lease.
	SupersedeReplacedGeneration(ctx context.Context, targetID, generationID, replacementID string, now time.Time) (bool, error)
	StartGeneration(ctx context.Context, generationID, leaseToken string, leaseExpiresAt, now time.Time) error
	// MarkGenerationReady finishes a processing generation that still holds
	// leaseToken.
	MarkGenerationReady(ctx context.Context, generationID, leaseToken string, now time.Time) (bool, error)
	// MarkGenerationFailed fails a generation still in status from.
	MarkGenerationFailed(ctx context.Context, generationID string, from GenerationStatus, code string, now time.Time) (bool, error)
	// MarkGenerationSuperseded supersedes a queued or processing generation.
	MarkGenerationSuperseded(ctx context.Context, generationID string, replacementID *string, clearLease bool, now time.Time) (bool, error)
	// MarkGenerationCancelled cancels a generation still in status from.
	MarkGenerationCancelled(ctx context.Context, generationID string, from GenerationStatus, now time.Time) (bool, error)

	InsertRun(ctx context.Context, run Run) error
	Run(ctx context.Context, runID string) (Run, error)
	// StartRun moves a queued run to running.
	StartRun(ctx context.Context, runID string, now time.Time) error
	// LockRun locks the run FOR UPDATE, serializing its status refreshes.
	LockRun(ctx context.Context, runID string) error
	RunStatusCounts(ctx context.Context, runID string) (map[GenerationStatus]int64, error)
	// SettleRun stores a refreshed status and marks the run started.
	SettleRun(ctx context.Context, runID string, status RunStatus, completedAt *time.Time, now time.Time) error

	// RenderConfigSnapshot reads the shared render configuration and its
	// revision.
	RenderConfigSnapshot(ctx context.Context) ([]byte, string, error)
	ResolvePublicText(ctx context.Context, text string) (string, error)
	// ReadyAssetsForFiles returns the newest ready public asset of each
	// source file that has one.
	ReadyAssetsForFiles(ctx context.Context, fileIDs []string) (map[string]AssetRef, error)
	// ResolveRequests asks the entity's request source for the selected
	// targets; a missing entity answers ErrEntityNotFound.
	ResolveRequests(ctx context.Context, entityType, entityID string, selection Selection) ([]Request, error)
	// CollectRequests asks every request source for its current targets.
	CollectRequests(ctx context.Context) ([]Request, error)
	ReconcileBeforeGlobalGeneration(ctx context.Context) error

	// ReleasePendingProjection clears the projection a pending render
	// replaces, so public metadata falls back until it completes.
	ReleasePendingProjection(ctx context.Context, target Target) error
	// CompleteProjection points the target's projection at assetID and binds
	// it; a removed locale answers ErrTranslationTargetMissing.
	CompleteProjection(ctx context.Context, target Target, assetID string, now time.Time) error

	CompleteAsset(ctx context.Context, written WrittenAsset) (Asset, error)
	AssetRef(asset Asset) (AssetRef, error)
	Asset(ctx context.Context, assetID string) (Asset, bool, error)

	UnboundReadyAssetIDs(ctx context.Context, cutoff time.Time) ([]string, error)
	ExpiredTerminalAssetIDs(ctx context.Context, cutoff time.Time) ([]string, error)
	LockAsset(ctx context.Context, assetID string) (Asset, bool, error)
	AssetBound(ctx context.Context, assetID string) (bool, error)
	// AssetReferenced reports whether any entity row still points at the
	// asset.
	AssetReferenced(ctx context.Context, assetID string) (bool, error)
	MarkUnboundAssetDeletePending(ctx context.Context, assetID string, cutoff, now time.Time) (bool, error)
	MarkTerminalAssetDeletePending(ctx context.Context, assetID string, cutoff, now time.Time) (bool, error)

	// NotifyLifecycle publishes event best effort: a failure is logged and
	// never aborts the transaction.
	NotifyLifecycle(ctx context.Context, event LifecycleEvent)
	EnqueueGeneration(ctx context.Context, generationID string) error
}
