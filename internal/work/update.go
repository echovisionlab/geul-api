package work

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/optional"
	"github.com/echovisionlab/geul-api/internal/structured"
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

// Update is an updated Work with its clients.
type Update struct {
	Work    Work
	Changed bool
	Clients []Client
}

type workUpdatePlan struct {
	change         WorkChange
	normalizedSlug *string
	slugPresent    bool
	mapPlace       bool
}

// workUpdate is the outcome of an update.
type workUpdate struct {
	work          Work
	changedFields []string
	changed       bool
}

func (s *Service) UpdateWork(ctx context.Context, command UpdateWorkCommand) (Update, error) {
	if err := s.requireStoredWork(ctx, command.ID); err != nil {
		return Update{}, err
	}
	plan, err := s.buildWorkUpdatePlan(ctx, command)
	if err != nil {
		return Update{}, err
	}
	update, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[workUpdate, Store]{
		Access: lockedWork(command.ID, policyv1.Work.Manage, nil),
		Change: func(ctx context.Context, store Store) (workUpdate, error) {
			return applyWorkUpdatePlan(ctx, store, command, plan)
		},
		Audit: func(update workUpdate) mutation.Audit {
			if len(update.changedFields) == 0 {
				return mutation.Unchanged()
			}
			return workUpdatedAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewWorkMetadataAuditRecord(metadata, update.work.ID, update.changedFields)
			})
		},
		Outbox: func(ctx context.Context, store Store, update workUpdate) error {
			if !update.changed {
				return nil
			}
			return store.PublishContentUpdate(ctx, ContentUpdate{WorkID: command.ID, Kind: SettingsUpdate, Fields: workSettingsUpdateFields(command)})
		},
	})
	if err != nil {
		if errors.Is(err, ErrSlugTaken) && !mutation.IsClassified(err) {
			return Update{}, domainerr.Duplicate("work", "slug", "slug").WithCause(err)
		}
		return Update{}, err
	}
	clients, err := s.getWorkClients(ctx, update.work.ID)
	if err != nil {
		return Update{}, err
	}
	return Update{Work: update.work, Changed: update.changed, Clients: clients}, nil
}

// workSettingsUpdateFields names the settings the command sets, in the
// order the content.updated signal lists them.
func workSettingsUpdateFields(request UpdateWorkCommand) []string {
	fields := make([]string, 0, 11)
	if request.Slug != nil {
		fields = append(fields, "slug")
	}
	if request.Type != nil {
		fields = append(fields, "type")
	}
	if request.Metadata != nil {
		fields = append(fields, "metadata")
	}
	if request.Featured != nil {
		fields = append(fields, "featured")
	}
	if request.Clients != nil {
		fields = append(fields, "clients")
	}
	if request.Year != nil {
		fields = append(fields, "year")
	}
	if request.Month != nil {
		fields = append(fields, "month")
	}
	if request.MapPlaceID != nil {
		fields = append(fields, "mapPlaceId")
	}
	if request.UntilYear != nil {
		fields = append(fields, "untilYear")
	}
	if request.UntilMonth != nil {
		fields = append(fields, "untilMonth")
	}
	if request.IsPresent != nil {
		fields = append(fields, "isPresent")
	}
	return fields
}

func (s *Service) buildWorkUpdatePlan(ctx context.Context, command UpdateWorkCommand) (workUpdatePlan, error) {
	slug, slugPresent := normalizeOptionalNullableString(command.Slug)
	if err := s.validateWorkSlug(ctx, slug); err != nil {
		return workUpdatePlan{}, err
	}
	plan := workUpdatePlan{normalizedSlug: slug, slugPresent: slugPresent}
	if slugPresent {
		plan.change.Slug, plan.change.ClearSlug = slug, slug == nil
	}
	plan.change.Type = command.Type
	plan.change.Featured = command.Featured
	if command.MapPlaceID != nil {
		mapPlaceID, err := s.normalizeMapPlaceID(ctx, *command.MapPlaceID)
		if err != nil {
			return workUpdatePlan{}, err
		}
		plan.mapPlace = true
		plan.change.MapPlaceID, plan.change.ClearPlace = mapPlaceID, mapPlaceID == nil
	}
	return plan, nil
}

// assignWorkUpdateRange writes to change the range values request touches,
// validated against the locked current values.
func assignWorkUpdateRange(work Work, request UpdateWorkCommand, change *WorkChange) error {
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
	if request.Year != nil {
		change.Year = &year
	}
	if request.Month != nil {
		change.Month = &month
	}
	if request.UntilYear != nil {
		change.UntilYear = OptionalInt32{Set: true, Value: untilYear}
	}
	if request.UntilMonth != nil {
		change.UntilMonth = OptionalInt32{Set: true, Value: untilMonth}
	}
	if request.IsPresent != nil {
		change.IsPresent = &isPresent
		if isPresent {
			change.UntilYear = OptionalInt32{Set: true}
			change.UntilMonth = OptionalInt32{Set: true}
		}
	}
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

func applyWorkUpdatePlan(ctx context.Context, store Store, command UpdateWorkCommand, plan workUpdatePlan) (workUpdate, error) {
	var update workUpdate
	work, err := store.FindWork(ctx, command.ID)
	if err != nil {
		return update, err
	}
	update.work = work
	if err := validateWorkUpdateObservedFields(command); err != nil {
		return update, err
	}
	change := plan.change
	if err := assignWorkUpdateRange(work, command, &change); err != nil {
		return update, err
	}
	if command.Metadata != nil {
		change.Metadata = mergeWorkMetadataIntent(
			sanitizeWorkMetadata(work.Metadata),
			sanitizeWorkMetadata(command.ObservedMetadata),
			sanitizeWorkMetadata(command.Metadata),
		)
	}
	if plan.slugPresent && plan.normalizedSlug != nil {
		if err := store.LockResourceRoute(ctx, *plan.normalizedSlug); err != nil {
			return update, err
		}
	}
	update.changedFields = workUpdateChangedFields(work, change, command.Metadata != nil, plan.mapPlace)
	clientsChanged, clientIDs, err := workClientSetChanged(ctx, store, work.ID, command.Clients, command.ObservedClients)
	if err != nil {
		return update, err
	}
	if len(update.changedFields) > 0 {
		change.UpdatedAt = dbtime.Now()
		if err := store.UpdateWork(ctx, work.ID, change); err != nil {
			return update, err
		}
		applyWorkUpdateFields(&update.work, change, command.Metadata != nil)
		update.work.UpdatedAt = change.UpdatedAt
		update.changed = true
	}
	if clientsChanged {
		if err := replaceWorkClients(ctx, store, work.ID, clientIDs); err != nil {
			return update, err
		}
		update.changedFields = append(update.changedFields, "clients")
		update.changed = true
	}
	return update, nil
}

func applyWorkUpdateFields(work *Work, change WorkChange, metadata bool) {
	if change.ClearSlug {
		work.Slug = nil
	} else if change.Slug != nil {
		slug := *change.Slug
		work.Slug = &slug
	}
	if change.Type != nil {
		work.Type = *change.Type
	}
	if metadata {
		work.Metadata = change.Metadata
	}
	if change.Featured != nil {
		work.Featured = *change.Featured
	}
	if change.Year != nil {
		work.Year = *change.Year
	}
	if change.Month != nil {
		work.Month = *change.Month
	}
	if change.UntilYear.Set {
		work.UntilYear = change.UntilYear.Value
	}
	if change.UntilMonth.Set {
		work.UntilMonth = change.UntilMonth.Value
	}
	if change.IsPresent != nil {
		work.IsPresent = *change.IsPresent
	}
	if change.ClearPlace {
		work.MapPlaceID = nil
	} else if change.MapPlaceID != nil {
		mapPlaceID := *change.MapPlaceID
		work.MapPlaceID = &mapPlaceID
	}
}

// workUpdateChangedFields names the columns change writes a different value
// to.
func workUpdateChangedFields(work Work, change WorkChange, metadata, mapPlace bool) []string {
	changed := []string{}
	if change.ClearSlug {
		if work.Slug != nil {
			changed = append(changed, "slug")
		}
	} else if change.Slug != nil && (work.Slug == nil || *change.Slug != *work.Slug) {
		changed = append(changed, "slug")
	}
	if change.Type != nil && *change.Type != work.Type {
		changed = append(changed, "type")
	}
	if metadata && !reflect.DeepEqual(change.Metadata, work.Metadata) {
		changed = append(changed, "metadata")
	}
	if change.Featured != nil && *change.Featured != work.Featured {
		changed = append(changed, "featured")
	}
	if change.Year != nil && *change.Year != work.Year {
		changed = append(changed, "year")
	}
	if change.Month != nil && *change.Month != work.Month {
		changed = append(changed, "month")
	}
	if change.UntilYear.Set && !reflect.DeepEqual(change.UntilYear.Value, work.UntilYear) {
		changed = append(changed, "until_year")
	}
	if change.UntilMonth.Set && !reflect.DeepEqual(change.UntilMonth.Value, work.UntilMonth) {
		changed = append(changed, "until_month")
	}
	if change.IsPresent != nil && *change.IsPresent != work.IsPresent {
		changed = append(changed, "is_present")
	}
	if mapPlace && !optional.Equal(change.MapPlaceID, work.MapPlaceID) {
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
	store Store,
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
	current, err := store.ClientIDs(ctx, workID)
	if err != nil {
		return false, nil, err
	}
	next := mergeWorkClientIntent(current, *observed, *clients)
	return !slices.Equal(current, next), next, nil
}

func replaceWorkClients(ctx context.Context, store Store, workID string, clientIDs []string) error {
	if err := validateWorkClientIDs(ctx, store, clientIDs); err != nil {
		return err
	}
	return store.ReplaceClients(ctx, workID, clientIDs)
}

func validateWorkClientIDs(ctx context.Context, store Store, clientIDs []string) error {
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
	validCount, err := store.CountExistingClients(ctx, clientIDs)
	if err != nil {
		return err
	}
	if int(validCount) != len(clientIDs) {
		return domainerr.InvalidField("clients", "contains unknown client id")
	}
	return nil
}
