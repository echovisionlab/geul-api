package audience

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (s *AudienceService) CreateSegment(
	ctx context.Context,
	req *connect.Request[managev1.CreateSegmentRequest],
) (*connect.Response[managev1.Segment], error) {
	can, err := policyv1.AudienceSegment.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	if req.Msg.Name == "" {
		return nil, errs.Required("name")
	}
	if req.Msg.SegmentType == managev1.SegmentType_SEGMENT_TYPE_UNSPECIFIED {
		return nil, errs.InvalidArgumentMsg("segment type is required")
	}
	segmentType := req.Msg.SegmentType.String()
	config, err := toModelSegmentConfig(req.Msg.Config)
	if err != nil {
		return nil, errs.InvalidArgumentMsg(err.Error())
	}
	segment := model.AudienceSegment{Name: req.Msg.Name, Description: req.Msg.Description, SegmentType: segmentType}
	if err := applySegmentConfig(&segment, config); err != nil {
		return nil, errs.InvalidArgumentMsg(err.Error())
	}
	if err := ValidateSegmentConfigForType(segmentType, config); err != nil {
		return nil, errs.InvalidArgumentMsg(err.Error())
	}
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if err := tx.Omit("ID", "Config").Clauses(clause.Returning{}).Create(&segment).Error; err != nil {
			return err
		}
		if err := replaceSegmentRelations(tx, s.memberReferences, segment.ID, config); err != nil {
			return err
		}
		policyTouch, err := policyv1.AudienceSegment.TouchPolicy(segment.ID)
		if err != nil {
			return err
		}
		policyDelete, err := policyv1.AudienceSegment.DeletePolicy(segment.ID)
		if err != nil {
			return err
		}
		if err := write(
			[]policyv1.RelationshipMutation{policyTouch},
			[]policyv1.RelationshipMutation{policyDelete},
		); err != nil {
			return err
		}
		return domainaudit.AppendOptionalRequest(
			ctx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditAudienceSegmentCreated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAudienceSegmentCreatedAuditRecord(metadata, segment.ID)
			},
		)
	})
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, errs.InvalidArgumentMsg("one or more audience references do not exist")
		}
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(toProtoSegment(&segment)), nil
}

func (s *AudienceService) EstimateSegmentCount(
	ctx context.Context,
	req *connect.Request[managev1.EstimateSegmentCountRequest],
) (*connect.Response[managev1.EstimateSegmentCountResponse], error) {
	can, err := policyv1.AudienceSegment.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := requireAudienceAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}
	segmentType := req.Msg.SegmentType.String()
	if segmentType == "" {
		return nil, errs.InvalidArgumentMsg("invalid segment type")
	}
	segment := &model.AudienceSegment{SegmentType: segmentType}
	config, err := toModelSegmentConfig(req.Msg.Config)
	if err != nil {
		return nil, errs.InvalidArgumentMsg(err.Error())
	}
	if err := ValidateSegmentConfigForType(segmentType, config); err != nil {
		return nil, errs.InvalidArgumentMsg(err.Error())
	}
	if err := applySegmentConfig(segment, config); err != nil {
		return nil, errs.InvalidArgumentMsg(err.Error())
	}
	count, err := s.estimateCount(ctx, segment)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.EstimateSegmentCountResponse{Count: count}), nil
}

type segmentUpdatePlan struct {
	request         *managev1.UpdateSegmentRequest
	requestedConfig *model.AudienceSegmentConfig
	observed        *segmentConfigBaseline
}

type segmentConfigBaseline struct {
	segmentType string
	config      model.AudienceSegmentConfig
}

func buildSegmentUpdatePlan(request *managev1.UpdateSegmentRequest) (segmentUpdatePlan, error) {
	plan := segmentUpdatePlan{request: request}
	if request.SegmentType != nil && *request.SegmentType == managev1.SegmentType_SEGMENT_TYPE_UNSPECIFIED {
		return plan, errs.InvalidArgumentMsg("segment type is required")
	}
	if (request.SegmentType != nil || request.Config != nil) && request.Observed == nil {
		return plan, errs.InvalidArgument("observed", "segment type and config are required when updating segment type or config")
	}
	if request.Observed != nil {
		if request.Observed.SegmentType == managev1.SegmentType_SEGMENT_TYPE_UNSPECIFIED || request.Observed.Config == nil {
			return plan, errs.InvalidArgumentMsg("observed segment type and config are required")
		}
		observedConfig, err := toModelSegmentConfig(request.Observed.Config)
		if err != nil {
			return plan, errs.InvalidArgumentMsg("observed config is invalid")
		}
		observedType := request.Observed.SegmentType.String()
		if err := ValidateSegmentConfigForType(observedType, observedConfig); err != nil {
			return plan, errs.InvalidArgumentMsg("observed config is invalid for its segment type")
		}
		plan.observed = &segmentConfigBaseline{segmentType: observedType, config: observedConfig}
	}
	if request.Config == nil {
		if plan.observed != nil && request.SegmentType != nil &&
			(*request.SegmentType).String() != plan.observed.segmentType {
			// An intentional type switch resets the coupled filter config even if
			// the caller omitted config, so relation-backed filters are cleared too.
			plan.requestedConfig = &model.AudienceSegmentConfig{}
		}
		return plan, nil
	}
	config, err := toModelSegmentConfig(request.Config)
	if err != nil {
		return plan, errs.InvalidArgumentMsg(err.Error())
	}
	plan.requestedConfig = &config
	return plan, nil
}

func (s *AudienceService) UpdateSegment(
	ctx context.Context,
	req *connect.Request[managev1.UpdateSegmentRequest],
) (*connect.Response[managev1.Segment], error) {
	can, err := policyv1.AudienceSegment.Edit(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	plan, err := buildSegmentUpdatePlan(req.Msg)
	if err != nil {
		return nil, err
	}
	segment, err := s.applySegmentUpdate(ctx, req.Msg.Id, plan, can)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoSegment(segment)), nil
}

func (s *AudienceService) applySegmentUpdate(
	ctx context.Context,
	segmentID string,
	plan segmentUpdatePlan,
	can policyv1.Can,
) (*model.AudienceSegment, error) {
	var segment model.AudienceSegment
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockSegmentForUpdate(ctx, tx, segmentID, &segment); err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if err := LoadSegmentConfig(ctx, tx, &segment); err != nil {
			return errs.Internal(err)
		}
		if err := ensureSegmentMutableForActiveDelivery(tx, segment.ID); err != nil {
			return err
		}
		nextType, nextConfig, err := plan.nextState(segment)
		if err != nil {
			return err
		}
		if err := ensureAudienceAccessTypeCompatible(tx, segment, nextType); err != nil {
			return err
		}
		changedFields, err := applySegmentUpdates(tx, s.memberReferences, &segment, plan, nextType, nextConfig)
		if err != nil {
			return err
		}
		if len(changedFields) > 0 {
			if err := domainaudit.AppendOptionalRequest(
				ctx,
				tx,
				s.auditWriter,
				sharedtelemetry.AuditAudienceSegmentUpdated,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewAudienceSegmentConfigUpdatedAuditRecord(
						metadata,
						segment.ID,
						changedFields,
					)
				},
			); err != nil {
				return err
			}
		}
		return loadSegmentForResponse(ctx, tx, segmentID, &segment)
	})
	return &segment, err
}

func lockSegmentForUpdate(ctx context.Context, tx *gorm.DB, segmentID string, segment *model.AudienceSegment) error {
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(segment, "id = ?", segmentID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.NotFoundMsg("segment not found")
		}
		return errs.Internal(err)
	}
	return nil
}

func (plan segmentUpdatePlan) nextState(segment model.AudienceSegment) (string, model.AudienceSegmentConfig, error) {
	nextType := segment.SegmentType
	nextConfig := segment.Config
	if plan.observed != nil {
		typeChangedFromBaseline := plan.request.SegmentType != nil &&
			(*plan.request.SegmentType).String() != plan.observed.segmentType
		if typeChangedFromBaseline {
			nextType = (*plan.request.SegmentType).String()
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
		return "", nextConfig, errs.InvalidArgumentMsg(err.Error())
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

func ensureAudienceAccessTypeCompatible(tx *gorm.DB, segment model.AudienceSegment, nextType string) error {
	if !authenticatedAccessSegmentType(segment.SegmentType) || authenticatedAccessSegmentType(nextType) {
		return nil
	}
	var attachmentReferenceCount int64
	if err := tx.Model(&model.ContentBlockAttachmentDownloadAudienceSegment{}).
		Where("audience_segment_id = ?", segment.ID).
		Count(&attachmentReferenceCount).Error; err != nil {
		return errs.Internal(err)
	}
	var trackReferenceCount int64
	if err := tx.Model(&model.TrackDownloadAudienceSegment{}).
		Where("audience_segment_id = ?", segment.ID).
		Count(&trackReferenceCount).Error; err != nil {
		return errs.Internal(err)
	}
	if attachmentReferenceCount == 0 && trackReferenceCount == 0 {
		return nil
	}
	return errs.FailedPrecondition("audience segment grants download access and cannot change to this type")
}

func applySegmentUpdates(
	tx *gorm.DB,
	memberReferences MemberReferences,
	segment *model.AudienceSegment,
	plan segmentUpdatePlan,
	nextType string,
	nextConfig model.AudienceSegmentConfig,
) ([]string, error) {
	updates, changedFields, configChanged := buildSegmentUpdates(segment, plan, nextType, nextConfig)
	if len(updates) > 0 {
		updates["updated_at"] = time.Now().UTC()
		if err := tx.Model(segment).Updates(updates).Error; err != nil {
			return nil, errs.Internal(err)
		}
	}
	if !configChanged {
		return changedFields, nil
	}
	if err := replaceSegmentRelations(tx, memberReferences, segment.ID, nextConfig); err != nil {
		if isForeignKeyViolation(err) {
			return nil, errs.InvalidArgumentMsg("one or more audience references do not exist")
		}
		return nil, errs.Internal(err)
	}
	return changedFields, nil
}

func buildSegmentUpdates(
	segment *model.AudienceSegment,
	plan segmentUpdatePlan,
	nextType string,
	nextConfig model.AudienceSegmentConfig,
) (structured.Fields, []string, bool) {
	updates := structured.Fields{}
	changedFields := make([]string, 0, 8)
	if plan.request.Name != nil && *plan.request.Name != segment.Name {
		updates["name"] = *plan.request.Name
		changedFields = append(changedFields, "name")
	}
	if plan.request.Description != nil && !equalOptionalString(*plan.request.Description, segment.Description) {
		updates["description"] = *plan.request.Description
		changedFields = append(changedFields, "description")
	}
	if plan.request.SegmentType != nil && nextType != segment.SegmentType {
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

func isForeignKeyViolation(err error) bool {
	if errors.Is(err, gorm.ErrForeignKeyViolated) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return true
	}
	const sqliteConstraintForeignKey, sqliteConstraintTrigger = 787, 1811
	var sqliteErr interface {
		error
		Code() int
	}
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code() == sqliteConstraintForeignKey ||
		(sqliteErr.Code() == sqliteConstraintTrigger &&
			strings.Contains(strings.ToLower(sqliteErr.Error()), "foreign key constraint failed"))
}
