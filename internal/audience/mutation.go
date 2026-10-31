package audience

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// CreateSegmentCommand creates an audience segment.
type CreateSegmentCommand struct {
	Name        string
	Description *string
	SegmentType model.AudienceSegmentType
	Config      *SegmentConfigInput
}

// CreateSegment validates the segment, stores it with its filter relations
// and grants its policy relationship.
func (s *Service) CreateSegment(ctx context.Context, command CreateSegmentCommand) (*managev1.Segment, error) {
	can, err := policyv1.AudienceSegment.Create()
	if err != nil {
		return nil, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	if command.Name == "" {
		return nil, domainerr.Required("name")
	}
	if command.SegmentType == segmentTypeUnspecified {
		return nil, invalidSegment("segment type is required")
	}
	config, err := normalizeSegmentConfig(command.Config)
	if err != nil {
		return nil, invalidSegment(err.Error())
	}
	segment := model.AudienceSegment{Name: command.Name, Description: command.Description, SegmentType: command.SegmentType}
	if err := applySegmentConfig(&segment, config); err != nil {
		return nil, invalidSegment(err.Error())
	}
	if err := ValidateSegmentConfigForType(command.SegmentType, config); err != nil {
		return nil, invalidSegment(err.Error())
	}
	created, err := mutation.Run(ctx, s.mutations, mutation.Spec[model.AudienceSegment]{
		Access: freshAdmin(can),
		Change: func(_ context.Context, tx *gorm.DB) (model.AudienceSegment, error) {
			if err := tx.Omit("ID", "Config").Clauses(clause.Returning{}).Create(&segment).Error; err != nil {
				return segment, err
			}
			return segment, replaceSegmentRelations(tx, s.memberReferences, segment.ID, config)
		},
		Audit: func(segment model.AudienceSegment) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditAudienceSegmentCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAudienceSegmentCreatedAuditRecord(metadata, segment.ID)
			})
		},
		Relationships: func(segment model.AudienceSegment) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
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
		if dberrors.IsForeignKeyViolation(err) {
			return nil, invalidSegment("one or more audience references do not exist")
		}
		return nil, err
	}
	return toProtoSegment(&created), nil
}

// EstimateSegmentCountQuery estimates the recipients of an unsaved segment.
type EstimateSegmentCountQuery struct {
	SegmentType model.AudienceSegmentType
	Config      *SegmentConfigInput
}

// EstimateSegmentCount returns the recipients the segment would select now.
func (s *Service) EstimateSegmentCount(ctx context.Context, query EstimateSegmentCountQuery) (int32, error) {
	can, err := policyv1.AudienceSegment.Create()
	if err != nil {
		return 0, err
	}
	if err := requireAudienceAdminCan(ctx, s.spiceDB, can); err != nil {
		return 0, err
	}
	segment := &model.AudienceSegment{SegmentType: query.SegmentType}
	config, err := normalizeSegmentConfig(query.Config)
	if err != nil {
		return 0, invalidSegment(err.Error())
	}
	if err := ValidateSegmentConfigForType(query.SegmentType, config); err != nil {
		return 0, invalidSegment(err.Error())
	}
	if err := applySegmentConfig(segment, config); err != nil {
		return 0, invalidSegment(err.Error())
	}
	return s.estimateCount(ctx, segment)
}

// UpdateSegmentCommand changes a segment. A type or filter change needs the
// type and filter the editor observed, so a concurrent edit of another
// filter leaf survives.
type UpdateSegmentCommand struct {
	ID          string
	Name        *string
	Description *string
	SegmentType *model.AudienceSegmentType
	Config      *SegmentConfigInput
	Observed    *ObservedSegment
}

// ObservedSegment is the segment type and filter an editor read.
type ObservedSegment struct {
	SegmentType model.AudienceSegmentType
	// Config is required; nil means the editor sent no observed filter.
	Config *SegmentConfigInput
}

type segmentUpdatePlan struct {
	command         UpdateSegmentCommand
	requestedConfig *model.AudienceSegmentConfig
	observed        *segmentConfigBaseline
}

type segmentConfigBaseline struct {
	segmentType model.AudienceSegmentType
	config      model.AudienceSegmentConfig
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
		if err := ValidateSegmentConfigForType(command.Observed.SegmentType, observedConfig); err != nil {
			return plan, invalidSegment("observed config is invalid for its segment type")
		}
		plan.observed = &segmentConfigBaseline{segmentType: command.Observed.SegmentType, config: observedConfig}
	}
	if command.Config == nil {
		if plan.observed != nil && command.SegmentType != nil && *command.SegmentType != plan.observed.segmentType {
			// An intentional type switch resets the coupled filter config even if
			// the caller omitted config, so relation-backed filters are cleared too.
			plan.requestedConfig = &model.AudienceSegmentConfig{}
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
func (s *Service) UpdateSegment(ctx context.Context, command UpdateSegmentCommand) (*managev1.Segment, error) {
	can, err := policyv1.AudienceSegment.Edit(command.ID)
	if err != nil {
		return nil, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	plan, err := buildSegmentUpdatePlan(command)
	if err != nil {
		return nil, err
	}
	segment, err := s.applySegmentUpdate(ctx, command.ID, plan, can)
	if err != nil {
		return nil, err
	}
	return toProtoSegment(segment), nil
}

// segmentUpdate is an applied segment change and the fields it changed.
type segmentUpdate struct {
	segment       model.AudienceSegment
	changedFields []string
}

func (s *Service) applySegmentUpdate(
	ctx context.Context,
	segmentID string,
	plan segmentUpdatePlan,
	can policyv1.Can,
) (*model.AudienceSegment, error) {
	var segment model.AudienceSegment
	updated, err := mutation.Run(ctx, s.mutations, mutation.Spec[segmentUpdate]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return lockSegmentForUpdate(ctx, tx, segmentID, &segment)
		},
		Access: freshAdmin(can),
		Change: func(ctx context.Context, tx *gorm.DB) (segmentUpdate, error) {
			if err := LoadSegmentConfig(ctx, tx, &segment); err != nil {
				return segmentUpdate{}, err
			}
			if err := ensureSegmentMutableForActiveDelivery(tx, segment.ID); err != nil {
				return segmentUpdate{}, err
			}
			nextType, nextConfig, err := plan.nextState(segment)
			if err != nil {
				return segmentUpdate{}, err
			}
			if err := ensureAudienceAccessTypeCompatible(tx, segment, nextType); err != nil {
				return segmentUpdate{}, err
			}
			changedFields, err := applySegmentUpdates(tx, s.memberReferences, &segment, plan, nextType, nextConfig)
			if err != nil {
				return segmentUpdate{}, err
			}
			if err := loadSegmentForResponse(ctx, tx, segmentID, &segment); err != nil {
				return segmentUpdate{}, err
			}
			return segmentUpdate{segment: segment, changedFields: changedFields}, nil
		},
		Audit: func(update segmentUpdate) mutation.Audit {
			if len(update.changedFields) == 0 {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditAudienceSegmentUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAudienceSegmentConfigUpdatedAuditRecord(metadata, segmentID, update.changedFields)
			})
		},
	})
	if err != nil {
		return nil, err
	}
	return &updated.segment, nil
}

// invalidSegment rejects a segment request with message.
func invalidSegment(message string) error {
	return domainerr.New(domainerr.Invalid, "", message)
}

func lockSegmentForUpdate(ctx context.Context, tx *gorm.DB, segmentID string, segment *model.AudienceSegment) error {
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(segment, "id = ?", segmentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errSegmentNotFound()
		}
		return err
	}
	return nil
}

func (plan segmentUpdatePlan) nextState(segment model.AudienceSegment) (model.AudienceSegmentType, model.AudienceSegmentConfig, error) {
	nextType := segment.SegmentType
	nextConfig := segment.Config
	if plan.observed != nil {
		typeChangedFromBaseline := plan.command.SegmentType != nil &&
			*plan.command.SegmentType != plan.observed.segmentType
		if typeChangedFromBaseline {
			nextType = *plan.command.SegmentType
			if plan.requestedConfig != nil {
				nextConfig = *plan.requestedConfig
			} else {
				nextConfig = model.AudienceSegmentConfig{}
			}
		} else if plan.requestedConfig != nil {
			// The editor did not change type. Preserve a concurrent type switch and
			// merge each config leaf against the state the editor actually observed.
			nextConfig = mergeSegmentConfig(segment.Config, plan.observed.config, *plan.requestedConfig)
		}
	}
	if err := ValidateSegmentConfigForType(nextType, nextConfig); err != nil {
		return "", nextConfig, invalidSegment(err.Error())
	}
	return nextType, nextConfig, nil
}

func mergeSegmentConfig(current, observed, desired model.AudienceSegmentConfig) model.AudienceSegmentConfig {
	merged := model.AudienceSegmentConfig{
		MemberTagIDs:     mergeObservedStringSet(current.MemberTagIDs, observed.MemberTagIDs, desired.MemberTagIDs),
		AccountRoles:     mergeObservedStringSet(current.AccountRoles, observed.AccountRoles, desired.AccountRoles),
		ExcludeMemberIDs: mergeObservedStringSet(current.ExcludeMemberIDs, observed.ExcludeMemberIDs, desired.ExcludeMemberIDs),
		CreatedAfter:     mergeObservedTime(current.CreatedAfter, observed.CreatedAfter, desired.CreatedAfter),
		CreatedBefore:    mergeObservedTime(current.CreatedBefore, observed.CreatedBefore, desired.CreatedBefore),
	}
	sortSegmentConfig(&merged)
	return merged
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

func ensureAudienceAccessTypeCompatible(tx *gorm.DB, segment model.AudienceSegment, nextType model.AudienceSegmentType) error {
	if !authenticatedAccessSegmentType(segment.SegmentType) || authenticatedAccessSegmentType(nextType) {
		return nil
	}
	var attachmentReferenceCount int64
	if err := tx.Model(&model.ContentBlockAttachmentDownloadAudienceSegment{}).
		Where("audience_segment_id = ?", segment.ID).
		Count(&attachmentReferenceCount).Error; err != nil {
		return err
	}
	var trackReferenceCount int64
	if err := tx.Model(&model.TrackDownloadAudienceSegment{}).
		Where("audience_segment_id = ?", segment.ID).
		Count(&trackReferenceCount).Error; err != nil {
		return err
	}
	if attachmentReferenceCount == 0 && trackReferenceCount == 0 {
		return nil
	}
	return domainerr.Precondition("audience segment grants download access and cannot change to this type")
}

func applySegmentUpdates(
	tx *gorm.DB,
	memberReferences MemberReferences,
	segment *model.AudienceSegment,
	plan segmentUpdatePlan,
	nextType model.AudienceSegmentType,
	nextConfig model.AudienceSegmentConfig,
) ([]string, error) {
	updates, changedFields, configChanged := buildSegmentUpdates(segment, plan, nextType, nextConfig)
	if len(updates) > 0 {
		updates["updated_at"] = time.Now().UTC()
		if err := tx.Model(segment).Updates(updates).Error; err != nil {
			return nil, err
		}
	}
	if !configChanged {
		return changedFields, nil
	}
	if err := replaceSegmentRelations(tx, memberReferences, segment.ID, nextConfig); err != nil {
		if dberrors.IsForeignKeyViolation(err) {
			return nil, invalidSegment("one or more audience references do not exist")
		}
		return nil, err
	}
	return changedFields, nil
}

func buildSegmentUpdates(
	segment *model.AudienceSegment,
	plan segmentUpdatePlan,
	nextType model.AudienceSegmentType,
	nextConfig model.AudienceSegmentConfig,
) (structured.Fields, []string, bool) {
	updates := structured.Fields{}
	changedFields := make([]string, 0, 8)
	if plan.command.Name != nil && *plan.command.Name != segment.Name {
		updates["name"] = *plan.command.Name
		changedFields = append(changedFields, "name")
	}
	if plan.command.Description != nil && !equalOptionalString(*plan.command.Description, segment.Description) {
		updates["description"] = *plan.command.Description
		changedFields = append(changedFields, "description")
	}
	if plan.command.SegmentType != nil && nextType != segment.SegmentType {
		updates["segment_type"] = nextType
		changedFields = append(changedFields, "segment_type")
	}
	if plan.requestedConfig == nil {
		return updates, changedFields, false
	}
	configChanged := false
	if !equalTime(nextConfig.CreatedAfter, segment.Config.CreatedAfter) {
		updates["created_after"] = nextConfig.CreatedAfter
		changedFields = append(changedFields, "created_after")
		configChanged = true
	}
	if !equalTime(nextConfig.CreatedBefore, segment.Config.CreatedBefore) {
		updates["created_before"] = nextConfig.CreatedBefore
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
	return updates, changedFields, configChanged
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
