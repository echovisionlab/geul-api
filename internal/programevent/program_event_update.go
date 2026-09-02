package programevent

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type programEventUpdate struct {
	fields structured.Fields
	now    time.Time
}

func (s *ProgramEventService) UpdateProgramEvent(
	ctx context.Context,
	req *connect.Request[managev1.UpdateProgramEventRequest],
) (*connect.Response[managev1.UpdateProgramEventResponse], error) {
	event, err := s.loadProgramEventForUpdate(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	update, err := s.prepareProgramEventUpdate(ctx, &event, req.Msg)
	if err != nil {
		return nil, err
	}
	changed, err := s.applyProgramEventUpdate(ctx, &event, req.Msg, update)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.UpdateProgramEventResponse{
		Id:        event.ID,
		Changed:   changed,
		UpdatedAt: timestamppb.New(event.UpdatedAt),
	}), nil
}

func (s *ProgramEventService) loadProgramEventForUpdate(ctx context.Context, eventID string) (model.ProgramEvent, error) {
	var event model.ProgramEvent
	if err := s.db.WithContext(ctx).First(&event, "id = ?", eventID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return event, errs.NotFound("program event", eventID)
		}
		return event, errs.Internal(err)
	}
	return event, nil
}

func (s *ProgramEventService) prepareProgramEventUpdate(
	ctx context.Context,
	event *model.ProgramEvent,
	r *managev1.UpdateProgramEventRequest,
) (programEventUpdate, error) {
	now := time.Now().UTC()
	update := programEventUpdate{
		fields: structured.Fields{},
		now:    now,
	}
	if r.TypeId != nil {
		update.fields["type_id"] = r.GetTypeId()
	}
	if err := applyProgramEventTimeUpdate(update.fields, event, r); err != nil {
		return programEventUpdate{}, err
	}
	if r.Timezone != nil {
		timezone := strings.TrimSpace(r.GetTimezone())
		if timezone == "" {
			return programEventUpdate{}, errs.Required("timezone")
		}
		update.fields["timezone"] = timezone
	}
	if r.AllDay != nil {
		update.fields["all_day"] = r.GetAllDay()
	}
	if err := applyProgramEventLocationUpdate(update.fields, event, r); err != nil {
		return programEventUpdate{}, err
	}
	applyProgramEventOptionalFields(update.fields, r)
	if r.Slug != nil {
		slug, err := validateProgramEventSlug(r.GetSlug())
		if err != nil {
			return programEventUpdate{}, err
		}
		if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "program event", "events", slug); err != nil {
			return programEventUpdate{}, err
		}
		update.fields["slug"] = slug
	}
	return update, nil
}

func applyProgramEventTimeUpdate(fields structured.Fields, event *model.ProgramEvent, r *managev1.UpdateProgramEventRequest) error {
	startsAt := event.StartsAt
	if r.StartsAt != nil {
		startsAt = r.StartsAt.AsTime()
		fields["starts_at"] = startsAt
	}
	endsAt := event.EndsAt
	switch {
	case r.ClearEndsAt:
		endsAt = nil
		fields["ends_at"] = nil
	case r.EndsAt != nil:
		endsAt = timestampPtr(r.EndsAt)
		fields["ends_at"] = endsAt
	}
	if r.StartsAt == nil && !r.ClearEndsAt && r.EndsAt == nil {
		return nil
	}
	return validateProgramEventTimeRange(startsAt, endsAt)
}

func applyProgramEventLocationUpdate(fields structured.Fields, event *model.ProgramEvent, r *managev1.UpdateProgramEventRequest) error {
	if r.LocationMode == nil && r.MapPlaceId == nil {
		return nil
	}
	locationMode := event.LocationMode
	if r.LocationMode != nil {
		locationMode = programEventLocationModeString(r.GetLocationMode())
	}
	mapPlaceID := event.MapPlaceID
	switch {
	case r.LocationMode != nil && !programEventLocationModeUsesMapPlace(locationMode):
		mapPlaceID = nil
	case r.MapPlaceId != nil:
		mapPlaceID = nullableString(r.MapPlaceId)
	}
	if err := validateProgramEventLocation(locationMode, mapPlaceID); err != nil {
		return err
	}
	if r.LocationMode != nil {
		fields["location_mode"] = locationMode
		if !programEventLocationModeUsesMapPlace(locationMode) {
			fields["map_place_id"] = nil
		}
	}
	if r.MapPlaceId != nil && (r.LocationMode == nil || programEventLocationModeUsesMapPlace(locationMode)) {
		fields["map_place_id"] = nullableString(r.MapPlaceId)
	}
	return nil
}

func applyProgramEventOptionalFields(fields structured.Fields, r *managev1.UpdateProgramEventRequest) {
	if r.SeriesId != nil {
		fields["series_id"] = nullableString(r.SeriesId)
	}
	switch {
	case r.ClearSeriesOrder:
		fields["series_order"] = nil
	case r.SeriesOrder != nil:
		fields["series_order"] = r.GetSeriesOrder()
	}
	for _, field := range []struct {
		column string
		value  *string
	}{
		{column: "ticket_url", value: r.TicketUrl},
		{column: "stream_url", value: r.StreamUrl},
		{column: "external_url", value: r.ExternalUrl},
	} {
		if field.value != nil {
			fields[field.column] = nullableString(field.value)
		}
	}
}

func (s *ProgramEventService) applyProgramEventUpdate(
	ctx context.Context,
	event *model.ProgramEvent,
	r *managev1.UpdateProgramEventRequest,
	update programEventUpdate,
) (bool, error) {
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.ProgramEvent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ?", event.ID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("program event", event.ID)
			}
			return errs.Internal(err)
		}
		*event = current
		if err := requireActiveProgramEventPrincipal(ctx, tx, "edit", false); err != nil {
			return err
		}
		if err := requireProgramEventPermission(
			ctx, s.spiceDB, current.ID,
			programEventMutationAction(current.Status, policyv1.ProgramEvent.Edit),
		); err != nil {
			return err
		}
		if err := validateProgramEventSeriesRelationForUpdate(ctx, tx, current, update.fields); err != nil {
			return err
		}
		if r.Slug != nil {
			if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "program event", "events", update.fields["slug"].(string)); err != nil {
				return err
			}
		}
		changedFields := programEventMetadataChangedFields(current, update.fields)
		metadataChanged := len(changedFields) > 0
		if metadataChanged {
			update.fields["updated_at"] = update.now
		}
		if metadataChanged {
			if err := tx.Model(&current).Updates(update.fields).Error; err != nil {
				if dberrors.IsUniqueViolation(err) && r.Slug != nil {
					return errs.SlugAlreadyExists("program event", r.GetSlug())
				}
				return errs.Internal(err)
			}
		}
		posterBefore, err := loadProgramEventPrimaryMediaFileID(ctx, tx, event.ID, "poster")
		if err != nil {
			return err
		}
		if err := applyProgramEventPosterUpdate(ctx, tx, s.runtime, event.ID, r); err != nil {
			return err
		}
		posterAfter, err := loadProgramEventPrimaryMediaFileID(ctx, tx, event.ID, "poster")
		if err != nil {
			return err
		}
		posterChanged := posterBefore != posterAfter
		if posterChanged {
			if posterAfter != "" {
				if err := s.appendProgramEventPosterAudit(ctx, tx, event.ID, posterAfter, sharedtelemetry.AuditCollectionOperationAdded); err != nil {
					return err
				}
			} else if posterBefore != "" {
				if err := s.appendProgramEventPosterAudit(ctx, tx, event.ID, posterBefore, sharedtelemetry.AuditCollectionOperationRemoved); err != nil {
					return err
				}
			}
		}
		relationFields, creditReplacement, err := replaceProgramEventUpdateRelations(ctx, tx, event.ID, r)
		if err != nil {
			return err
		}
		for _, change := range creditReplacement.changes {
			if err := s.appendProgramEventChildAudit(ctx, tx, event.ID, "credits", change.id, change.operation); err != nil {
				return err
			}
		}
		if creditReplacement.orderChanged {
			if err := s.appendProgramEventChildOrderAudit(ctx, tx, event.ID, "credits", creditReplacement.orderedIDs); err != nil {
				return err
			}
		}
		changedFields = append(changedFields, relationFields...)
		changed = metadataChanged || posterChanged || len(relationFields) > 0
		if !changed {
			return nil
		}
		if !metadataChanged {
			if err := tx.WithContext(ctx).Model(&model.ProgramEvent{}).Where("id = ?", event.ID).Update("updated_at", update.now).Error; err != nil {
				return errs.Internal(err)
			}
		}
		event.UpdatedAt = update.now
		if len(changedFields) == 0 {
			return nil
		}
		return appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventMetadataAuditRecord(metadata, event.ID, changedFields)
		})
	})
	return changed, err
}

func programEventMetadataChangedFields(event model.ProgramEvent, fields structured.Fields) []string {
	changed := make([]string, 0, len(fields))
	if value, ok := fields["type_id"].(string); ok && value != event.TypeID {
		changed = append(changed, "type")
	}
	if value, ok := nullableStringField(fields, "series_id"); ok && !sameNullableString(event.SeriesID, value) {
		changed = append(changed, "series")
	}
	if value, ok := nullableInt32Field(fields, "series_order"); ok && !sameNullableInt32(event.SeriesOrder, value) {
		changed = append(changed, "series_order")
	}
	if value, ok := fields["starts_at"].(time.Time); ok && !value.Equal(event.StartsAt) {
		changed = append(changed, "starts_at")
	}
	if value, ok := nullableTimeField(fields, "ends_at"); ok && !sameNullableTime(event.EndsAt, value) {
		changed = append(changed, "ends_at")
	}
	if value, ok := fields["timezone"].(string); ok && value != event.Timezone {
		changed = append(changed, "timezone")
	}
	if value, ok := fields["all_day"].(bool); ok && value != event.AllDay {
		changed = append(changed, "all_day")
	}
	if value, ok := fields["location_mode"].(string); ok && value != event.LocationMode {
		changed = append(changed, "location_mode")
	}
	if value, ok := nullableStringField(fields, "map_place_id"); ok && !sameNullableString(event.MapPlaceID, value) {
		changed = append(changed, "map_place_id")
	}
	if value, ok := fields["slug"].(string); ok && value != event.Slug {
		changed = append(changed, "slug")
	}
	if value, ok := nullableStringField(fields, "ticket_url"); ok && !sameNullableString(event.TicketURL, value) {
		changed = append(changed, "ticket_url")
	}
	if value, ok := nullableStringField(fields, "stream_url"); ok && !sameNullableString(event.StreamURL, value) {
		changed = append(changed, "stream_url")
	}
	if value, ok := nullableStringField(fields, "external_url"); ok && !sameNullableString(event.ExternalURL, value) {
		changed = append(changed, "external_url")
	}
	return changed
}

func nullableStringField(fields structured.Fields, name string) (*string, bool) {
	value, exists := fields[name]
	if !exists || value == nil {
		return nil, exists
	}
	stringValue, ok := value.(*string)
	return stringValue, ok
}

func nullableInt32Field(fields structured.Fields, name string) (*int32, bool) {
	value, exists := fields[name]
	if !exists || value == nil {
		return nil, exists
	}
	intValue, ok := value.(int32)
	if !ok {
		return nil, false
	}
	return &intValue, true
}

func nullableTimeField(fields structured.Fields, name string) (*time.Time, bool) {
	value, exists := fields[name]
	if !exists || value == nil {
		return nil, exists
	}
	timeValue, ok := value.(*time.Time)
	return timeValue, ok
}

func sameNullableTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func sameNullableInt32(left, right *int32) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validateProgramEventSeriesRelationForUpdate(ctx context.Context, tx *gorm.DB, current model.ProgramEvent, fields structured.Fields) error {
	seriesID := current.SeriesID
	if value, exists := nullableStringField(fields, "series_id"); exists {
		seriesID = value
	}
	seriesOrder := current.SeriesOrder
	if value, exists := nullableInt32Field(fields, "series_order"); exists {
		seriesOrder = value
	}
	return validateProgramEventSeriesRelation(ctx, tx, current.ID, seriesID, seriesOrder)
}

func validateProgramEventSeriesRelation(ctx context.Context, tx *gorm.DB, eventID string, seriesID *string, seriesOrder *int32) error {
	if seriesID == nil && seriesOrder == nil {
		return nil
	}
	if seriesID == nil {
		return errs.InvalidArgument("series_id", "series_id is required when series_order is set")
	}
	if seriesOrder == nil {
		return errs.InvalidArgument("series_order", "series_order is required when series_id is set")
	}
	if strings.TrimSpace(*seriesID) == "" {
		return errs.Required("series_id")
	}
	if *seriesOrder < 0 {
		return errs.InvalidArgument("series_order", "must be non-negative")
	}
	var series model.ProgramEventSeries
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&series, "id = ?", *seriesID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.InvalidArgument("series_id", "program event series does not exist")
		}
		return errs.Internal(err)
	}
	query := tx.Select("id").Where("series_id = ? AND series_order = ?", *seriesID, *seriesOrder)
	if strings.TrimSpace(eventID) != "" {
		query = query.Where("id <> ?", eventID)
	}
	var duplicate model.ProgramEvent
	err := query.Take(&duplicate).Error
	if err == nil {
		return errs.InvalidArgument("series_order", "must be unique within the program event series")
	}
	if err != gorm.ErrRecordNotFound {
		return errs.Internal(err)
	}
	return nil
}

func applyProgramEventPosterUpdate(ctx context.Context, tx *gorm.DB, mediaAssets MediaAssets, eventID string, r *managev1.UpdateProgramEventRequest) error {
	if r.PosterFileId == nil {
		return nil
	}
	if strings.TrimSpace(r.GetPosterFileId()) == "" {
		return deleteDefaultProgramEventPosterMedia(ctx, tx, mediaAssets, eventID)
	}
	return addProgramEventMedia(ctx, tx, mediaAssets, eventID, r.GetPosterFileId(), "poster", nil, nil, true)
}

func replaceProgramEventUpdateRelations(ctx context.Context, tx *gorm.DB, eventID string, r *managev1.UpdateProgramEventRequest) ([]string, programEventCreditReplacement, error) {
	changed := make([]string, 0, 3)
	creditReplacement := programEventCreditReplacement{}
	if r.ReplaceArtists || len(r.Artists) > 0 {
		current, err := loadProgramEventArtists(ctx, tx, eventID)
		if err != nil {
			return nil, creditReplacement, err
		}
		if !sameProgramEventArtists(current, r.Artists) {
			if err := replaceProgramEventArtists(ctx, tx, eventID, r.Artists); err != nil {
				return nil, creditReplacement, err
			}
			changed = append(changed, "artists")
		}
	}
	if r.ReplaceLabels || len(r.Labels) > 0 {
		current, err := loadProgramEventLabels(ctx, tx, eventID)
		if err != nil {
			return nil, creditReplacement, err
		}
		if !sameProgramEventLabels(current, r.Labels) {
			if err := replaceProgramEventLabels(ctx, tx, eventID, r.Labels); err != nil {
				return nil, creditReplacement, err
			}
			changed = append(changed, "labels")
		}
	}
	if r.ReplaceClients || len(r.Clients) > 0 {
		current, err := loadProgramEventClients(ctx, tx, eventID)
		if err != nil {
			return nil, creditReplacement, err
		}
		if !sameProgramEventClients(current, r.Clients) {
			if err := replaceProgramEventClients(ctx, tx, eventID, r.Clients); err != nil {
				return nil, creditReplacement, err
			}
			changed = append(changed, "clients")
		}
	}
	if r.ReplaceCredits || len(r.Credits) > 0 {
		current, err := loadProgramEventCreditRows(ctx, tx, eventID)
		if err != nil {
			return nil, creditReplacement, err
		}
		if !sameProgramEventCredits(current, r.Credits) {
			creditReplacement, err = replaceProgramEventCredits(ctx, tx, eventID, r.Credits)
			if err != nil {
				return nil, creditReplacement, err
			}
		}
	}
	return changed, creditReplacement, nil
}

func sameProgramEventArtists(left, right []*managev1.ProgramEventArtist) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].GetArtistId() != right[i].GetArtistId() || left[i].GetRole() != right[i].GetRole() || left[i].GetSortOrder() != right[i].GetSortOrder() {
			return false
		}
	}
	return true
}
func sameProgramEventLabels(left, right []*managev1.ProgramEventLabel) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].GetLabelId() != right[i].GetLabelId() || left[i].GetRole() != right[i].GetRole() || left[i].GetSortOrder() != right[i].GetSortOrder() {
			return false
		}
	}
	return true
}
func sameProgramEventClients(left, right []*managev1.ProgramEventClient) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].GetClientId() != right[i].GetClientId() || left[i].GetRole() != right[i].GetRole() || left[i].GetSortOrder() != right[i].GetSortOrder() {
			return false
		}
	}
	return true
}
