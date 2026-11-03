package criteria

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// CreateCriteriaCommand creates a Member criteria.
type CreateCriteriaCommand struct {
	Name        string
	Description *string
	Kind        Kind
	Config      *CriteriaConfigInput
}

// CreateCriteria validates the criteria, stores it with its filter relations
// and grants its policy relationship.
func (s *Service) CreateCriteria(ctx context.Context, command CreateCriteriaCommand) (CriteriaView, error) {
	can, err := policyv1.MemberCriteria.Create()
	if err != nil {
		return CriteriaView{}, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return CriteriaView{}, err
	}
	if command.Name == "" {
		return CriteriaView{}, domainerr.Required("name")
	}
	if command.Kind == kindUnspecified {
		return CriteriaView{}, invalidCriteria("criteria type is required")
	}
	config, err := normalizeCriteriaConfig(command.Config)
	if err != nil {
		return CriteriaView{}, invalidCriteria(err.Error())
	}
	if err := requireCreatedWindow(config); err != nil {
		return CriteriaView{}, invalidCriteria(err.Error())
	}
	if err := ValidateConfig(command.Kind, config); err != nil {
		return CriteriaView{}, invalidCriteria(err.Error())
	}
	created, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Criteria, Store]{
		Access: freshAdmin(can),
		Change: func(ctx context.Context, store Store) (Criteria, error) {
			criteria, err := store.InsertCriteria(ctx, Criteria{
				Name: command.Name, Description: command.Description, Type: command.Kind, Config: config,
			})
			if err != nil {
				return criteria, err
			}
			return criteria, replaceConfig(ctx, store, criteria.ID, config)
		},
		Audit: func(criteria Criteria) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMemberCriteriaCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberCriteriaCreatedAuditRecord(metadata, criteria.ID)
			})
		},
		Relationships: func(criteria Criteria) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			policyTouch, err := policyv1.MemberCriteria.TouchPolicy(criteria.ID)
			if err != nil {
				return nil, nil, err
			}
			policyDelete, err := policyv1.MemberCriteria.DeletePolicy(criteria.ID)
			if err != nil {
				return nil, nil, err
			}
			return []policyv1.RelationshipMutation{policyTouch}, []policyv1.RelationshipMutation{policyDelete}, nil
		},
	})
	if err != nil {
		if errors.Is(err, ErrUnknownReference) {
			return CriteriaView{}, invalidCriteria("one or more criteria references do not exist")
		}
		return CriteriaView{}, err
	}
	return CriteriaView{Criteria: created}, nil
}

// requireCreatedWindow rejects a Member creation window that ends before it
// starts.
func requireCreatedWindow(config Config) error {
	if config.CreatedAfter != nil && config.CreatedBefore != nil && config.CreatedAfter.After(*config.CreatedBefore) {
		return fmt.Errorf("created_after must not be later than created_before")
	}
	return nil
}

// EstimateCriteriaCountQuery estimates the recipients of an unsaved criteria.
type EstimateCriteriaCountQuery struct {
	Kind   Kind
	Config *CriteriaConfigInput
}

// EstimateCriteriaCount returns the recipients the criteria would select now.
func (s *Service) EstimateCriteriaCount(ctx context.Context, query EstimateCriteriaCountQuery) (int32, error) {
	can, err := policyv1.MemberCriteria.Create()
	if err != nil {
		return 0, err
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return 0, err
	}
	config, err := normalizeCriteriaConfig(query.Config)
	if err != nil {
		return 0, invalidCriteria(err.Error())
	}
	if err := ValidateConfig(query.Kind, config); err != nil {
		return 0, invalidCriteria(err.Error())
	}
	return s.estimateCount(ctx, Criteria{Type: query.Kind, Config: config})
}

// UpdateCriteriaCommand changes a criteria. A type or filter change needs the
// type and filter the editor observed, so a concurrent edit of another
// filter leaf survives.
type UpdateCriteriaCommand struct {
	ID          string
	Name        *string
	Description *string
	Kind        *Kind
	Config      *CriteriaConfigInput
	Observed    *ObservedCriteria
}

// ObservedCriteria is the criteria type and filter an editor read.
type ObservedCriteria struct {
	Kind Kind
	// Config is required; nil means the editor sent no observed filter.
	Config *CriteriaConfigInput
}

type criteriaUpdatePlan struct {
	command         UpdateCriteriaCommand
	requestedConfig *Config
	observed        *criteriaConfigBaseline
}

type criteriaConfigBaseline struct {
	kind   Kind
	config Config
}

func buildCriteriaUpdatePlan(command UpdateCriteriaCommand) (criteriaUpdatePlan, error) {
	plan := criteriaUpdatePlan{command: command}
	if command.Kind != nil && *command.Kind == kindUnspecified {
		return plan, invalidCriteria("criteria type is required")
	}
	if (command.Kind != nil || command.Config != nil) && command.Observed == nil {
		return plan, domainerr.InvalidField("observed", "criteria type and config are required when updating criteria type or config")
	}
	if command.Observed != nil {
		if command.Observed.Kind == kindUnspecified || command.Observed.Config == nil {
			return plan, invalidCriteria("observed criteria type and config are required")
		}
		observedConfig, err := normalizeCriteriaConfig(command.Observed.Config)
		if err != nil {
			return plan, invalidCriteria("observed config is invalid")
		}
		if err := ValidateConfig(command.Observed.Kind, observedConfig); err != nil {
			return plan, invalidCriteria("observed config is invalid for its criteria type")
		}
		plan.observed = &criteriaConfigBaseline{kind: command.Observed.Kind, config: observedConfig}
	}
	if command.Config == nil {
		if plan.observed != nil && command.Kind != nil && *command.Kind != plan.observed.kind {
			// An intentional type switch resets the coupled filter config even if
			// the caller omitted config, so relation-backed filters are cleared too.
			plan.requestedConfig = &Config{}
		}
		return plan, nil
	}
	config, err := normalizeCriteriaConfig(command.Config)
	if err != nil {
		return plan, invalidCriteria(err.Error())
	}
	plan.requestedConfig = &config
	return plan, nil
}

// UpdateCriteria applies the change to a criteria no scheduled or sending
// delivery uses and returns the stored criteria.
func (s *Service) UpdateCriteria(ctx context.Context, command UpdateCriteriaCommand) (CriteriaView, error) {
	can, err := policyv1.MemberCriteria.Edit(command.ID)
	if err != nil {
		return CriteriaView{}, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return CriteriaView{}, err
	}
	plan, err := buildCriteriaUpdatePlan(command)
	if err != nil {
		return CriteriaView{}, err
	}
	var criteria Criteria
	updated, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[criteriaUpdate, Store]{
		Lock: func(ctx context.Context, store Store) (err error) {
			criteria, err = store.LockCriteria(ctx, command.ID)
			return criteriaNotFound(err)
		},
		Access: freshAdmin(can),
		Change: func(ctx context.Context, store Store) (criteriaUpdate, error) {
			return plan.apply(ctx, store, criteria)
		},
		Audit: func(update criteriaUpdate) mutation.Audit {
			if len(update.changedFields) == 0 {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditMemberCriteriaUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberCriteriaConfigUpdatedAuditRecord(metadata, command.ID, update.changedFields)
			})
		},
	})
	if err != nil {
		return CriteriaView{}, err
	}
	return updated.view, nil
}

// criteriaUpdate is an applied criteria change and the fields it changed.
type criteriaUpdate struct {
	view          CriteriaView
	changedFields []string
}

// apply changes the locked criteria and reads it back.
func (plan criteriaUpdatePlan) apply(ctx context.Context, store Store, criteria Criteria) (criteriaUpdate, error) {
	if err := ensureCriteriaMutableForActiveDelivery(ctx, store, criteria.ID); err != nil {
		return criteriaUpdate{}, err
	}
	nextType, nextConfig, err := plan.nextState(criteria)
	if err != nil {
		return criteriaUpdate{}, err
	}
	if err := ensureCriteriaAccessTypeCompatible(ctx, store, criteria, nextType); err != nil {
		return criteriaUpdate{}, err
	}
	change, changedFields, configChanged := buildCriteriaChange(criteria, plan, nextType, nextConfig)
	if change.changed() {
		change.UpdatedAt = time.Now().UTC()
		if err := store.UpdateCriteria(ctx, criteria.ID, change); err != nil {
			return criteriaUpdate{}, err
		}
	}
	if configChanged {
		if err := replaceConfig(ctx, store, criteria.ID, nextConfig); err != nil {
			if errors.Is(err, ErrUnknownReference) {
				return criteriaUpdate{}, invalidCriteria("one or more criteria references do not exist")
			}
			return criteriaUpdate{}, err
		}
	}
	view, err := criteriaView(ctx, store, criteria.ID)
	if err != nil {
		return criteriaUpdate{}, err
	}
	return criteriaUpdate{view: view, changedFields: changedFields}, nil
}

// invalidCriteria rejects a criteria request with message.
func invalidCriteria(message string) error {
	return domainerr.New(domainerr.Invalid, "", message)
}

func (plan criteriaUpdatePlan) nextState(criteria Criteria) (Kind, Config, error) {
	nextType := criteria.Type
	nextConfig := criteria.Config
	if plan.observed != nil {
		typeChangedFromBaseline := plan.command.Kind != nil &&
			*plan.command.Kind != plan.observed.kind
		if typeChangedFromBaseline {
			nextType = *plan.command.Kind
			if plan.requestedConfig != nil {
				nextConfig = *plan.requestedConfig
			} else {
				nextConfig = Config{}
			}
		} else if plan.requestedConfig != nil {
			// The editor did not change type. Preserve a concurrent type switch and
			// merge each config leaf against the state the editor actually observed.
			nextConfig = mergeCriteriaConfig(criteria.Config, plan.observed.config, *plan.requestedConfig)
		}
	}
	if err := ValidateConfig(nextType, nextConfig); err != nil {
		return "", nextConfig, invalidCriteria(err.Error())
	}
	return nextType, nextConfig, nil
}

func mergeCriteriaConfig(current, observed, desired Config) Config {
	return Config{
		MemberTagIDs:     mergeObservedStringSet(current.MemberTagIDs, observed.MemberTagIDs, desired.MemberTagIDs),
		AccountRoles:     mergeObservedStringSet(current.AccountRoles, observed.AccountRoles, desired.AccountRoles),
		ExcludeMemberIDs: mergeObservedStringSet(current.ExcludeMemberIDs, observed.ExcludeMemberIDs, desired.ExcludeMemberIDs),
		CreatedAfter:     mergeObservedTime(current.CreatedAfter, observed.CreatedAfter, desired.CreatedAfter),
		CreatedBefore:    mergeObservedTime(current.CreatedBefore, observed.CreatedBefore, desired.CreatedBefore),
	}.sorted()
}

func mergeObservedStringSet(current, observed, desired []string) []string {
	merged := make(map[string]struct{}, len(current)+len(desired))
	for _, value := range current {
		merged[value] = struct{}{}
	}
	observedSet := make(map[string]struct{}, len(observed))
	desiredSet := make(map[string]struct{}, len(desired))
	for _, value := range observed {
		observedSet[value] = struct{}{}
	}
	for _, value := range desired {
		desiredSet[value] = struct{}{}
		if _, existed := observedSet[value]; !existed {
			merged[value] = struct{}{}
		}
	}
	for _, value := range observed {
		if _, retained := desiredSet[value]; !retained {
			delete(merged, value)
		}
	}
	result := make([]string, 0, len(merged))
	for value := range merged {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func mergeObservedTime(current, observed, desired *time.Time) *time.Time {
	if equalTime(observed, desired) {
		return current
	}
	if desired == nil {
		return nil
	}
	value := *desired
	return &value
}

func ensureCriteriaAccessTypeCompatible(ctx context.Context, store Store, criteria Criteria, nextType Kind) error {
	if !authenticatedAccessKind(criteria.Type) || authenticatedAccessKind(nextType) {
		return nil
	}
	granting, err := store.GrantsDownloadAccess(ctx, criteria.ID)
	if err != nil || !granting {
		return err
	}
	return domainerr.Precondition("Member criteria grants download access and cannot change to this type")
}

func buildCriteriaChange(
	criteria Criteria,
	plan criteriaUpdatePlan,
	nextType Kind,
	nextConfig Config,
) (CriteriaChange, []string, bool) {
	var change CriteriaChange
	changedFields := make([]string, 0, 8)
	if plan.command.Name != nil && *plan.command.Name != criteria.Name {
		change.Name = plan.command.Name
		changedFields = append(changedFields, "name")
	}
	if plan.command.Description != nil && !equalOptionalString(*plan.command.Description, criteria.Description) {
		change.Description = plan.command.Description
		changedFields = append(changedFields, "description")
	}
	if plan.command.Kind != nil && nextType != criteria.Type {
		change.Type = &nextType
		changedFields = append(changedFields, "kind")
	}
	if plan.requestedConfig == nil {
		return change, changedFields, false
	}
	configChanged := false
	if !equalTime(nextConfig.CreatedAfter, criteria.Config.CreatedAfter) {
		change.SetCreatedAfter, change.CreatedAfter = true, nextConfig.CreatedAfter
		changedFields = append(changedFields, "created_after")
		configChanged = true
	}
	if !equalTime(nextConfig.CreatedBefore, criteria.Config.CreatedBefore) {
		change.SetCreatedBefore, change.CreatedBefore = true, nextConfig.CreatedBefore
		changedFields = append(changedFields, "created_before")
		configChanged = true
	}
	if !slices.Equal(nextConfig.MemberTagIDs, criteria.Config.MemberTagIDs) {
		changedFields = append(changedFields, "member_tag_ids")
		configChanged = true
	}
	if !slices.Equal(nextConfig.AccountRoles, criteria.Config.AccountRoles) {
		changedFields = append(changedFields, "account_roles")
		configChanged = true
	}
	if !slices.Equal(nextConfig.ExcludeMemberIDs, criteria.Config.ExcludeMemberIDs) {
		changedFields = append(changedFields, "exclude_member_ids")
		configChanged = true
	}
	return change, changedFields, configChanged
}

func equalOptionalString(next string, current *string) bool {
	return current != nil && next == *current
}

func equalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
