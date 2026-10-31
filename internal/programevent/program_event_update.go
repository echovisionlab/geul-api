package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// programEventUpdate is a validated Program Event metadata update; nil and
// unset fields are left alone. Time and location fields depend on the row
// they apply to, so applyProgramEventTimeAndLocationUpdates recomputes them
// against the locked row.
type programEventUpdate struct {
	typeID       *string
	timezone     *string
	allDay       *bool
	slug         *string
	seriesID     nullableColumn[string]
	seriesOrder  nullableColumn[int32]
	ticketURL    nullableColumn[string]
	streamURL    nullableColumn[string]
	externalURL  nullableColumn[string]
	startsAt     *time.Time
	endsAt       nullableColumn[time.Time]
	locationMode *model.ProgramEventLocationMode
	mapPlaceID   nullableColumn[string]
	now          time.Time
}

// nullableColumn is an update to a nullable column. An unset column is left
// alone; a set one with a nil value is written as NULL.
type nullableColumn[T any] struct {
	set   bool
	value *T
}

func setNullableColumn[T any](value *T) nullableColumn[T] {
	return nullableColumn[T]{set: true, value: value}
}

func (c nullableColumn[T]) columnValue() any {
	if c.value == nil {
		return nil
	}
	return *c.value
}

// columns is the program_event row update for the fields the request sets.
func (u programEventUpdate) columns(updatedAt time.Time) structured.Fields {
	columns := structured.Fields{"updated_at": updatedAt}
	if u.typeID != nil {
		columns["type_id"] = *u.typeID
	}
	if u.startsAt != nil {
		columns["starts_at"] = *u.startsAt
	}
	if u.endsAt.set {
		columns["ends_at"] = u.endsAt.columnValue()
	}
	if u.timezone != nil {
		columns["timezone"] = *u.timezone
	}
	if u.allDay != nil {
		columns["all_day"] = *u.allDay
	}
	if u.locationMode != nil {
		columns["location_mode"] = *u.locationMode
	}
	if u.slug != nil {
		columns["slug"] = *u.slug
	}
	for column, update := range map[string]nullableColumn[string]{
		"map_place_id": u.mapPlaceID,
		"series_id":    u.seriesID,
		"ticket_url":   u.ticketURL,
		"stream_url":   u.streamURL,
		"external_url": u.externalURL,
	} {
		if update.set {
			columns[column] = update.columnValue()
		}
	}
	if u.seriesOrder.set {
		columns["series_order"] = u.seriesOrder.columnValue()
	}
	return columns
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
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
	update := programEventUpdate{
		typeID: r.TypeId,
		allDay: r.AllDay,
		now:    dbtime.Now(),
	}
	if err := applyProgramEventTimeAndLocationUpdates(&update, event, r); err != nil {
		return programEventUpdate{}, err
	}
	if r.Timezone != nil {
		timezone := strings.TrimSpace(r.GetTimezone())
		if timezone == "" {
			return programEventUpdate{}, errs.Required("timezone")
		}
		update.timezone = &timezone
	}
	applyProgramEventOptionalFields(&update, r)
	if r.Slug != nil {
		slug, err := validateProgramEventSlug(r.GetSlug())
		if err != nil {
			return programEventUpdate{}, err
		}
		if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "program event", "events", slug); err != nil {
			return programEventUpdate{}, err
		}
		update.slug = &slug
	}
	return update, nil
}

func applyProgramEventTimeAndLocationUpdates(
	update *programEventUpdate,
	event *model.ProgramEvent,
	r *managev1.UpdateProgramEventRequest,
) error {
	update.startsAt, update.endsAt = nil, nullableColumn[time.Time]{}
	update.locationMode, update.mapPlaceID = nil, nullableColumn[string]{}
	if err := applyProgramEventTimeUpdate(update, event, r); err != nil {
		return err
	}
	return applyProgramEventLocationUpdate(update, event, r)
}

func applyProgramEventTimeUpdate(update *programEventUpdate, event *model.ProgramEvent, r *managev1.UpdateProgramEventRequest) error {
	startsAt := event.StartsAt
	if r.StartsAt != nil {
		startsAt = r.StartsAt.AsTime()
		update.startsAt = &startsAt
	}
	endsAt := event.EndsAt
	switch {
	case r.ClearEndsAt:
		endsAt = nil
		update.endsAt = setNullableColumn[time.Time](nil)
	case r.EndsAt != nil:
		endsAt = timestampPtr(r.EndsAt)
		update.endsAt = setNullableColumn(endsAt)
	}
	if r.StartsAt == nil && !r.ClearEndsAt && r.EndsAt == nil {
		return nil
	}
	return validateProgramEventTimeRange(startsAt, endsAt)
}

func applyProgramEventLocationUpdate(update *programEventUpdate, event *model.ProgramEvent, r *managev1.UpdateProgramEventRequest) error {
	if r.LocationMode == nil && r.MapPlaceId == nil {
		return nil
	}
	locationMode := event.LocationMode
	if r.LocationMode != nil {
		locationMode = locationModeFromProto(r.GetLocationMode())
	}
	mapPlaceID := event.MapPlaceID
	switch {
	case r.LocationMode != nil && !locationMode.UsesMapPlace():
		mapPlaceID = nil
	case r.MapPlaceId != nil:
		if locationMode.UsesMapPlace() {
			mapPlaceID = nullableString(r.MapPlaceId)
		} else {
			mapPlaceID = nil
		}
	}
	if err := validateProgramEventLocation(locationMode, mapPlaceID); err != nil {
		return err
	}
	if r.LocationMode != nil {
		update.locationMode = &locationMode
		if !locationMode.UsesMapPlace() {
			update.mapPlaceID = setNullableColumn[string](nil)
		}
	}
	if r.MapPlaceId != nil {
		update.mapPlaceID = setNullableColumn[string](nil)
		if locationMode.UsesMapPlace() {
			update.mapPlaceID = setNullableColumn(nullableString(r.MapPlaceId))
		}
	}
	return nil
}

func applyProgramEventOptionalFields(update *programEventUpdate, r *managev1.UpdateProgramEventRequest) {
	if r.SeriesId != nil {
		update.seriesID = setNullableColumn(nullableString(r.SeriesId))
	}
	switch {
	case r.ClearSeriesOrder:
		update.seriesOrder = setNullableColumn[int32](nil)
	case r.SeriesOrder != nil:
		seriesOrder := r.GetSeriesOrder()
		update.seriesOrder = setNullableColumn(&seriesOrder)
	}
	if r.TicketUrl != nil {
		update.ticketURL = setNullableColumn(nullableString(r.TicketUrl))
	}
	if r.StreamUrl != nil {
		update.streamURL = setNullableColumn(nullableString(r.StreamUrl))
	}
	if r.ExternalUrl != nil {
		update.externalURL = setNullableColumn(nullableString(r.ExternalUrl))
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
		changedFields, err := s.writeProgramEventMetadata(ctx, tx, event, r, update)
		if err != nil {
			return err
		}
		metadataChanged := len(changedFields) > 0
		posterChanged, err := s.applyProgramEventPosterChange(ctx, tx, event.ID, r)
		if err != nil {
			return err
		}
		relationFields, creditReplacement, err := replaceProgramEventUpdateRelations(ctx, tx, event.ID, r)
		if err != nil {
			return err
		}
		if err := s.appendProgramEventCreditReplacementAudit(ctx, tx, event.ID, creditReplacement); err != nil {
			return err
		}
		changedFields = append(changedFields, relationFields...)
		creditsChanged := len(creditReplacement.changes) > 0 || creditReplacement.orderChanged
		changed = metadataChanged || posterChanged || len(relationFields) > 0 || creditsChanged
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

// writeProgramEventMetadata locks the event into *event, recomputes update
// against it and writes the metadata columns that change. It returns the
// changed audit fields.
func (s *ProgramEventService) writeProgramEventMetadata(
	ctx context.Context,
	tx *gorm.DB,
	event *model.ProgramEvent,
	r *managev1.UpdateProgramEventRequest,
	update programEventUpdate,
) ([]string, error) {
	var current model.ProgramEvent
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&current, "id = ?", event.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("program event", event.ID)
		}
		return nil, errs.Internal(err)
	}
	*event = current
	if err := requireActiveProgramEventPrincipal(ctx, tx, "edit", false); err != nil {
		return nil, err
	}
	if err := requireProgramEventPermission(
		ctx, s.spiceDB, current.ID,
		programEventMutationAction(current.Status, policyv1.ProgramEvent.Edit),
	); err != nil {
		return nil, err
	}
	if err := applyProgramEventTimeAndLocationUpdates(&update, &current, r); err != nil {
		return nil, err
	}
	if err := validateProgramEventSeriesRelationForUpdate(tx, current, update); err != nil {
		return nil, err
	}
	if update.slug != nil {
		if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "program event", "events", *update.slug); err != nil {
			return nil, err
		}
	}
	changedFields := programEventMetadataChangedFields(current, update)
	if len(changedFields) == 0 {
		return nil, nil
	}
	if err := tx.Model(&current).Updates(update.columns(update.now)).Error; err != nil {
		if dberrors.IsUniqueViolation(err) && r.Slug != nil {
			return nil, errs.WithCause(errs.SlugAlreadyExists("program event", r.GetSlug()), err)
		}
		return nil, errs.Internal(err)
	}
	return changedFields, nil
}

// applyProgramEventPosterChange applies the requested poster and audits the
// primary poster it added or removed. It reports whether the poster changed.
func (s *ProgramEventService) applyProgramEventPosterChange(
	ctx context.Context,
	tx *gorm.DB,
	eventID string,
	r *managev1.UpdateProgramEventRequest,
) (bool, error) {
	posterBefore, err := loadProgramEventPrimaryMediaFileID(ctx, tx, eventID, "poster")
	if err != nil {
		return false, err
	}
	if err := applyProgramEventPosterUpdate(ctx, tx, s.runtime, eventID, r); err != nil {
		return false, err
	}
	posterAfter, err := loadProgramEventPrimaryMediaFileID(ctx, tx, eventID, "poster")
	if err != nil {
		return false, err
	}
	switch {
	case posterBefore == posterAfter:
		return false, nil
	case posterAfter != "":
		return true, s.appendProgramEventPosterAudit(ctx, tx, eventID, posterAfter, sharedtelemetry.AuditCollectionOperationAdded)
	default:
		return true, s.appendProgramEventPosterAudit(ctx, tx, eventID, posterBefore, sharedtelemetry.AuditCollectionOperationRemoved)
	}
}

func (s *ProgramEventService) appendProgramEventCreditReplacementAudit(
	ctx context.Context,
	tx *gorm.DB,
	eventID string,
	replacement programEventCreditReplacement,
) error {
	for _, change := range replacement.changes {
		if err := s.appendProgramEventChildAudit(ctx, tx, eventID, "credits", change.id, change.operation); err != nil {
			return err
		}
	}
	if !replacement.orderChanged {
		return nil
	}
	return s.appendProgramEventChildOrderAudit(ctx, tx, eventID, "credits", replacement.orderedIDs)
}

func programEventMetadataChangedFields(event model.ProgramEvent, update programEventUpdate) []string {
	changed := make([]string, 0, 4)
	if update.typeID != nil && *update.typeID != event.TypeID {
		changed = append(changed, "type")
	}
	if update.seriesID.set && !sameNullableString(event.SeriesID, update.seriesID.value) {
		changed = append(changed, "series")
	}
	if update.seriesOrder.set && !sameNullableInt32(event.SeriesOrder, update.seriesOrder.value) {
		changed = append(changed, "series_order")
	}
	if update.startsAt != nil && !update.startsAt.Equal(event.StartsAt) {
		changed = append(changed, "starts_at")
	}
	if update.endsAt.set && !sameNullableTime(event.EndsAt, update.endsAt.value) {
		changed = append(changed, "ends_at")
	}
	if update.timezone != nil && *update.timezone != event.Timezone {
		changed = append(changed, "timezone")
	}
	if update.allDay != nil && *update.allDay != event.AllDay {
		changed = append(changed, "all_day")
	}
	if update.locationMode != nil && *update.locationMode != event.LocationMode {
		changed = append(changed, "location_mode")
	}
	if update.mapPlaceID.set && !sameNullableString(event.MapPlaceID, update.mapPlaceID.value) {
		changed = append(changed, "map_place_id")
	}
	if update.slug != nil && *update.slug != event.Slug {
		changed = append(changed, "slug")
	}
	if update.ticketURL.set && !sameNullableString(event.TicketURL, update.ticketURL.value) {
		changed = append(changed, "ticket_url")
	}
	if update.streamURL.set && !sameNullableString(event.StreamURL, update.streamURL.value) {
		changed = append(changed, "stream_url")
	}
	if update.externalURL.set && !sameNullableString(event.ExternalURL, update.externalURL.value) {
		changed = append(changed, "external_url")
	}
	return changed
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

func validateProgramEventSeriesRelationForUpdate(tx *gorm.DB, current model.ProgramEvent, update programEventUpdate) error {
	seriesID := current.SeriesID
	if update.seriesID.set {
		seriesID = update.seriesID.value
	}
	seriesOrder := current.SeriesOrder
	if update.seriesOrder.set {
		seriesOrder = update.seriesOrder.value
	}
	return validateProgramEventSeriesRelation(tx, current.ID, seriesID, seriesOrder)
}

func validateProgramEventSeriesRelation(tx *gorm.DB, eventID string, seriesID *string, seriesOrder *int32) error {
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
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id").First(&series, "id = ?", *seriesID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
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
	if !errors.Is(err, gorm.ErrRecordNotFound) {
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
	if err := validateProgramEventRelationBaselines(r); err != nil {
		return nil, creditReplacement, err
	}
	if r.ReplaceArtists || len(r.Artists) > 0 {
		didChange, err := mergeAndApplyProgramEventArtists(ctx, tx, eventID, r.ObservedArtists.Artists, r.Artists)
		if err != nil {
			return nil, creditReplacement, err
		}
		if didChange {
			changed = append(changed, "artists")
		}
	}
	if r.ReplaceLabels || len(r.Labels) > 0 {
		didChange, err := mergeAndApplyProgramEventLabels(ctx, tx, eventID, r.ObservedLabels.Labels, r.Labels)
		if err != nil {
			return nil, creditReplacement, err
		}
		if didChange {
			changed = append(changed, "labels")
		}
	}
	if r.ReplaceClients || len(r.Clients) > 0 {
		didChange, err := mergeAndApplyProgramEventClients(ctx, tx, eventID, r.ObservedClients.Clients, r.Clients)
		if err != nil {
			return nil, creditReplacement, err
		}
		if didChange {
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

func validateProgramEventRelationBaselines(r *managev1.UpdateProgramEventRequest) error {
	if (r.ReplaceArtists || len(r.Artists) > 0) && r.ObservedArtists == nil {
		return errs.InvalidArgument("observed_artists", "is required when updating artists")
	}
	if (r.ReplaceLabels || len(r.Labels) > 0) && r.ObservedLabels == nil {
		return errs.InvalidArgument("observed_labels", "is required when updating labels")
	}
	if (r.ReplaceClients || len(r.Clients) > 0) && r.ObservedClients == nil {
		return errs.InvalidArgument("observed_clients", "is required when updating clients")
	}
	return nil
}
