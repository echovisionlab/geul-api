package audience

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

// CreateSegmentCommand creates an audience segment.
type CreateSegmentCommand struct {
	Name        string
	Description *string
	SegmentType SegmentType
	Config      *SegmentConfigInput
}

// CreateSegment validates the segment, stores it with its filter relations
// and grants its policy relationship.
func (s *Service) CreateSegment(ctx context.Context, command CreateSegmentCommand) (SegmentView, error) {
	can, err := policyv1.AudienceSegment.Create()
	if err != nil {
		return SegmentView{}, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return SegmentView{}, err
	}
	if command.Name == "" {
		return SegmentView{}, domainerr.Required("name")
	}
	if command.SegmentType == segmentTypeUnspecified {
		return SegmentView{}, invalidSegment("segment type is required")
	}
	config, err := normalizeSegmentConfig(command.Config)
	if err != nil {
		return SegmentView{}, invalidSegment(err.Error())
	}
	if err := requireCreatedWindow(config); err != nil {
		return SegmentView{}, invalidSegment(err.Error())
	}
	if err := ValidateConfig(command.SegmentType, config); err != nil {
		return SegmentView{}, invalidSegment(err.Error())
	}
	created, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Segment, Store]{
		Access: freshAdmin(can),
		Change: func(ctx context.Context, store Store) (Segment, error) {
			segment, err := store.InsertSegment(ctx, Segment{
				Name: command.Name, Description: command.Description, Type: command.SegmentType, Config: config,
			})
			if err != nil {
				return segment, err
			}
			return segment, replaceConfig(ctx, store, segment.ID, config)
		},
		Audit: func(segment Segment) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditAudienceSegmentCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAudienceSegmentCreatedAuditRecord(metadata, segment.ID)
			})
		},
		Relationships: func(segment Segment) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			policyTouch, err := policyv1.AudienceSegment.TouchPolicy(segment.ID)
			if err != nil {
				return nil, nil, err
			}
			policyDelete, err := policyv1.AudienceSegment.DeletePolicy(segment.ID)
			if err != nil {
				return nil, nil, err
			}
			return []policyv1.RelationshipMutation{policyTouch}, []policyv1.RelationshipMutation{policyDelete}, nil
		},
	})
	if err != nil {
		if errors.Is(err, ErrUnknownReference) {
			return SegmentView{}, invalidSegment("one or more audience references do not exist")
		}
		return SegmentView{}, err
	}
	return SegmentView{Segment: created}, nil
}

// requireCreatedWindow rejects a Member creation window that ends before it
// starts.
func requireCreatedWindow(config Config) error {
	if config.CreatedAfter != nil && config.CreatedBefore != nil && config.CreatedAfter.After(*config.CreatedBefore) {
		return fmt.Errorf("created_after must not be later than created_before")
	}
	return nil
}

// EstimateSegmentCountQuery estimates the recipients of an unsaved segment.
type EstimateSegmentCountQuery struct {
	SegmentType SegmentType
	Config      *SegmentConfigInput
}

// EstimateSegmentCount returns the recipients the segment would select now.
func (s *Service) EstimateSegmentCount(ctx context.Context, query EstimateSegmentCountQuery) (int32, error) {
	can, err := policyv1.AudienceSegment.Create()
	if err != nil {
		return 0, err
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return 0, err
	}
	config, err := normalizeSegmentConfig(query.Config)
	if err != nil {
		return 0, invalidSegment(err.Error())
	}
	if err := ValidateConfig(query.SegmentType, config); err != nil {
		return 0, invalidSegment(err.Error())
	}
	return s.estimateCount(ctx, Segment{Type: query.SegmentType, Config: config})
}

// UpdateSegmentCommand changes a segment. A type or filter change needs the
// type and filter the editor observed, so a concurrent edit of another
// filter leaf survives.
type UpdateSegmentCommand struct {
	ID          string
	Name        *string
	Description *string
	SegmentType *SegmentType
	Config      *SegmentConfigInput
	Observed    *ObservedSegment
}

// ObservedSegment is the segment type and filter an editor read.
type ObservedSegment struct {
	SegmentType SegmentType
	// Config is required; nil means the editor sent no observed filter.
	Config *SegmentConfigInput
}

type segmentUpdatePlan struct {
	command         UpdateSegmentCommand
	requestedConfig *Config
	observed        *segmentConfigBaseline
}

type segmentConfigBaseline struct {
	segmentType SegmentType
	config      Config
}

func buildSegmentUpdatePlan(command UpdateSegmentCommand) (segmentUpdatePlan, error) {
	plan := segmentUpdatePlan{command: command}
	if command.SegmentType != nil && *command.SegmentType == segmentTypeUnspecified {
		return plan, invalidSegment("segment type is required")
	}
	if (command.SegmentType != nil || command.Config != nil) && command.Observed == nil {
		return plan, domainerr.InvalidField("observed", "segment type and config are required when updating segment type or config")
	}
	if command.Observed != nil {
		if command.Observed.SegmentType == segmentTypeUnspecified || command.Observed.Config == nil {
			return plan, invalidSegment("observed segment type and config are required")
		}
		observedConfig, err := normalizeSegmentConfig(command.Observed.Config)
		if err != nil {
			return plan, invalidSegment("observed config is invalid")
		}
		if err := ValidateConfig(command.Observed.SegmentType, observedConfig); err != nil {
			return plan, invalidSegment("observed config is invalid for its segment type")
		}
		plan.observed = &segmentConfigBaseline{segmentType: command.Observed.SegmentType, config: observedConfig}
	}
	if command.Config == nil {
		if plan.observed != nil && command.SegmentType != nil && *command.SegmentType != plan.observed.segmentType {
			// An intentional type switch resets the coupled filter config even if
			// the caller omitted config, so relation-backed filters are cleared too.
			plan.requestedConfig = &Config{}
		}
		return plan, nil
	}
	config, err := normalizeSegmentConfig(command.Config)
	if err != nil {
		return plan, invalidSegment(err.Error())
	}
	plan.requestedConfig = &config
	return plan, nil
}

// UpdateSegment applies the change to a segment no scheduled or sending
// delivery uses and returns the stored segment.
func (s *Service) UpdateSegment(ctx context.Context, command UpdateSegmentCommand) (SegmentView, error) {
	can, err := policyv1.AudienceSegment.Edit(command.ID)
	if err != nil {
		return SegmentView{}, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return SegmentView{}, err
	}
	plan, err := buildSegmentUpdatePlan(command)
	if err != nil {
		return SegmentView{}, err
	}
	var segment Segment
	updated, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[segmentUpdate, Store]{
		Lock: func(ctx context.Context, store Store) (err error) {
			segment, err = store.LockSegment(ctx, command.ID)
			return segmentNotFound(err)
		},
		Access: freshAdmin(can),
		Change: func(ctx context.Context, store Store) (segmentUpdate, error) {
			return plan.apply(ctx, store, segment)
		},
		Audit: func(update segmentUpdate) mutation.Audit {
			if len(update.changedFields) == 0 {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditAudienceSegmentUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAudienceSegmentConfigUpdatedAuditRecord(metadata, command.ID, update.changedFields)
			})
		},
	})
	if err != nil {
		return SegmentView{}, err
	}
	return updated.view, nil
}

// segmentUpdate is an applied segment change and the fields it changed.
type segmentUpdate struct {
	view          SegmentView
	changedFields []string
}

// apply changes the locked segment and reads it back.
func (plan segmentUpdatePlan) apply(ctx context.Context, store Store, segment Segment) (segmentUpdate, error) {
	if err := ensureSegmentMutableForActiveDelivery(ctx, store, segment.ID); err != nil {
		return segmentUpdate{}, err
	}
	nextType, nextConfig, err := plan.nextState(segment)
	if err != nil {
		return segmentUpdate{}, err
	}
	if err := ensureAudienceAccessTypeCompatible(ctx, store, segment, nextType); err != nil {
		return segmentUpdate{}, err
	}
	change, changedFields, configChanged := buildSegmentChange(segment, plan, nextType, nextConfig)
	if change.changed() {
		change.UpdatedAt = time.Now().UTC()
		if err := store.UpdateSegment(ctx, segment.ID, change); err != nil {
			return segmentUpdate{}, err
		}
	}
	if configChanged {
		if err := replaceConfig(ctx, store, segment.ID, nextConfig); err != nil {
			if errors.Is(err, ErrUnknownReference) {
				return segmentUpdate{}, invalidSegment("one or more audience references do not exist")
			}
			return segmentUpdate{}, err
		}
	}
	view, err := segmentView(ctx, store, segment.ID)
	if err != nil {
		return segmentUpdate{}, err
	}
	return segmentUpdate{view: view, changedFields: changedFields}, nil
}

// invalidSegment rejects a segment request with message.
func invalidSegment(message string) error {
	return domainerr.New(domainerr.Invalid, "", message)
}

func (plan segmentUpdatePlan) nextState(segment Segment) (SegmentType, Config, error) {
	nextType := segment.Type
	nextConfig := segment.Config
	if plan.observed != nil {
		typeChangedFromBaseline := plan.command.SegmentType != nil &&
			*plan.command.SegmentType != plan.observed.segmentType
		if typeChangedFromBaseline {
			nextType = *plan.command.SegmentType
			if plan.requestedConfig != nil {
				nextConfig = *plan.requestedConfig
			} else {
				nextConfig = Config{}
			}
		} else if plan.requestedConfig != nil {
			// The editor did not change type. Preserve a concurrent type switch and
			// merge each config leaf against the state the editor actually observed.
			nextConfig = mergeSegmentConfig(segment.Config, plan.observed.config, *plan.requestedConfig)
		}
	}
	if err := ValidateConfig(nextType, nextConfig); err != nil {
		return "", nextConfig, invalidSegment(err.Error())
	}
	return nextType, nextConfig, nil
}

func mergeSegmentConfig(current, observed, desired Config) Config {
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

func ensureAudienceAccessTypeCompatible(ctx context.Context, store Store, segment Segment, nextType SegmentType) error {
	if !authenticatedAccessSegmentType(segment.Type) || authenticatedAccessSegmentType(nextType) {
		return nil
	}
	granting, err := store.GrantsDownloadAccess(ctx, segment.ID)
	if err != nil || !granting {
		return err
	}
	return domainerr.Precondition("audience segment grants download access and cannot change to this type")
}

func buildSegmentChange(
	segment Segment,
	plan segmentUpdatePlan,
	nextType SegmentType,
	nextConfig Config,
) (SegmentChange, []string, bool) {
	var change SegmentChange
	changedFields := make([]string, 0, 8)
	if plan.command.Name != nil && *plan.command.Name != segment.Name {
		change.Name = plan.command.Name
		changedFields = append(changedFields, "name")
	}
	if plan.command.Description != nil && !equalOptionalString(*plan.command.Description, segment.Description) {
		change.Description = plan.command.Description
		changedFields = append(changedFields, "description")
	}
	if plan.command.SegmentType != nil && nextType != segment.Type {
		change.Type = &nextType
		changedFields = append(changedFields, "segment_type")
	}
	if plan.requestedConfig == nil {
		return change, changedFields, false
	}
	configChanged := false
	if !equalTime(nextConfig.CreatedAfter, segment.Config.CreatedAfter) {
		change.SetCreatedAfter, change.CreatedAfter = true, nextConfig.CreatedAfter
		changedFields = append(changedFields, "created_after")
		configChanged = true
	}
	if !equalTime(nextConfig.CreatedBefore, segment.Config.CreatedBefore) {
		change.SetCreatedBefore, change.CreatedBefore = true, nextConfig.CreatedBefore
		changedFields = append(changedFields, "created_before")
		configChanged = true
	}
	if !slices.Equal(nextConfig.MemberTagIDs, segment.Config.MemberTagIDs) {
		changedFields = append(changedFields, "member_tag_ids")
		configChanged = true
	}
	if !slices.Equal(nextConfig.AccountRoles, segment.Config.AccountRoles) {
		changedFields = append(changedFields, "account_roles")
		configChanged = true
	}
	if !slices.Equal(nextConfig.ExcludeMemberIDs, segment.Config.ExcludeMemberIDs) {
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
