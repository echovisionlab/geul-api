package og

import (
	"context"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// AdminAuthorizer owns account and resource authorization outside the OG lifecycle.
type AdminAuthorizer interface {
	RequireAuthenticated(context.Context) error
	RequireAdmin(context.Context) error
	AuthorizeEntity(context.Context, string, string, bool) error
}

// Admin implements the OG-owned methods of the manage Admin service.
type Admin struct {
	units      mutation.UnitOfWork[Store]
	authorizer AdminAuthorizer
}

func NewAdmin(units mutation.UnitOfWork[Store], authorizer AdminAuthorizer) *Admin {
	if units == nil || authorizer == nil {
		panic("OG admin service dependencies are required")
	}
	return &Admin{units: units, authorizer: authorizer}
}

// RegenerateCommand selects the targets of one entity to regenerate.
// EntityType is the policy name, or the client's unknown type as it named it.
type RegenerateCommand struct {
	EntityType string
	EntityID   string
	Selection  Selection
}

// RegenerateOgImage durably creates a generation run for the selected target.
func (s *Admin) RegenerateOgImage(ctx context.Context, command RegenerateCommand) (*Plan, error) {
	if err := s.authorizer.RequireAuthenticated(ctx); err != nil {
		return nil, err
	}
	if err := s.authorizeEntityTarget(
		ctx, command.EntityType, normalizeAuthorizationEntityID(command.EntityType, command.EntityID), true,
	); err != nil {
		return nil, err
	}
	var plan *Plan
	err := s.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		store := tx.Store()
		requests, err := ResolveRequests(ctx, store, command.EntityType, command.EntityID, command.Selection)
		if err != nil {
			return err
		}
		plan, err = RequestBulk(ctx, store, dbtime.Now(), "manual", "admin_regenerate", requests,
			func(ctx context.Context, store Store) ([]Request, error) {
				return ResolveRequests(ctx, store, command.EntityType, command.EntityID, command.Selection)
			},
		)
		return err
	})
	if err != nil {
		return nil, err
	}
	return plan, nil
}

// RegenerateAllOgImages creates one atomic run for every concrete OG target.
func (s *Admin) RegenerateAllOgImages(ctx context.Context) (*Plan, error) {
	if err := s.authorizer.RequireAdmin(ctx); err != nil {
		return nil, err
	}
	var plan *Plan
	err := s.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		store := tx.Store()
		if err := store.ReconcileBeforeGlobalGeneration(ctx); err != nil {
			return err
		}
		requests, err := CollectRequests(ctx, store)
		if err != nil {
			return err
		}
		plan, err = RequestBulk(ctx, store, dbtime.Now(), "manual", "admin_regenerate_all", requests, CollectRequests)
		return err
	})
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "Queued one OG regeneration run for all targets", "run_id", plan.RunID, "generation_count", len(plan.GenerationIDs))
	return plan, nil
}

// GenerationView is a generation with its target, and its asset once ready.
type GenerationView struct {
	Generation Generation
	Target     GenerationTarget
	Asset      *AssetRef
}

func (s *Admin) GetOgGeneration(ctx context.Context, generationID string) (GenerationView, error) {
	if err := s.authorizer.RequireAuthenticated(ctx); err != nil {
		return GenerationView{}, err
	}
	generation, target, err := s.loadGeneration(ctx, generationID)
	if err != nil {
		return GenerationView{}, err
	}
	policy, ok := PolicyForEntityName(target.EntityType)
	if !ok {
		return GenerationView{}, domainerr.InvalidField("entity_type", "unknown OG target entity type")
	}
	if err := s.authorizeEntityTarget(ctx, policy.Name, target.EntityID, false); err != nil {
		return GenerationView{}, err
	}
	return s.generationView(ctx, generation, target)
}

// TargetScope is the scope a latest-generation query names.
type TargetScope int

const (
	ScopeMissing TargetScope = iota
	ScopeEntity
	ScopeLocale
)

// TargetQuery names a stored target as a client sends it. EntityType is the
// policy name, or the client's unknown type as it named it.
type TargetQuery struct {
	EntityType string
	EntityID   string
	Scope      TargetScope
	Locale     string
}

// GetLatestOgGeneration returns the latest generation of target, which is
// nil when the client sent no target.
func (s *Admin) GetLatestOgGeneration(ctx context.Context, target *TargetQuery) (GenerationView, error) {
	if err := s.authorizer.RequireAuthenticated(ctx); err != nil {
		return GenerationView{}, err
	}
	key, err := parseTargetQuery(target)
	if err != nil {
		return GenerationView{}, err
	}
	if err := s.authorizeEntityTarget(ctx, key.EntityType, key.EntityID, false); err != nil {
		return GenerationView{}, err
	}
	var stored GenerationTarget
	if err := s.units.Read(ctx, func(store Store) (err error) {
		stored, err = store.FindTarget(ctx, key)
		return err
	}); err != nil {
		if missingRow(err) {
			return GenerationView{}, domainerr.ResourceNotFound("og_generation_target", key.EntityID)
		}
		return GenerationView{}, err
	}
	if stored.LatestGenerationID == nil {
		return GenerationView{}, domainerr.ResourceNotFound("og_generation", key.EntityID)
	}
	generation, _, err := s.loadGeneration(ctx, *stored.LatestGenerationID)
	if err != nil {
		return GenerationView{}, err
	}
	return s.generationView(ctx, generation, stored)
}

// RunView is a run with its generations in request order and the targets of
// its failed generations.
type RunView struct {
	Run           Run
	Generations   []Generation
	FailedTargets map[string]GenerationTarget
}

func (s *Admin) GetOgGenerationRun(ctx context.Context, runID string) (RunView, error) {
	if err := s.authorizer.RequireAdmin(ctx); err != nil {
		return RunView{}, err
	}
	runID = strings.TrimSpace(runID)
	if _, err := uuid.Parse(runID); err != nil {
		return RunView{}, domainerr.InvalidField("run_id", "must be a UUID")
	}
	view := RunView{FailedTargets: map[string]GenerationTarget{}}
	err := s.units.Read(ctx, func(store Store) (err error) {
		if view.Run, err = store.Run(ctx, runID); err != nil {
			if missingRow(err) {
				return domainerr.ResourceNotFound("og_generation_run", runID)
			}
			return err
		}
		if view.Generations, err = store.RunGenerations(ctx, runID); err != nil {
			return err
		}
		for _, generation := range view.Generations {
			if generation.Status != StatusFailed {
				continue
			}
			target, err := store.Target(ctx, generation.TargetID)
			if err != nil {
				return err
			}
			view.FailedTargets[generation.ID] = target
		}
		return nil
	})
	return view, err
}

func (s *Admin) loadGeneration(ctx context.Context, generationID string) (Generation, GenerationTarget, error) {
	generationID = strings.TrimSpace(generationID)
	if _, err := uuid.Parse(generationID); err != nil {
		return Generation{}, GenerationTarget{}, domainerr.InvalidField("generation_id", "must be a UUID")
	}
	var generation Generation
	var target GenerationTarget
	err := s.units.Read(ctx, func(store Store) (err error) {
		if generation, err = store.Generation(ctx, generationID); err != nil {
			if missingRow(err) {
				return domainerr.ResourceNotFound("og_generation", generationID)
			}
			return err
		}
		target, err = store.Target(ctx, generation.TargetID)
		return err
	})
	return generation, target, err
}

func (s *Admin) generationView(ctx context.Context, generation Generation, target GenerationTarget) (GenerationView, error) {
	view := GenerationView{Generation: generation, Target: target}
	if generation.Status != StatusReady {
		return view, nil
	}
	err := s.units.Read(ctx, func(store Store) error {
		asset, found, err := store.Asset(ctx, generation.ID)
		if err != nil || !found || asset.Status != AssetReady {
			return err
		}
		ref, err := store.AssetRef(asset)
		if err != nil {
			return err
		}
		view.Asset = &ref
		return nil
	})
	return view, err
}

// authorizeEntityTarget prevents the author-role OG endpoints from becoming
// a cross-resource read or mutation surface. Site and legal route targets have
// no resource-manager relation, so they remain admin-only.
func (s *Admin) authorizeEntityTarget(ctx context.Context, entityType string, entityID string, requireEdit bool) error {
	if err := s.authorizer.RequireAuthenticated(ctx); err != nil {
		return err
	}
	policy, ok := PolicyForEntityName(entityType)
	if !ok {
		return domainerr.InvalidEntityType(entityType)
	}
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return domainerr.Required("entity_id")
	}
	return s.authorizer.AuthorizeEntity(ctx, policy.Name, entityID, requireEdit)
}

func normalizeAuthorizationEntityID(entityType string, entityID string) string {
	entityID = strings.TrimSpace(entityID)
	policy, ok := PolicyForEntityName(entityType)
	if !ok {
		return entityID
	}
	switch policy.LocaleStrategy {
	case LocaleStrategyStatic:
		return policy.CanonicalEntityID
	default:
		if policy.Name == entitySite && entityID == "" {
			return SiteEntityID
		}
		return entityID
	}
}

func parseTargetQuery(target *TargetQuery) (TargetKey, error) {
	if target == nil {
		return TargetKey{}, domainerr.Required("target")
	}
	policy, ok := PolicyForEntityName(target.EntityType)
	if !ok {
		return TargetKey{}, domainerr.InvalidEntityType(target.EntityType)
	}
	entityID := strings.TrimSpace(target.EntityID)
	if entityID == "" {
		return TargetKey{}, domainerr.Required("target.entity_id")
	}
	if policy.Name == entitySite && entityID != SiteEntityID {
		return TargetKey{}, domainerr.InvalidField("target.entity_id", "site OG target must use the canonical site identity")
	}
	if policy.LocaleStrategy == LocaleStrategyStatic && entityID != policy.CanonicalEntityID {
		return TargetKey{}, domainerr.InvalidField("target.entity_id", "legal OG target must use the canonical route identity")
	}
	switch target.Scope {
	case ScopeEntity:
		if policy.LocaleStrategy != LocaleStrategyBaseOnly {
			return TargetKey{}, domainerr.InvalidField("target.scope", "this OG entity requires a locale scope")
		}
		return TargetKey{EntityType: policy.Name, EntityID: entityID, Kind: KindEntity}, nil
	case ScopeLocale:
		if policy.LocaleStrategy == LocaleStrategyBaseOnly {
			return TargetKey{}, domainerr.InvalidField("target.scope", "base-only OG entities require entity scope")
		}
		locale := strings.TrimSpace(target.Locale)
		if locale == "" {
			return TargetKey{}, domainerr.Required("target.locale.locale")
		}
		return TargetKey{EntityType: policy.Name, EntityID: entityID, Kind: KindLocale, Locale: &locale}, nil
	default:
		return TargetKey{}, domainerr.Required("target.scope")
	}
}
