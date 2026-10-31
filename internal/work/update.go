package work

import (
	"context"
	"reflect"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// UpdateWorkCommand edits Work settings. A nil field is left alone.
// Metadata and Clients merge into the current values the changes from the
// observed baseline the caller read, which they then require.
type UpdateWorkCommand struct {
	ID               string
	Slug             *string
	Type             *string
	Metadata         structured.Fields
	ObservedMetadata structured.Fields
	Featured         *bool
	Clients          *[]string
	ObservedClients  *[]string
	Year             *int32
	Month            *int32
	UntilYear        *int32
	UntilMonth       *int32
	IsPresent        *bool
	MapPlaceID       *string
}

type workUpdatePlan struct {
	fields         structured.Fields
	normalizedSlug *string
	slugPresent    bool
}

// workUpdate is the outcome of an update.
type workUpdate struct {
	work          model.Work
	changedFields []string
	changed       bool
}

func (s *Service) UpdateWork(ctx context.Context, command UpdateWorkCommand) (*managev1.UpdateWorkResponse, error) {
	if err := requireStoredWork(ctx, s.db, command.ID); err != nil {
		return nil, err
	}
	plan, err := s.buildWorkUpdatePlan(ctx, command)
	if err != nil {
		return nil, err
	}
	update, err := mutation.Run(ctx, s.mutations, mutation.Spec[workUpdate]{
		Access: lockedWork(command.ID, policyv1.Work.Manage, nil),
		Change: func(ctx context.Context, tx *gorm.DB) (workUpdate, error) {
			return s.applyWorkUpdatePlan(ctx, tx, command, plan)
		},
		Audit: func(update workUpdate) mutation.Audit {
			if len(update.changedFields) == 0 {
				return mutation.Unchanged()
			}
			return workUpdatedAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewWorkMetadataAuditRecord(metadata, update.work.ID, update.changedFields)
			})
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, update workUpdate) error {
			if !update.changed {
				return nil
			}
			return contentsignal.PublishInTransaction(ctx, tx, s.signals, buildManageWorkContentUpdatedEvent(command))
		},
	})
	if err != nil {
		if !mutation.IsClassified(err) && dberrors.IsUniqueViolation(err) {
			return nil, domainerr.Duplicate("work", "slug", "slug").WithCause(err)
		}
		return nil, err
	}
	work := update.work
	metadata, err := structpb.NewStruct(work.Metadata)
	if err != nil {
		return nil, err
	}
	clients, err := s.getWorkClients(ctx, work.ID)
	if err != nil {
		return nil, err
	}
	return &managev1.UpdateWorkResponse{
		Id:         work.ID,
		Changed:    update.changed,
		Slug:       work.Slug,
		Type:       managev1.WorkType(managev1.WorkType_value[work.Type]),
		Metadata:   metadata,
		Featured:   work.Featured,
		Clients:    clients,
		Year:       work.Year,
		Month:      work.Month,
		UntilYear:  work.UntilYear,
		UntilMonth: work.UntilMonth,
		IsPresent:  work.IsPresent,
		MapPlaceId: work.MapPlaceID,
		UpdatedAt:  timestamppb.New(work.UpdatedAt),
	}, nil
}

func (s *Service) buildWorkUpdatePlan(ctx context.Context, command UpdateWorkCommand) (workUpdatePlan, error) {
	slug, slugPresent := normalizeOptionalNullableString(command.Slug)
	if err := s.validateWorkUpdateSlug(ctx, slug); err != nil {
		return workUpdatePlan{}, err
	}
	plan := workUpdatePlan{
		fields:         structured.Fields{},
		normalizedSlug: slug,
		slugPresent:    slugPresent,
	}
	assignWorkUpdateScalarFields(plan.fields, command, slug, slugPresent)
	if err := s.assignWorkUpdateMapPlace(ctx, plan.fields, command.MapPlaceID); err != nil {
		return workUpdatePlan{}, err
	}
	return plan, nil
}

func (s *Service) validateWorkUpdateSlug(ctx context.Context, slug *string) error {
	if slug == nil {
		return nil
	}
	if err := pathslug.Validate(*slug); err != nil {
		return err
	}
	return routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "work", "works", *slug)
}

func assignWorkUpdateScalarFields(
	updates structured.Fields,
	command UpdateWorkCommand,
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
	if command.Type != nil {
		updates["type"] = *command.Type
	}
	if command.Featured != nil {
		updates["featured"] = *command.Featured
	}
}

func assignWorkUpdateRange(
	work model.Work,
	request UpdateWorkCommand,
) (structured.Fields, error) {
	if request.Year == nil && request.Month == nil && request.UntilYear == nil &&
		request.UntilMonth == nil && request.IsPresent == nil {
		return nil, nil
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
		return nil, err
	}
	updates := structured.Fields{}
	if request.Year != nil {
		updates["year"] = year
	}
	if request.Month != nil {
		updates["month"] = month
	}
	if request.UntilYear != nil {
		updates["until_year"] = untilYear
	}
	if request.UntilMonth != nil {
		updates["until_month"] = untilMonth
	}
	if request.IsPresent != nil {
		updates["is_present"] = isPresent
		if isPresent {
			updates["until_year"] = (*int32)(nil)
			updates["until_month"] = (*int32)(nil)
		}
	}
	return updates, nil
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

func (s *Service) assignWorkUpdateMapPlace(
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

func (s *Service) applyWorkUpdatePlan(
	ctx context.Context,
	tx *gorm.DB,
	command UpdateWorkCommand,
	plan workUpdatePlan,
) (workUpdate, error) {
	var update workUpdate
	if err := tx.WithContext(ctx).Take(&update.work, "id = ?", command.ID).Error; err != nil {
		return update, err
	}
	work := &update.work
	if err := validateWorkUpdateObservedFields(command); err != nil {
		return update, err
	}
	rangeFields, err := assignWorkUpdateRange(*work, command)
	if err != nil {
		return update, err
	}
	for field, value := range rangeFields {
		plan.fields[field] = value
	}
	if command.Metadata != nil {
		plan.fields["metadata"] = mergeWorkMetadataIntent(
			sanitizeWorkMetadata(work.Metadata),
			sanitizeWorkMetadata(command.ObservedMetadata),
			sanitizeWorkMetadata(command.Metadata),
		)
	}
	if plan.slugPresent && plan.normalizedSlug != nil {
		if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "work", "works", *plan.normalizedSlug); err != nil {
			return update, err
		}
	}
	update.changedFields = workUpdateChangedFields(*work, plan.fields)
	clientsChanged, clientIDs, err := workClientSetChanged(ctx, tx, work.ID, command.Clients, command.ObservedClients)
	if err != nil {
		return update, err
	}
	if len(update.changedFields) > 0 {
		mutationNow := dbtime.Now()
		plan.fields["updated_at"] = mutationNow
		if err := tx.Model(work).Updates(plan.fields).Error; err != nil {
			return update, err
		}
		applyWorkUpdateFields(work, plan.fields)
		work.UpdatedAt = mutationNow
		update.changed = true
	}
	if clientsChanged {
		if err := replaceWorkClients(ctx, tx, work.ID, clientIDs); err != nil {
			return update, err
		}
		update.changedFields = append(update.changedFields, "clients")
		update.changed = true
	}
	return update, nil
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

func validateWorkUpdateObservedFields(command UpdateWorkCommand) error {
	if command.Metadata != nil && command.ObservedMetadata == nil {
		return domainerr.InvalidField("observed_metadata", "is required when updating metadata")
	}
	if command.Clients != nil && command.ObservedClients == nil {
		return domainerr.InvalidField("observed_clients", "is required when updating clients")
	}
	return nil
}

func workClientSetChanged(
	ctx context.Context,
	tx *gorm.DB,
	workID string,
	clients *[]string,
	observed *[]string,
) (bool, []string, error) {
	if clients == nil {
		return false, nil, nil
	}
	if observed == nil {
		return false, nil, domainerr.InvalidField("observed_clients", "is required when updating clients")
	}
	var current []string
	if err := tx.WithContext(ctx).Table("work_client").Where("work_id = ?", workID).Order("sort_order").Pluck("client_id", &current).Error; err != nil {
		return false, nil, err
	}
	next := mergeWorkClientIntent(current, *observed, *clients)
	return !reflect.DeepEqual(current, next), next, nil
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
			return domainerr.InvalidField("clients", "contains empty client id")
		}
		if _, exists := seen[clientID]; exists {
			return domainerr.InvalidField("clients", "contains duplicate client id")
		}
		seen[clientID] = struct{}{}
	}
	var validCount int64
	if err := tx.WithContext(ctx).Table("client").Where("id IN ?", clientIDs).Count(&validCount).Error; err != nil {
		return err
	}
	if int(validCount) != len(clientIDs) {
		return domainerr.InvalidField("clients", "contains unknown client id")
	}
	return nil
}
