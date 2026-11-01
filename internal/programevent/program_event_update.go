package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
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
	LocationMode     *LocationMode
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
	SettingsUpdate
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
	var current Event
	change, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[eventChange, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			current, err = store.Event(ctx, event.ID, UpdateLock)
			return notFoundAs(err, domainerr.ResourceNotFound("program event", event.ID))
		},
		Access: mutation.Fenced(func(ctx context.Context, _ Store, lockActive func(*auth.UserInfo) (bool, error)) error {
			return authorizeLockedProgramEvent(ctx, lockActive, s.spiceDB, current.ID, current.Status, "edit", policyv1.ProgramEvent.Edit)
		}),
		Change: func(ctx context.Context, store Store) (eventChange, error) {
			return s.applyProgramEventUpdate(ctx, store, current, command, update)
		},
		Audit: func(change eventChange) mutation.Audit {
			return mutation.Audits(change.records...)
		},
	})
	return change.update, err
}

func (s *Service) loadProgramEventForUpdate(ctx context.Context, eventID string) (Event, error) {
	var event Event
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		event, err = store.Event(ctx, eventID, NoLock)
		return err
	})
	return event, notFoundAs(err, domainerr.ResourceNotFound("program event", eventID))
}

func (s *Service) prepareProgramEventUpdate(
	ctx context.Context,
	event *Event,
	command UpdateProgramEventCommand,
) (programEventUpdate, error) {
	update := programEventUpdate{SettingsUpdate{
		TypeID:    command.TypeID,
		AllDay:    command.AllDay,
		UpdatedAt: dbtime.Now(),
	}}
	if err := applyProgramEventTimeAndLocationUpdates(&update, event, command); err != nil {
		return programEventUpdate{}, err
	}
	if command.Timezone != nil {
		timezone := strings.TrimSpace(*command.Timezone)
		if timezone == "" {
			return programEventUpdate{}, domainerr.Required("timezone")
		}
		update.Timezone = &timezone
	}
	applyProgramEventOptionalFields(&update, command)
	if command.Slug != nil {
		slug, err := validateProgramEventSlug(*command.Slug)
		if err != nil {
			return programEventUpdate{}, err
		}
		if err := s.checkRoute(ctx, EventRoute, slug); err != nil {
			return programEventUpdate{}, err
		}
		update.Slug = &slug
	}
	return update, nil
}

func applyProgramEventTimeAndLocationUpdates(
	update *programEventUpdate,
	event *Event,
	command UpdateProgramEventCommand,
) error {
	update.StartsAt, update.EndsAt = nil, Nullable[time.Time]{}
	update.LocationMode, update.MapPlaceID = nil, Nullable[string]{}
	if err := applyProgramEventTimeUpdate(update, event, command); err != nil {
		return err
	}
	return applyProgramEventLocationUpdate(update, event, command)
}

func applyProgramEventTimeUpdate(update *programEventUpdate, event *Event, command UpdateProgramEventCommand) error {
	startsAt := event.StartsAt
	if command.StartsAt != nil {
		startsAt = *command.StartsAt
		update.StartsAt = &startsAt
	}
	endsAt := event.EndsAt
	switch {
	case command.ClearEndsAt:
		endsAt = nil
		update.EndsAt = setNullable[time.Time](nil)
	case command.EndsAt != nil:
		endsAt = command.EndsAt
		update.EndsAt = setNullable(endsAt)
	}
	if command.StartsAt == nil && !command.ClearEndsAt && command.EndsAt == nil {
		return nil
	}
	return validateProgramEventTimeRange(startsAt, endsAt)
}

func applyProgramEventLocationUpdate(update *programEventUpdate, event *Event, command UpdateProgramEventCommand) error {
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
		update.LocationMode = &locationMode
		if !locationMode.UsesMapPlace() {
			update.MapPlaceID = setNullable[string](nil)
		}
	}
	if command.MapPlaceID != nil {
		update.MapPlaceID = setNullable[string](nil)
		if locationMode.UsesMapPlace() {
			update.MapPlaceID = setNullable(nullableString(command.MapPlaceID))
		}
	}
	return nil
}

func applyProgramEventOptionalFields(update *programEventUpdate, command UpdateProgramEventCommand) {
	if command.SeriesID != nil {
		update.SeriesID = setNullable(nullableString(command.SeriesID))
	}
	switch {
	case command.ClearSeriesOrder:
		update.SeriesOrder = setNullable[int32](nil)
	case command.SeriesOrder != nil:
		seriesOrder := *command.SeriesOrder
		update.SeriesOrder = setNullable(&seriesOrder)
	}
	if command.TicketURL != nil {
		update.TicketURL = setNullable(nullableString(command.TicketURL))
	}
	if command.StreamURL != nil {
		update.StreamURL = setNullable(nullableString(command.StreamURL))
	}
	if command.ExternalURL != nil {
		update.ExternalURL = setNullable(nullableString(command.ExternalURL))
	}
}

func (s *Service) applyProgramEventUpdate(
	ctx context.Context,
	store Store,
	current Event,
	command UpdateProgramEventCommand,
	update programEventUpdate,
) (eventChange, error) {
	changedFields, err := writeProgramEventMetadata(ctx, store, current, command, update)
	if err != nil {
		return eventChange{}, err
	}
	metadataChanged := len(changedFields) > 0
	posterAudit, err := applyProgramEventPosterChange(ctx, store, current.ID, command)
	if err != nil {
		return eventChange{}, err
	}
	relationFields, creditReplacement, err := replaceProgramEventUpdateRelations(ctx, store, current.ID, command)
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
		if err := store.TouchEvent(ctx, current.ID, update.UpdatedAt); err != nil {
			return eventChange{}, err
		}
	}
	change.update.Changed, change.update.UpdatedAt = true, update.UpdatedAt
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
	store Store,
	current Event,
	command UpdateProgramEventCommand,
	update programEventUpdate,
) ([]string, error) {
	if err := applyProgramEventTimeAndLocationUpdates(&update, &current, command); err != nil {
		return nil, err
	}
	if err := validateProgramEventSeriesRelationForUpdate(ctx, store, current, update); err != nil {
		return nil, err
	}
	if update.Slug != nil {
		if err := store.LockRoute(ctx, EventRoute, *update.Slug); err != nil {
			return nil, err
		}
	}
	changedFields := programEventMetadataChangedFields(current, update)
	if len(changedFields) == 0 {
		return nil, nil
	}
	if err := store.UpdateSettings(ctx, current.ID, update.SettingsUpdate); err != nil {
		if errors.Is(err, ErrSlugTaken) && command.Slug != nil {
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
func applyProgramEventPosterChange(
	ctx context.Context,
	store Store,
	eventID string,
	command UpdateProgramEventCommand,
) (*mutation.Audit, error) {
	posterBefore, err := store.PrimaryMediaFileID(ctx, eventID, "poster")
	if err != nil {
		return nil, err
	}
	if err := applyProgramEventPosterUpdate(ctx, store, eventID, command.PosterFileID); err != nil {
		return nil, err
	}
	posterAfter, err := store.PrimaryMediaFileID(ctx, eventID, "poster")
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

func programEventMetadataChangedFields(event Event, update programEventUpdate) []string {
	changed := make([]string, 0, 4)
	if update.TypeID != nil && *update.TypeID != event.TypeID {
		changed = append(changed, "type")
	}
	if update.SeriesID.Set && !sameNullableString(event.SeriesID, update.SeriesID.Value) {
		changed = append(changed, "series")
	}
	if update.SeriesOrder.Set && !sameNullableInt32(event.SeriesOrder, update.SeriesOrder.Value) {
		changed = append(changed, "series_order")
	}
	if update.StartsAt != nil && !update.StartsAt.Equal(event.StartsAt) {
		changed = append(changed, "starts_at")
	}
	if update.EndsAt.Set && !sameNullableTime(event.EndsAt, update.EndsAt.Value) {
		changed = append(changed, "ends_at")
	}
	if update.Timezone != nil && *update.Timezone != event.Timezone {
		changed = append(changed, "timezone")
	}
	if update.AllDay != nil && *update.AllDay != event.AllDay {
		changed = append(changed, "all_day")
	}
	if update.LocationMode != nil && *update.LocationMode != event.LocationMode {
		changed = append(changed, "location_mode")
	}
	if update.MapPlaceID.Set && !sameNullableString(event.MapPlaceID, update.MapPlaceID.Value) {
		changed = append(changed, "map_place_id")
	}
	if update.Slug != nil && *update.Slug != event.Slug {
		changed = append(changed, "slug")
	}
	if update.TicketURL.Set && !sameNullableString(event.TicketURL, update.TicketURL.Value) {
		changed = append(changed, "ticket_url")
	}
	if update.StreamURL.Set && !sameNullableString(event.StreamURL, update.StreamURL.Value) {
		changed = append(changed, "stream_url")
	}
	if update.ExternalURL.Set && !sameNullableString(event.ExternalURL, update.ExternalURL.Value) {
		changed = append(changed, "external_url")
	}
	return changed
}

func validateProgramEventSeriesRelationForUpdate(ctx context.Context, store Store, current Event, update programEventUpdate) error {
	seriesID := current.SeriesID
	if update.SeriesID.Set {
		seriesID = update.SeriesID.Value
	}
	seriesOrder := current.SeriesOrder
	if update.SeriesOrder.Set {
		seriesOrder = update.SeriesOrder.Value
	}
	return validateProgramEventSeriesRelation(ctx, store, current.ID, seriesID, seriesOrder)
}

func validateProgramEventSeriesRelation(ctx context.Context, store Store, eventID string, seriesID *string, seriesOrder *int32) error {
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
	if err := store.LockSeriesID(ctx, *seriesID); err != nil {
		if errors.Is(err, ErrSeriesNotFound) {
			return domainerr.InvalidField("series_id", "program event series does not exist")
		}
		return err
	}
	taken, err := store.SeriesOrderTaken(ctx, *seriesID, *seriesOrder, eventID)
	if err != nil {
		return err
	}
	if taken {
		return domainerr.InvalidField("series_order", "must be unique within the program event series")
	}
	return nil
}

func applyProgramEventPosterUpdate(ctx context.Context, store Store, eventID string, posterFileID *string) error {
	if posterFileID == nil {
		return nil
	}
	if strings.TrimSpace(*posterFileID) == "" {
		return deleteDefaultProgramEventPosterMedia(ctx, store, eventID)
	}
	return addProgramEventMedia(ctx, store, eventID, *posterFileID, "poster", nil, nil, true)
}

func replaceProgramEventUpdateRelations(ctx context.Context, store Store, eventID string, command UpdateProgramEventCommand) ([]string, programEventCreditReplacement, error) {
	creditReplacement := programEventCreditReplacement{}
	if err := validateProgramEventRelationBaselines(command); err != nil {
		return nil, creditReplacement, err
	}
	changed := make([]string, 0, 3)
	for _, relation := range []struct {
		field string
		kind  RelationKind
		edit  RelationsEdit
	}{
		{"artists", ArtistRelation, command.Artists},
		{"labels", LabelRelation, command.Labels},
		{"clients", ClientRelation, command.Clients},
	} {
		if !relation.edit.requested() {
			continue
		}
		didChange, err := mergeAndApplyProgramEventRelations(ctx, store, relation.kind, eventID, *relation.edit.Observed, relation.edit.Desired)
		if err != nil {
			return nil, creditReplacement, err
		}
		if didChange {
			changed = append(changed, relation.field)
		}
	}
	if command.Credits.requested() {
		current, err := store.Credits(ctx, eventID)
		if err != nil {
			return nil, creditReplacement, err
		}
		if !sameProgramEventCredits(current, command.Credits.Desired) {
			creditReplacement, err = replaceProgramEventCredits(ctx, store, eventID, command.Credits.Desired)
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
