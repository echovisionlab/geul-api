package og

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

const defaultOgGenerationLease = 10 * time.Minute

const (
	FailureCodeInvalidClaim       = "invalid_claim"
	FailureCodeSourceRejected     = "source_rejected"
	FailureCodeProcessingFailed   = "processing_failed"
	FailureCodeIntegrityFailed    = "integrity_failed"
	FailureCodeCompletionRejected = "completion_rejected"
)

// Lifecycle runs the renderer's side of a generation: claim, complete and
// fail. The renderer has no request principal; the lease it was handed is its
// authority.
type Lifecycle struct {
	units mutation.UnitOfWork[Store]
	lease time.Duration
}

func NewLifecycle(units mutation.UnitOfWork[Store]) *Lifecycle {
	if units == nil {
		panic("OG generation lifecycle: units are required")
	}
	return &Lifecycle{units: units, lease: defaultOgGenerationLease}
}

type ClaimResult int

const (
	Claimed ClaimResult = iota + 1
	ClaimSkipped
)

type Claim struct {
	Result               ClaimResult
	Generation           Generation
	Target               GenerationTarget
	EntitySnapshot       ogEntitySnapshot
	RenderConfigSnapshot []byte
	ConfigRevision       string
	LeaseToken           string
	LeaseExpiresAt       *time.Time
}

func (s *Lifecycle) Claim(ctx context.Context, generationID string) (*Claim, error) {
	generationID = strings.TrimSpace(generationID)
	if _, err := uuid.Parse(generationID); err != nil {
		return nil, domainerr.InvalidField("generation_id", "must be a UUID")
	}
	now := dbtime.Now()
	claim := &Claim{Result: ClaimSkipped}
	err := s.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		return s.claim(ctx, tx.Store(), generationID, claim, now)
	})
	if err != nil {
		if missingRow(err) {
			return nil, domainerr.ResourceNotFound("og_generation", generationID)
		}
		return nil, err
	}
	return claim, nil
}

func (s *Lifecycle) claim(ctx context.Context, store Store, generationID string, claim *Claim, now time.Time) error {
	generation, target, err := lockOgGenerationAndTarget(ctx, store, generationID)
	if err != nil {
		return err
	}
	claim.Generation = generation
	claim.Target = target
	eligible, err := resolveOgGenerationClaimDisposition(ctx, store, &generation, target, claim, now)
	if err != nil || !eligible {
		return err
	}
	return s.startOgGenerationClaim(ctx, store, &generation, target, claim, now)
}

func resolveOgGenerationClaimDisposition(
	ctx context.Context,
	store Store,
	generation *Generation,
	target GenerationTarget,
	claim *Claim,
	now time.Time,
) (bool, error) {
	if generation.Status.Terminal() {
		return false, nil
	}
	if target.LatestGenerationID == nil {
		err := markOgGenerationFailed(ctx, store, generation, FailureCodeInvalidClaim, now)
		claim.Generation = *generation
		return false, err
	}
	if *target.LatestGenerationID != generation.ID {
		err := markOgGenerationSuperseded(ctx, store, generation, target.LatestGenerationID, now)
		claim.Generation = *generation
		return false, err
	}
	if !generation.DeadlineAt.After(now) {
		err := markOgGenerationFailed(ctx, store, generation, FailureCodeProcessingFailed, now)
		claim.Generation = *generation
		return false, err
	}
	if generation.Status == StatusProcessing && generation.LeaseExpiresAt != nil && generation.LeaseExpiresAt.After(now) {
		claim.LeaseExpiresAt = generation.LeaseExpiresAt
		return false, nil
	}
	return generation.Status == StatusQueued || generation.Status == StatusProcessing, nil
}

func (s *Lifecycle) startOgGenerationClaim(
	ctx context.Context,
	store Store,
	generation *Generation,
	target GenerationTarget,
	claim *Claim,
	now time.Time,
) error {
	leaseToken := uuid.NewString()
	leaseExpiresAt := now.Add(s.lease)
	if err := store.StartGeneration(ctx, generation.ID, leaseToken, leaseExpiresAt, now); err != nil {
		return err
	}
	generation.Status = StatusProcessing
	generation.ProcessingAt = &now
	generation.LeaseToken = &leaseToken
	generation.LeaseExpiresAt = &leaseExpiresAt
	generation.LastErrorCode = nil
	if err := json.Unmarshal(generation.EntitySnapshot, &claim.EntitySnapshot); err != nil {
		return fmt.Errorf("decode OG entity snapshot: %w", err)
	}
	run, err := store.Run(ctx, generation.RunID)
	if err != nil {
		return err
	}
	claim.Result = Claimed
	claim.Generation = *generation
	claim.LeaseToken = leaseToken
	claim.LeaseExpiresAt = &leaseExpiresAt
	claim.RenderConfigSnapshot = append([]byte(nil), run.RenderConfigSnapshot...)
	claim.ConfigRevision = run.ConfigRevision
	store.NotifyLifecycle(ctx, LifecycleEvent{Generation: *generation, Target: target, Status: StatusProcessing, OccurredAt: now})
	return store.StartRun(ctx, run.ID, now)
}

// Complete finishes a generation from the renderer's written asset and
// returns the status it settled in, with the asset when it completed one.
func (s *Lifecycle) Complete(
	ctx context.Context,
	generationID string,
	leaseToken string,
	written *WrittenAsset,
) (GenerationStatus, *AssetRef, error) {
	now := dbtime.Now()
	status := StatusReady
	var completed *AssetRef
	err := s.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		resultStatus, asset, err := s.complete(ctx, tx.Store(), generationID, leaseToken, written, now)
		status = resultStatus
		completed = asset
		return err
	})
	return status, completed, err
}

func (s *Lifecycle) complete(
	ctx context.Context,
	store Store,
	generationID string,
	leaseToken string,
	written *WrittenAsset,
	now time.Time,
) (GenerationStatus, *AssetRef, error) {
	generation, target, err := lockOgGenerationAndTarget(ctx, store, generationID)
	if err != nil {
		return StatusReady, nil, err
	}
	if written == nil || strings.TrimSpace(written.AssetID) != generation.ID {
		return StatusReady, nil, domainerr.InvalidField("written.asset_id", "must equal generation_id")
	}
	if generation.Status == StatusReady || generation.Status == StatusSuperseded {
		if generation.LeaseToken == nil || *generation.LeaseToken != strings.TrimSpace(leaseToken) {
			return StatusReady, nil, domainerr.Precondition("OG generation completion lease does not match")
		}
		ref, err := completeAsset(ctx, store, *written)
		if err != nil {
			return StatusReady, nil, err
		}
		return generation.Status, &ref, nil
	}
	if err := validateOgGenerationCompletionLease(generation, leaseToken, now); err != nil {
		return StatusReady, nil, err
	}
	asset, err := store.CompleteAsset(ctx, *written)
	if err != nil {
		return StatusReady, nil, err
	}
	status, err := finalizeNewOgGenerationCompletion(ctx, store, &generation, target, asset, leaseToken, now)
	if err != nil {
		return status, nil, err
	}
	ref, err := store.AssetRef(asset)
	if err != nil {
		return status, nil, err
	}
	return status, &ref, nil
}

func completeAsset(ctx context.Context, store Store, written WrittenAsset) (AssetRef, error) {
	asset, err := store.CompleteAsset(ctx, written)
	if err != nil {
		return AssetRef{}, err
	}
	return store.AssetRef(asset)
}

func finalizeNewOgGenerationCompletion(
	ctx context.Context,
	store Store,
	generation *Generation,
	target GenerationTarget,
	asset Asset,
	leaseToken string,
	now time.Time,
) (GenerationStatus, error) {
	if !generation.DeadlineAt.After(now) || target.LatestGenerationID == nil {
		return StatusFailed, markOgGenerationFailed(ctx, store, generation, FailureCodeCompletionRejected, now)
	}
	if generation.Status == StatusSuperseded || *target.LatestGenerationID != generation.ID {
		if generation.Status != StatusSuperseded {
			if err := markOgGenerationSuperseded(ctx, store, generation, target.LatestGenerationID, now); err != nil {
				return StatusSuperseded, err
			}
		}
		return StatusSuperseded, refreshOgRunStatus(ctx, store, generation.RunID, now)
	}
	claimed, err := store.ClaimLatestTarget(ctx, target.ID, generation.ID, now)
	if err != nil {
		return StatusReady, err
	}
	if !claimed {
		return StatusReady, domainerr.Precondition("OG generation is no longer the latest target")
	}
	if err := bindCompletedOgAsset(ctx, store, *generation, target, asset); err != nil {
		if errors.Is(err, ErrTranslationTargetMissing) {
			slog.WarnContext(ctx, "OG completion target missing", failurecause.Attr(err), "generation_id", generation.ID, "run_id", generation.RunID)
			// No replacement exists when a locale was removed. Superseded
			// requires a real successor; finish this rejected completion as
			// failed so its lease cannot keep retrying an invalid DB transition.
			return StatusFailed, markOgGenerationFailed(ctx, store, generation, FailureCodeCompletionRejected, now)
		}
		return StatusReady, err
	}
	ready, err := store.MarkGenerationReady(ctx, generation.ID, leaseToken, now)
	if err != nil {
		return StatusReady, err
	}
	if !ready {
		return StatusReady, domainerr.Precondition("OG generation lease changed while completing")
	}
	generation.Status = StatusReady
	ref, err := store.AssetRef(asset)
	if err != nil {
		return StatusReady, err
	}
	store.NotifyLifecycle(ctx, LifecycleEvent{Generation: *generation, Target: target, Status: StatusReady, Asset: &ref, OccurredAt: now})
	return StatusReady, refreshOgRunStatus(ctx, store, generation.RunID, now)
}

func bindCompletedOgAsset(
	ctx context.Context,
	store Store,
	generation Generation,
	target GenerationTarget,
	asset Asset,
) error {
	var snapshot ogEntitySnapshot
	if err := json.Unmarshal(generation.EntitySnapshot, &snapshot); err != nil {
		return domainerr.Precondition("OG generation entity snapshot is invalid").WithCause(failurecause.ServerFault(failurecause.WithAttrs(err,
			slog.String("generation_id", generation.ID), slog.String("run_id", generation.RunID))))
	}
	return store.CompleteProjection(ctx, Target{
		EntityType: target.EntityType,
		EntityID:   target.EntityID,
		Locale:     target.Locale,
		Kind:       target.TargetKind,
	}, asset.ID)
}

// Fail records the renderer's bounded failure code for a leased generation.
func (s *Lifecycle) Fail(ctx context.Context, generationID string, leaseToken string, code string) error {
	code, err := boundedOgFailureCode(code)
	if err != nil {
		return err
	}
	now := dbtime.Now()
	return s.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		store := tx.Store()
		generation, target, err := lockOgGenerationAndTarget(ctx, store, generationID)
		if err != nil {
			return err
		}
		if generation.Status == StatusSuperseded || generation.Status == StatusCancelled {
			return nil
		}
		if err := validateOgGenerationLease(generation, leaseToken, now); err != nil {
			return err
		}
		if target.LatestGenerationID == nil {
			return markOgGenerationFailed(ctx, store, &generation, FailureCodeCompletionRejected, now)
		}
		if *target.LatestGenerationID != generation.ID {
			return markOgGenerationSuperseded(ctx, store, &generation, target.LatestGenerationID, now)
		}
		return markOgGenerationFailed(ctx, store, &generation, code, now)
	})
}

// GenerationStatus reads the stored status of a generation.
func (s *Lifecycle) GenerationStatus(ctx context.Context, generationID string) (GenerationStatus, error) {
	var status GenerationStatus
	err := s.units.Read(ctx, func(store Store) error {
		generation, err := store.Generation(ctx, generationID)
		status = generation.Status
		return err
	})
	return status, err
}

// CancelEntity cancels every active generation of one entity in the owning
// mutation's transaction and clears its targets' pointers.
func CancelEntity(ctx context.Context, store Store, now time.Time, entityType string, entityID string) error {
	policy, ok := PolicyForEntityName(entityType)
	if !ok {
		return domainerr.InvalidEntityType(entityType)
	}
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return domainerr.Required("entity_id")
	}
	if policy.CanonicalEntityID != "" && entityID != policy.CanonicalEntityID {
		if policy.Name == entitySite {
			return domainerr.InvalidField("entity_id", "site OG targets must use the canonical site identity")
		}
		return domainerr.InvalidField("entity_id", "legal OG targets must use the canonical route identity")
	}
	targets, err := store.LockEntityTargets(ctx, policy.Name, entityID)
	if err != nil {
		return err
	}
	now = now.UTC()
	runIDs := make(map[string]struct{})
	for _, target := range targets {
		generations, err := store.LockActiveGenerations(ctx, target.ID)
		if err != nil {
			return err
		}
		for _, generation := range generations {
			cancelled, err := store.MarkGenerationCancelled(ctx, generation.ID, generation.Status, now)
			if err != nil {
				return err
			}
			if !cancelled {
				return domainerr.Precondition("OG generation changed while cancelling entity")
			}
			generation.Status = StatusCancelled
			generation.CancelledAt = &now
			generation.CompletedAt = &now
			store.NotifyLifecycle(ctx, LifecycleEvent{Generation: generation, Target: target, Status: StatusCancelled, OccurredAt: now})
			runIDs[generation.RunID] = struct{}{}
		}
		if err := store.ClearTargetPointer(ctx, target.ID, now); err != nil {
			return err
		}
	}
	for runID := range runIDs {
		if err := refreshOgRunStatus(ctx, store, runID, now); err != nil {
			return err
		}
	}
	return nil
}

func boundedOgFailureCode(code string) (string, error) {
	switch code = strings.TrimSpace(code); code {
	case FailureCodeInvalidClaim,
		FailureCodeSourceRejected,
		FailureCodeProcessingFailed,
		FailureCodeIntegrityFailed,
		FailureCodeCompletionRejected:
		return code, nil
	default:
		return "", domainerr.InvalidField("error_code", "unsupported OG failure code")
	}
}

func lockOgGenerationAndTarget(ctx context.Context, store Store, generationID string) (Generation, GenerationTarget, error) {
	generation, target, err := store.LockGeneration(ctx, strings.TrimSpace(generationID))
	if err != nil {
		return Generation{}, GenerationTarget{}, err
	}
	if generation.TargetID != target.ID {
		return Generation{}, GenerationTarget{}, domainerr.Precondition("OG generation target changed while locking")
	}
	return generation, target, nil
}

func validateOgGenerationLease(generation Generation, leaseToken string, now time.Time) error {
	leaseToken = strings.TrimSpace(leaseToken)
	if generation.Status != StatusProcessing || generation.LeaseToken == nil || *generation.LeaseToken != leaseToken {
		return domainerr.Precondition("OG generation lease is not active")
	}
	if generation.LeaseExpiresAt == nil || !generation.LeaseExpiresAt.After(now) {
		return domainerr.Precondition("OG generation lease has expired")
	}
	return nil
}

func validateOgGenerationCompletionLease(generation Generation, leaseToken string, now time.Time) error {
	leaseToken = strings.TrimSpace(leaseToken)
	if generation.Status != StatusProcessing && generation.Status != StatusSuperseded {
		return domainerr.Precondition("OG generation is not completable")
	}
	if generation.LeaseToken == nil || *generation.LeaseToken != leaseToken {
		return domainerr.Precondition("OG generation lease is not active")
	}
	if generation.LeaseExpiresAt == nil || !generation.LeaseExpiresAt.After(now) {
		return domainerr.Precondition("OG generation lease has expired")
	}
	return nil
}

func markOgGenerationSuperseded(
	ctx context.Context,
	store Store,
	generation *Generation,
	replacementID *string,
	now time.Time,
) error {
	if generation.Status == StatusSuperseded {
		return nil
	}
	if generation.Status != StatusQueued && generation.Status != StatusProcessing {
		return domainerr.Precondition("only an active OG generation can be superseded")
	}
	supersededBy := replacementID
	if supersededBy != nil && *supersededBy == generation.ID {
		supersededBy = nil
	}
	// A processing generation keeps its lease so its renderer can still
	// replay the completion it may already have written.
	superseded, err := store.MarkGenerationSuperseded(ctx, generation.ID, supersededBy, generation.Status != StatusProcessing, now)
	if err != nil {
		return err
	}
	if !superseded {
		return domainerr.Precondition("OG generation changed while superseding")
	}
	generation.Status = StatusSuperseded
	generation.SupersededAt = &now
	generation.CompletedAt = &now
	target, err := store.Target(ctx, generation.TargetID)
	if err != nil {
		return err
	}
	store.NotifyLifecycle(ctx, LifecycleEvent{
		Generation: *generation, Target: target, Status: StatusSuperseded, ReplacementID: replacementID, OccurredAt: now,
	})
	return refreshOgRunStatus(ctx, store, generation.RunID, now)
}

func markOgGenerationFailed(ctx context.Context, store Store, generation *Generation, code string, now time.Time) error {
	boundedCode, err := boundedOgFailureCode(code)
	if err != nil {
		return err
	}
	failed, err := store.MarkGenerationFailed(ctx, generation.ID, generation.Status, boundedCode, now)
	if err != nil {
		return err
	}
	if !failed {
		return domainerr.Precondition("OG generation changed while failing")
	}
	generation.Status = StatusFailed
	generation.FailedAt = &now
	generation.CompletedAt = &now
	generation.LastErrorCode = optionalString(&boundedCode)
	target, err := store.Target(ctx, generation.TargetID)
	if err != nil {
		return err
	}
	store.NotifyLifecycle(ctx, LifecycleEvent{Generation: *generation, Target: target, Status: StatusFailed, OccurredAt: now})
	return refreshOgRunStatus(ctx, store, generation.RunID, now)
}

// refreshOgRunStatus settles a run from its generations' statuses once the
// run row is locked, so concurrent terminal updates count each other.
func refreshOgRunStatus(ctx context.Context, store Store, runID string, now time.Time) error {
	if err := store.LockRun(ctx, runID); err != nil {
		return err
	}
	counts, err := store.RunStatusCounts(ctx, runID)
	if err != nil {
		return err
	}
	status, completed := settledRunStatus(counts)
	var completedAt *time.Time
	if completed {
		completedAt = &now
	}
	return store.SettleRun(ctx, runID, status, completedAt, now)
}

func settledRunStatus(counts map[GenerationStatus]int64) (RunStatus, bool) {
	var total int64
	for _, count := range counts {
		total += count
	}
	terminal := counts[StatusReady] + counts[StatusFailed] + counts[StatusSuperseded] + counts[StatusCancelled]
	if total == 0 || terminal != total {
		return RunRunning, false
	}
	switch {
	case counts[StatusCancelled]+counts[StatusSuperseded] == total:
		return RunCancelled, true
	case counts[StatusReady] == total:
		return RunReady, true
	case counts[StatusFailed] > 0 && counts[StatusReady] > 0:
		return RunPartialFailed, true
	case counts[StatusFailed] == 0:
		return RunCancelled, true
	default:
		return RunFailed, true
	}
}

func missingRow(err error) bool {
	return errors.Is(err, ErrGenerationNotFound) || errors.Is(err, ErrTargetNotFound) || errors.Is(err, ErrRunNotFound)
}
