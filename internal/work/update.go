package work

import (
	"context"
	"reflect"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type workUpdatePlan struct {
	fields         structured.Fields
	normalizedSlug *string
	slugPresent    bool
}

func (s *WorkService) UpdateWork(
	ctx context.Context,
	req *connect.Request[managev1.UpdateWorkRequest],
) (*connect.Response[managev1.UpdateWorkResponse], error) {
	work, err := loadWorkForUpdate(ctx, s.db, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	plan, err := s.buildWorkUpdatePlan(ctx, work, req.Msg)
	if err != nil {
		return nil, err
	}
	changed, err := s.applyWorkUpdatePlan(ctx, &work, req.Msg, plan)
	if err != nil {
		return nil, mapWorkUpdateError(err)
	}
	if changed {
		publishWorkContentUpdated(ctx, s.asyncPublisher, buildManageWorkContentUpdatedEvent(req.Msg))
	}
	metadata, err := structpb.NewStruct(work.Metadata)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.UpdateWorkResponse{
		Id:         work.ID,
		Changed:    changed,
		Slug:       work.Slug,
		Type:       managev1.WorkType(managev1.WorkType_value[work.Type]),
		Metadata:   metadata,
		Featured:   work.Featured,
		Clients:    s.getWorkClients(ctx, work.ID),
		Year:       work.Year,
		Month:      work.Month,
		UntilYear:  work.UntilYear,
		UntilMonth: work.UntilMonth,
		IsPresent:  work.IsPresent,
		MapPlaceId: work.MapPlaceID,
		UpdatedAt:  timestamppb.New(work.UpdatedAt),
	}), nil
}

func loadWorkForUpdate(ctx context.Context, db *gorm.DB, workID string) (model.Work, error) {
	var work model.Work
	err := db.WithContext(ctx).First(&work, "id = ?", workID).Error
	if err == gorm.ErrRecordNotFound {
		return model.Work{}, errs.NotFound("work", workID)
	}
	if err != nil {
		return model.Work{}, errs.Internal(err)
	}
	return work, nil
}

func (s *WorkService) buildWorkUpdatePlan(
	ctx context.Context,
	work model.Work,
	request *managev1.UpdateWorkRequest,
) (workUpdatePlan, error) {
	slug, slugPresent := normalizeOptionalNullableString(request.Slug)
	if err := s.validateWorkUpdateSlug(ctx, slug); err != nil {
		return workUpdatePlan{}, err
	}
	plan := workUpdatePlan{
		fields:         structured.Fields{},
		normalizedSlug: slug,
		slugPresent:    slugPresent,
	}
	assignWorkUpdateScalarFields(plan.fields, request, slug, slugPresent)
	if err := assignWorkUpdateRange(plan.fields, work, request); err != nil {
		return workUpdatePlan{}, err
	}
	if err := s.assignWorkUpdateMapPlace(ctx, plan.fields, request.MapPlaceId); err != nil {
		return workUpdatePlan{}, err
	}
	return plan, nil
}

func (s *WorkService) validateWorkUpdateSlug(ctx context.Context, slug *string) error {
	if slug == nil {
		return nil
	}
	if err := validateSlugWithoutSlash(*slug); err != nil {
		return err
	}
	return routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "work", "works", *slug)
}

func assignWorkUpdateScalarFields(
	updates structured.Fields,
	request *managev1.UpdateWorkRequest,
	slug *string,
	slugPresent bool,
) {
	if slugPresent {
		if slug == nil {
			updates["slug"] = nil
		} else {
			updates["slug"] = *slug
		}
	}
	if request.Type != nil {
		updates["type"] = request.Type.String()
	}
	if request.Metadata != nil {
		updates["metadata"] = sanitizeWorkMetadata(request.Metadata.AsMap())
	}
	if request.Featured != nil {
		updates["featured"] = *request.Featured
	}
}

func assignWorkUpdateRange(
	updates structured.Fields,
	work model.Work,
	request *managev1.UpdateWorkRequest,
) error {
	if request.Year == nil && request.Month == nil && request.UntilYear == nil &&
		request.UntilMonth == nil && request.IsPresent == nil {
		return nil
	}
	year := fieldValueOr(request.Year, work.Year, request.Year != nil)
	month := fieldValueOr(request.Month, work.Month, request.Month != nil)
	untilYear := cloneInt32(work.UntilYear)
	if request.UntilYear != nil {
		untilYear = cloneInt32(request.UntilYear)
	}
	untilMonth := cloneInt32(work.UntilMonth)
	if request.UntilMonth != nil {
		untilMonth = cloneInt32(request.UntilMonth)
	}
	isPresent := fieldValueOr(request.IsPresent, work.IsPresent, request.IsPresent != nil)
	if isPresent {
		untilYear = nil
		untilMonth = nil
	}
	if err := validateWorkRange(year, month, untilYear, untilMonth, isPresent); err != nil {
		return err
	}
	updates["year"] = year
	updates["month"] = month
	updates["until_year"] = untilYear
	updates["until_month"] = untilMonth
	updates["is_present"] = isPresent
	return nil
}

func fieldValueOr[T any](value *T, fallback T, provided bool) T {
	if !provided || value == nil {
		return fallback
	}
	return *value
}

func cloneInt32(value *int32) *int32 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (s *WorkService) assignWorkUpdateMapPlace(
	ctx context.Context,
	updates structured.Fields,
	requested *string,
) error {
	if requested == nil {
		return nil
	}
	mapPlaceID, err := s.normalizeMapPlaceID(ctx, *requested)
	if err != nil {
		return err
	}
	if mapPlaceID == nil {
		updates["map_place_id"] = nil
	} else {
		updates["map_place_id"] = *mapPlaceID
	}
	return nil
}

func (s *WorkService) applyWorkUpdatePlan(
	ctx context.Context,
	work *model.Work,
	request *managev1.UpdateWorkRequest,
	plan workUpdatePlan,
) (bool, error) {
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := requireLockedWorkPermission(ctx, tx, s.spiceDB, work.ID, policyv1.Work.Manage, workAuthorizationMutation); err != nil {
			return err
		}
		var locked model.Work
		if err := tx.WithContext(ctx).Take(&locked, "id = ?", work.ID).Error; err != nil {
			return err
		}
		*work = locked
		if plan.slugPresent && plan.normalizedSlug != nil {
			if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "work", "works", *plan.normalizedSlug); err != nil {
				return err
			}
		}
		changedFields := workUpdateChangedFields(*work, plan.fields)
		clientsChanged, err := workClientSetChanged(ctx, tx, work.ID, request.Clients)
		if err != nil {
			return err
		}
		if len(changedFields) > 0 {
			mutationNow := time.Now()
			plan.fields["updated_at"] = mutationNow
			if err := tx.Model(work).Updates(plan.fields).Error; err != nil {
				return err
			}
			applyWorkUpdateFields(work, plan.fields)
			work.UpdatedAt = mutationNow
			changed = true
		}
		if clientsChanged {
			if err := replaceWorkClients(ctx, tx, work.ID, request.Clients.ClientIds); err != nil {
				return err
			}
			changedFields = append(changedFields, "clients")
			changed = true
		}
		if len(changedFields) > 0 {
			if err := s.appendWorkAudit(ctx, tx, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewWorkMetadataAuditRecord(metadata, work.ID, changedFields)
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return changed, err
}

func applyWorkUpdateFields(work *model.Work, fields structured.Fields) {
	if value, ok := fields["slug"]; ok {
		if value == nil {
			work.Slug = nil
		} else if slug, ok := value.(string); ok {
			work.Slug = &slug
		}
	}
	if value, ok := fields["type"].(string); ok {
		work.Type = value
	}
	if value, ok := fields["metadata"].(structured.Fields); ok {
		work.Metadata = value
	}
	if value, ok := fields["featured"].(bool); ok {
		work.Featured = value
	}
	if value, ok := fields["year"].(int32); ok {
		work.Year = value
	}
	if value, ok := fields["month"].(int32); ok {
		work.Month = value
	}
	if value, exists := fields["until_year"]; exists {
		work.UntilYear, _ = value.(*int32)
	}
	if value, exists := fields["until_month"]; exists {
		work.UntilMonth, _ = value.(*int32)
	}
	if value, ok := fields["is_present"].(bool); ok {
		work.IsPresent = value
	}
	if value, exists := fields["map_place_id"]; exists {
		if value == nil {
			work.MapPlaceID = nil
		} else if mapPlaceID, ok := value.(string); ok {
			work.MapPlaceID = &mapPlaceID
		}
	}
}

func workUpdateChangedFields(work model.Work, fields structured.Fields) []string {
	changed := []string{}
	if value, ok := fields["slug"]; ok {
		if value == nil {
			if work.Slug != nil {
				changed = append(changed, "slug")
			}
		} else if slug, ok := value.(string); !ok || work.Slug == nil || slug != *work.Slug {
			changed = append(changed, "slug")
		}
	}
	if value, ok := fields["type"].(string); ok && value != work.Type {
		changed = append(changed, "type")
	}
	if value, ok := fields["metadata"]; ok && !reflect.DeepEqual(value, work.Metadata) {
		changed = append(changed, "metadata")
	}
	if value, ok := fields["featured"].(bool); ok && value != work.Featured {
		changed = append(changed, "featured")
	}
	if value, ok := fields["year"].(int32); ok && value != work.Year {
		changed = append(changed, "year")
	}
	if value, ok := fields["month"].(int32); ok && value != work.Month {
		changed = append(changed, "month")
	}
	if value, ok := fields["until_year"]; ok && !reflect.DeepEqual(value, work.UntilYear) {
		changed = append(changed, "until_year")
	}
	if value, ok := fields["until_month"]; ok && !reflect.DeepEqual(value, work.UntilMonth) {
		changed = append(changed, "until_month")
	}
	if value, ok := fields["is_present"].(bool); ok && value != work.IsPresent {
		changed = append(changed, "is_present")
	}
	if value, ok := fields["map_place_id"]; ok && !reflect.DeepEqual(value, work.MapPlaceID) {
		changed = append(changed, "map_place_id")
	}
	return changed
}

func workClientSetChanged(ctx context.Context, tx *gorm.DB, workID string, clients *managev1.WorkClientsUpdate) (bool, error) {
	if clients == nil {
		return false, nil
	}
	var current []string
	if err := tx.WithContext(ctx).Table("work_client").Where("work_id = ?", workID).Order("sort_order").Pluck("client_id", &current).Error; err != nil {
		return false, err
	}
	return !reflect.DeepEqual(current, clients.ClientIds), nil
}

func replaceWorkClients(ctx context.Context, tx *gorm.DB, workID string, clientIDs []string) error {
	if err := validateWorkClientIDs(ctx, tx, clientIDs); err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec("DELETE FROM work_client WHERE work_id = ?", workID).Error; err != nil {
		return err
	}
	for sortOrder, clientID := range clientIDs {
		if err := tx.WithContext(ctx).Exec(
			"INSERT INTO work_client (work_id, client_id, sort_order, created_at) VALUES (?, ?, ?, NOW())",
			workID, clientID, sortOrder,
		).Error; err != nil {
			return err
		}
	}
	return nil
}

func validateWorkClientIDs(ctx context.Context, tx *gorm.DB, clientIDs []string) error {
	if len(clientIDs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(clientIDs))
	for _, clientID := range clientIDs {
		if strings.TrimSpace(clientID) == "" {
			return errs.InvalidArgument("clients", "contains empty client id")
		}
		if _, exists := seen[clientID]; exists {
			return errs.InvalidArgument("clients", "contains duplicate client id")
		}
		seen[clientID] = struct{}{}
	}
	var validCount int64
	if err := tx.WithContext(ctx).Table("client").Where("id IN ?", clientIDs).Count(&validCount).Error; err != nil {
		return err
	}
	if int(validCount) != len(clientIDs) {
		return errs.InvalidArgument("clients", "contains unknown client id")
	}
	return nil
}

func mapWorkUpdateError(err error) error {
	if connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	if strings.Contains(err.Error(), "duplicate key") {
		return errs.SlugAlreadyExists("work", "slug")
	}
	return errs.Internal(err)
}
