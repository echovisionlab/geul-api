package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// RelationsEdit merges the artists, labels or clients a caller wants into
// the ones it read. Observed is the list the caller read; relations added by
// someone else since are kept. An edit with no Desired entries applies only
// when Replace is set.
type RelationsEdit struct {
	Replace  bool
	Desired  []Relation
	Observed *[]Relation
}

func (e RelationsEdit) requested() bool {
	return e.Replace || len(e.Desired) > 0
}

// CreditsEdit replaces the whole credit list. Credits with an ID update that
// credit; an edit with no Desired entries applies only when Replace is set.
type CreditsEdit struct {
	Replace bool
	Desired []Credit
}

func (e CreditsEdit) requested() bool {
	return e.Replace || len(e.Desired) > 0
}

// UpdateProgramEventCommand changes event settings; nil and unset fields
// are left alone. An empty MapPlaceID, URL or SeriesID clears it, and an
// empty PosterFileID removes the poster.
type UpdateProgramEventCommand struct {
	ID               string
	Slug             *string
	TypeID           *string
	StartsAt         *time.Time
	EndsAt           *time.Time
	ClearEndsAt      bool
	Timezone         *string
	AllDay           *bool
	LocationMode     *model.ProgramEventLocationMode
	MapPlaceID       *string
	SeriesID         *string
	SeriesOrder      *int32
	ClearSeriesOrder bool
	PosterFileID     *string
	TicketURL        *string
	StreamURL        *string
	ExternalURL      *string
	Artists          RelationsEdit
	Labels           RelationsEdit
	Clients          RelationsEdit
	Credits          CreditsEdit
}

// EventUpdate is the outcome of UpdateProgramEvent.
type EventUpdate struct {
	ID        string
	Changed   bool
	UpdatedAt time.Time
}

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

// eventChange is a committed event settings update and the audit records
// it names, in the order they are appended.
type eventChange struct {
	update  EventUpdate
	records []mutation.Audit
}

// UpdateProgramEvent applies command to the locked event. The time,
// location and series checks are repeated against the locked row.
func (s *Service) UpdateProgramEvent(ctx context.Context, command UpdateProgramEventCommand) (EventUpdate, error) {
	event, err := s.loadProgramEventForUpdate(ctx, command.ID)
	if err != nil {
		return EventUpdate{}, err
	}
	update, err := s.prepareProgramEventUpdate(ctx, &event, command)
	if err != nil {
		return EventUpdate{}, err
	}
	var current model.ProgramEvent
	change, err := mutation.Run(ctx, s.mutations, mutation.Spec[eventChange]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&current, "id = ?", event.ID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return domainerr.ResourceNotFound("program event", event.ID)
				}
				return err
			}
			return nil
		},
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			return authorizeLockedProgramEvent(ctx, tx, s.spiceDB, current.ID, current.Status, "edit", policyv1.ProgramEvent.Edit)
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (eventChange, error) {
			return s.applyProgramEventUpdate(ctx, tx, current, command, update)
		},
		Audit: func(change eventChange) mutation.Audit {
			if len(change.records) == 0 {
				return mutation.Unchanged()
			}
			return mutation.All(change.records...)
		},
	})
	return change.update, err
}

func (s *Service) loadProgramEventForUpdate(ctx context.Context, eventID string) (model.ProgramEvent, error) {
	var event model.ProgramEvent
	if err := s.db.WithContext(ctx).First(&event, "id = ?", eventID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return event, domainerr.ResourceNotFound("program event", eventID)
		}
		return event, err
	}
	return event, nil
}

func (s *Service) prepareProgramEventUpdate(
	ctx context.Context,
	event *model.ProgramEvent,
	command UpdateProgramEventCommand,
) (programEventUpdate, error) {
	update := programEventUpdate{
		typeID: command.TypeID,
		allDay: command.AllDay,
		now:    dbtime.Now(),
	}
	if err := applyProgramEventTimeAndLocationUpdates(&update, event, command); err != nil {
		return programEventUpdate{}, err
	}
	if command.Timezone != nil {
		timezone := strings.TrimSpace(*command.Timezone)
		if timezone == "" {
			return programEventUpdate{}, domainerr.Required("timezone")
		}
		update.timezone = &timezone
	}
	applyProgramEventOptionalFields(&update, command)
	if command.Slug != nil {
		slug, err := validateProgramEventSlug(*command.Slug)
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
	command UpdateProgramEventCommand,
) error {
	update.startsAt, update.endsAt = nil, nullableColumn[time.Time]{}
	update.locationMode, update.mapPlaceID = nil, nullableColumn[string]{}
	if err := applyProgramEventTimeUpdate(update, event, command); err != nil {
		return err
	}
	return applyProgramEventLocationUpdate(update, event, command)
}

func applyProgramEventTimeUpdate(update *programEventUpdate, event *model.ProgramEvent, command UpdateProgramEventCommand) error {
	startsAt := event.StartsAt
	if command.StartsAt != nil {
		startsAt = *command.StartsAt
		update.startsAt = &startsAt
	}
	endsAt := event.EndsAt
	switch {
	case command.ClearEndsAt:
		endsAt = nil
		update.endsAt = setNullableColumn[time.Time](nil)
	case command.EndsAt != nil:
		endsAt = command.EndsAt
		update.endsAt = setNullableColumn(endsAt)
	}
	if command.StartsAt == nil && !command.ClearEndsAt && command.EndsAt == nil {
		return nil
	}
	return validateProgramEventTimeRange(startsAt, endsAt)
}

func applyProgramEventLocationUpdate(update *programEventUpdate, event *model.ProgramEvent, command UpdateProgramEventCommand) error {
	if command.LocationMode == nil && command.MapPlaceID == nil {
		return nil
	}
	locationMode := event.LocationMode
	if command.LocationMode != nil {
		locationMode = *command.LocationMode
	}
	mapPlaceID := event.MapPlaceID
	switch {
	case command.LocationMode != nil && !locationMode.UsesMapPlace():
		mapPlaceID = nil
	case command.MapPlaceID != nil:
		if locationMode.UsesMapPlace() {
			mapPlaceID = nullableString(command.MapPlaceID)
		} else {
			mapPlaceID = nil
		}
	}
	if err := validateProgramEventLocation(locationMode, mapPlaceID); err != nil {
		return err
	}
	if command.LocationMode != nil {
		update.locationMode = &locationMode
		if !locationMode.UsesMapPlace() {
			update.mapPlaceID = setNullableColumn[string](nil)
		}
	}
	if command.MapPlaceID != nil {
		update.mapPlaceID = setNullableColumn[string](nil)
		if locationMode.UsesMapPlace() {
			update.mapPlaceID = setNullableColumn(nullableString(command.MapPlaceID))
		}
	}
	return nil
}

func applyProgramEventOptionalFields(update *programEventUpdate, command UpdateProgramEventCommand) {
	if command.SeriesID != nil {
		update.seriesID = setNullableColumn(nullableString(command.SeriesID))
	}
	switch {
	case command.ClearSeriesOrder:
		update.seriesOrder = setNullableColumn[int32](nil)
	case command.SeriesOrder != nil:
		seriesOrder := *command.SeriesOrder
		update.seriesOrder = setNullableColumn(&seriesOrder)
	}
	if command.TicketURL != nil {
		update.ticketURL = setNullableColumn(nullableString(command.TicketURL))
	}
	if command.StreamURL != nil {
		update.streamURL = setNullableColumn(nullableString(command.StreamURL))
	}
	if command.ExternalURL != nil {
		update.externalURL = setNullableColumn(nullableString(command.ExternalURL))
	}
}

func (s *Service) applyProgramEventUpdate(
	ctx context.Context,
	tx *gorm.DB,
	current model.ProgramEvent,
	command UpdateProgramEventCommand,
	update programEventUpdate,
) (eventChange, error) {
	changedFields, err := writeProgramEventMetadata(ctx, tx, current, command, update)
	if err != nil {
		return eventChange{}, err
	}
	metadataChanged := len(changedFields) > 0
	posterAudit, err := s.applyProgramEventPosterChange(ctx, tx, current.ID, command)
	if err != nil {
		return eventChange{}, err
	}
	relationFields, creditReplacement, err := replaceProgramEventUpdateRelations(ctx, tx, current.ID, command)
	if err != nil {
		return eventChange{}, err
	}
	change := eventChange{update: EventUpdate{ID: current.ID, UpdatedAt: current.UpdatedAt}}
	if posterAudit != nil {
		change.records = append(change.records, *posterAudit)
	}
	change.records = append(change.records, creditReplacementAudits(current.ID, creditReplacement)...)
	changedFields = append(changedFields, relationFields...)
	creditsChanged := len(creditReplacement.changes) > 0 || creditReplacement.orderChanged
	if !metadataChanged && posterAudit == nil && len(relationFields) == 0 && !creditsChanged {
		return change, nil
	}
	if !metadataChanged {
		if err := tx.WithContext(ctx).Model(&model.ProgramEvent{}).Where("id = ?", current.ID).Update("updated_at", update.now).Error; err != nil {
			return eventChange{}, err
		}
	}
	change.update.Changed, change.update.UpdatedAt = true, update.now
	if len(changedFields) > 0 {
		change.records = append(change.records, eventAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventMetadataAuditRecord(metadata, current.ID, changedFields)
		}))
	}
	return change, nil
}

// writeProgramEventMetadata recomputes update against the locked event and
// writes the metadata columns that change. It returns the changed audit
// fields.
func writeProgramEventMetadata(
	ctx context.Context,
	tx *gorm.DB,
	current model.ProgramEvent,
	command UpdateProgramEventCommand,
	update programEventUpdate,
) ([]string, error) {
	if err := applyProgramEventTimeAndLocationUpdates(&update, &current, command); err != nil {
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
		if dberrors.IsUniqueViolation(err) && command.Slug != nil {
			return nil, domainerr.Duplicate("program event", "slug", *command.Slug).WithCause(err)
		}
		return nil, err
	}
	return changedFields, nil
}

// eventAudit is a Program Event update record attributed to the request.
func eventAudit(build func(sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error)) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditProgramEventUpdated, build)
}

// posterAudit records the primary poster an edit added or, when after is
// empty, removed.
func posterAudit(eventID, before, after string) *mutation.Audit {
	if before == after {
		return nil
	}
	fileID, operation := after, sharedtelemetry.AuditCollectionOperationAdded
	if after == "" {
		fileID, operation = before, sharedtelemetry.AuditCollectionOperationRemoved
	}
	audit := eventAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewProgramEventPosterAuditRecord(metadata, eventID, fileID, operation)
	})
	return &audit
}

// applyProgramEventPosterChange applies the requested poster and returns the
// record of the primary poster it added or removed.
func (s *Service) applyProgramEventPosterChange(
	ctx context.Context,
	tx *gorm.DB,
	eventID string,
	command UpdateProgramEventCommand,
) (*mutation.Audit, error) {
	posterBefore, err := loadProgramEventPrimaryMediaFileID(ctx, tx, eventID, "poster")
	if err != nil {
		return nil, err
	}
	if err := applyProgramEventPosterUpdate(ctx, tx, s.media, eventID, command.PosterFileID); err != nil {
		return nil, err
	}
	posterAfter, err := loadProgramEventPrimaryMediaFileID(ctx, tx, eventID, "poster")
	if err != nil {
		return nil, err
	}
	return posterAudit(eventID, posterBefore, posterAfter), nil
}

func creditReplacementAudits(eventID string, replacement programEventCreditReplacement) []mutation.Audit {
	records := make([]mutation.Audit, 0, len(replacement.changes)+1)
	for _, change := range replacement.changes {
		records = append(records, childAudit(eventID, "credits", change.id, change.operation))
	}
	if replacement.orderChanged {
		records = append(records, childOrderAudit(eventID, "credits", replacement.orderedIDs))
	}
	return records
}

func childAudit(eventID, kind, itemID string, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
	return eventAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewProgramEventChildAuditRecord(metadata, eventID, kind, itemID, operation)
	})
}

func childOrderAudit(eventID, kind string, itemIDs []string) mutation.Audit {
	return eventAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewProgramEventChildOrderAuditRecord(metadata, eventID, kind, itemIDs)
	})
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
		return domainerr.InvalidField("series_id", "series_id is required when series_order is set")
	}
	if seriesOrder == nil {
		return domainerr.InvalidField("series_order", "series_order is required when series_id is set")
	}
	if strings.TrimSpace(*seriesID) == "" {
		return domainerr.Required("series_id")
	}
	if *seriesOrder < 0 {
		return domainerr.InvalidField("series_order", "must be non-negative")
	}
	var series model.ProgramEventSeries
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id").First(&series, "id = ?", *seriesID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.InvalidField("series_id", "program event series does not exist")
		}
		return err
	}
	query := tx.Select("id").Where("series_id = ? AND series_order = ?", *seriesID, *seriesOrder)
	if strings.TrimSpace(eventID) != "" {
		query = query.Where("id <> ?", eventID)
	}
	var duplicate model.ProgramEvent
	err := query.Take(&duplicate).Error
	if err == nil {
		return domainerr.InvalidField("series_order", "must be unique within the program event series")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}

func applyProgramEventPosterUpdate(ctx context.Context, tx *gorm.DB, mediaAssets MediaAssets, eventID string, posterFileID *string) error {
	if posterFileID == nil {
		return nil
	}
	if strings.TrimSpace(*posterFileID) == "" {
		return deleteDefaultProgramEventPosterMedia(ctx, tx, mediaAssets, eventID)
	}
	return addProgramEventMedia(ctx, tx, mediaAssets, eventID, *posterFileID, "poster", nil, nil, true)
}

func replaceProgramEventUpdateRelations(ctx context.Context, tx *gorm.DB, eventID string, command UpdateProgramEventCommand) ([]string, programEventCreditReplacement, error) {
	creditReplacement := programEventCreditReplacement{}
	if err := validateProgramEventRelationBaselines(command); err != nil {
		return nil, creditReplacement, err
	}
	changed := make([]string, 0, 3)
	for _, relation := range []struct {
		field string
		edit  RelationsEdit
		apply func(context.Context, *gorm.DB, string, []Relation, []Relation) (bool, error)
	}{
		{"artists", command.Artists, mergeAndApplyProgramEventArtists},
		{"labels", command.Labels, mergeAndApplyProgramEventLabels},
		{"clients", command.Clients, mergeAndApplyProgramEventClients},
	} {
		if !relation.edit.requested() {
			continue
		}
		didChange, err := relation.apply(ctx, tx, eventID, *relation.edit.Observed, relation.edit.Desired)
		if err != nil {
			return nil, creditReplacement, err
		}
		if didChange {
			changed = append(changed, relation.field)
		}
	}
	if command.Credits.requested() {
		current, err := loadProgramEventCreditRows(ctx, tx, eventID)
		if err != nil {
			return nil, creditReplacement, err
		}
		if !sameProgramEventCredits(current, command.Credits.Desired) {
			creditReplacement, err = replaceProgramEventCredits(ctx, tx, eventID, command.Credits.Desired)
			if err != nil {
				return nil, creditReplacement, err
			}
		}
	}
	return changed, creditReplacement, nil
}

func validateProgramEventRelationBaselines(command UpdateProgramEventCommand) error {
	if command.Artists.requested() && command.Artists.Observed == nil {
		return domainerr.InvalidField("observed_artists", "is required when updating artists")
	}
	if command.Labels.requested() && command.Labels.Observed == nil {
		return domainerr.InvalidField("observed_labels", "is required when updating labels")
	}
	if command.Clients.requested() && command.Clients.Observed == nil {
		return domainerr.InvalidField("observed_clients", "is required when updating clients")
	}
	return nil
}
