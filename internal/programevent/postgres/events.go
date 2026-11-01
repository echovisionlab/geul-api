package programeventpostgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/programevent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

const programEventSourceTitleSQL = "program_event.title"

var eventSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"starts_at":    "starts_at",
		"published_at": "published_at",
		"updated_at":   "updated_at",
		"created_at":   "created_at",
	},
	DefaultSort: "starts_at DESC",
}

var eventFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:       queryutil.TypeText,
			AllowedOps: queryutil.SearchOps,
			SearchColumns: []string{
				programEventSourceTitleSQL,
			},
		},
		"slug": {
			Column:     "slug",
			Type:       queryutil.TypeText,
			AllowedOps: queryutil.TextOps,
		},
		"status": {
			Column:     "status",
			Type:       queryutil.TypeEnum,
			AllowedOps: queryutil.EnumOps,
			EnumValues: []string{
				string(model.ProgramEventStatusDraft),
				string(model.ProgramEventStatusPublished),
				string(model.ProgramEventStatusArchived),
			},
		},
		"type_id": {
			Column:     "type_id",
			Type:       queryutil.TypeID,
			AllowedOps: queryutil.IDOps,
			IsFK:       true,
		},
		"series_id": {
			Column:     "series_id",
			Type:       queryutil.TypeID,
			AllowedOps: queryutil.IDOps,
			IsFK:       true,
		},
		"map_place_id": {
			Column: "map_place_id",
			Type:   queryutil.TypeID,
			AllowedOps: []commonv1.FilterOp{
				commonv1.FilterOp_FILTER_OP_EQ,
				commonv1.FilterOp_FILTER_OP_NEQ,
				commonv1.FilterOp_FILTER_OP_IN,
				commonv1.FilterOp_FILTER_OP_NOT_IN,
				commonv1.FilterOp_FILTER_OP_IS_NULL,
				commonv1.FilterOp_FILTER_OP_IS_NOT_NULL,
			},
			IsFK: true,
		},
		"starts_at": {
			Column:     "starts_at",
			Type:       queryutil.TypeDate,
			AllowedOps: queryutil.DateOps,
		},
		"published_at": {
			Column:     "published_at",
			Type:       queryutil.TypeDate,
			AllowedOps: queryutil.DateOps,
		},
	},
}

func lockClause(lock programevent.RowLock) (clause.Locking, bool) {
	switch lock {
	case programevent.ShareLock:
		return clause.Locking{Strength: clause.LockingStrengthShare}, true
	case programevent.UpdateLock:
		return clause.Locking{Strength: clause.LockingStrengthUpdate}, true
	default:
		return clause.Locking{}, false
	}
}

func withLock(query *gorm.DB, lock programevent.RowLock) *gorm.DB {
	if locking, ok := lockClause(lock); ok {
		return query.Clauses(locking)
	}
	return query
}

func eventNotFound(err error) error { return notFound(err, programevent.ErrEventNotFound) }

func eventFromRow(row model.ProgramEvent) programevent.Event {
	return programevent.Event{
		ID: row.ID, ContentDocumentID: row.ContentDocumentID, Title: row.Title, Slug: row.Slug,
		Status: programevent.Status(row.Status), SourceLocale: row.SourceLocale, TypeID: row.TypeID,
		SeriesID: row.SeriesID, SeriesOrder: row.SeriesOrder, StartsAt: row.StartsAt, EndsAt: row.EndsAt,
		Timezone: row.Timezone, AllDay: row.AllDay, LocationMode: programevent.LocationMode(row.LocationMode),
		MapPlaceID: row.MapPlaceID, TicketURL: row.TicketURL, StreamURL: row.StreamURL, ExternalURL: row.ExternalURL,
		PublishedAt: row.PublishedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func eventRow(event programevent.Event) model.ProgramEvent {
	return model.ProgramEvent{
		ID: event.ID, ContentDocumentID: event.ContentDocumentID, Title: event.Title, Slug: event.Slug,
		Status: model.ProgramEventStatus(event.Status), SourceLocale: event.SourceLocale, TypeID: event.TypeID,
		SeriesID: event.SeriesID, SeriesOrder: event.SeriesOrder, StartsAt: event.StartsAt, EndsAt: event.EndsAt,
		Timezone: event.Timezone, AllDay: event.AllDay, LocationMode: model.ProgramEventLocationMode(event.LocationMode),
		MapPlaceID: event.MapPlaceID, TicketURL: event.TicketURL, StreamURL: event.StreamURL, ExternalURL: event.ExternalURL,
		PublishedAt: event.PublishedAt, CreatedAt: event.CreatedAt, UpdatedAt: event.UpdatedAt,
	}
}

func (s *store) Event(ctx context.Context, eventID string, lock programevent.RowLock) (programevent.Event, error) {
	var row model.ProgramEvent
	if err := withLock(s.tx.WithContext(ctx), lock).First(&row, "id = ?", eventID).Error; err != nil {
		return programevent.Event{}, eventNotFound(err)
	}
	return eventFromRow(row), nil
}

func (s *store) RequireEvent(ctx context.Context, eventID string) error {
	var event model.ProgramEvent
	return eventNotFound(s.tx.WithContext(ctx).Select("id").Take(&event, "id = ?", eventID).Error)
}

func (s *store) LockEventID(ctx context.Context, eventID string) error {
	var event struct {
		ID string `gorm:"column:id"`
	}
	return eventNotFound(s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Table("program_event").
		Select("id").
		Where("id = ?", eventID).
		Take(&event).Error)
}

func (s *store) EventStatus(ctx context.Context, eventID string, lock programevent.RowLock) (programevent.Status, error) {
	var event struct {
		Status model.ProgramEventStatus `gorm:"column:status"`
	}
	if err := withLock(s.tx.WithContext(ctx).Table("program_event"), lock).
		Select("status").Where("id = ?", eventID).Take(&event).Error; err != nil {
		return "", eventNotFound(err)
	}
	return programevent.Status(event.Status), nil
}

func lifecycleFromRow(row model.ProgramEvent) programevent.Lifecycle {
	return programevent.Lifecycle{ID: row.ID, Status: programevent.Status(row.Status), PublishedAt: row.PublishedAt, UpdatedAt: row.UpdatedAt}
}

func (s *store) LockLifecycle(ctx context.Context, eventID string) (programevent.Lifecycle, error) {
	var current model.ProgramEvent
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "status", "published_at", "updated_at").
		First(&current, "id = ?", eventID).Error; err != nil {
		return programevent.Lifecycle{}, eventNotFound(err)
	}
	return lifecycleFromRow(current), nil
}

func (s *store) SetLifecycle(ctx context.Context, eventID string, status programevent.Status, publishedAt *time.Time, updatedAt time.Time) (programevent.Lifecycle, error) {
	current := model.ProgramEvent{ID: eventID}
	updates := structured.Fields{"status": model.ProgramEventStatus(status), "updated_at": updatedAt}
	if publishedAt != nil {
		updates["published_at"] = *publishedAt
	}
	if err := s.tx.Model(&current).Clauses(clause.Returning{Columns: []clause.Column{
		{Name: "status"}, {Name: "published_at"}, {Name: "updated_at"},
	}}).Updates(updates).Error; err != nil {
		return programevent.Lifecycle{}, err
	}
	return lifecycleFromRow(current), nil
}

func (s *store) InsertEvent(ctx context.Context, event *programevent.Event) error {
	row := eventRow(*event)
	if err := s.tx.Omit("ID").Clauses(clause.Returning{}).Create(&row).Error; err != nil {
		return slugTaken(err)
	}
	*event = eventFromRow(row)
	return nil
}

// settingsColumns is the program_event row update for the fields update
// sets.
func settingsColumns(update programevent.SettingsUpdate) structured.Fields {
	columns := structured.Fields{"updated_at": update.UpdatedAt}
	if update.TypeID != nil {
		columns["type_id"] = *update.TypeID
	}
	if update.StartsAt != nil {
		columns["starts_at"] = *update.StartsAt
	}
	if update.EndsAt.Set {
		columns["ends_at"] = nullableValue(update.EndsAt)
	}
	if update.Timezone != nil {
		columns["timezone"] = *update.Timezone
	}
	if update.AllDay != nil {
		columns["all_day"] = *update.AllDay
	}
	if update.LocationMode != nil {
		columns["location_mode"] = model.ProgramEventLocationMode(*update.LocationMode)
	}
	if update.Slug != nil {
		columns["slug"] = *update.Slug
	}
	for column, value := range map[string]programevent.Nullable[string]{
		"map_place_id": update.MapPlaceID,
		"series_id":    update.SeriesID,
		"ticket_url":   update.TicketURL,
		"stream_url":   update.StreamURL,
		"external_url": update.ExternalURL,
	} {
		if value.Set {
			columns[column] = nullableValue(value)
		}
	}
	if update.SeriesOrder.Set {
		columns["series_order"] = nullableValue(update.SeriesOrder)
	}
	return columns
}

// nullableValue is the column value of a set nullable update: nil writes
// NULL.
func nullableValue[T any](value programevent.Nullable[T]) any {
	if value.Value == nil {
		return nil
	}
	return *value.Value
}

func (s *store) UpdateSettings(ctx context.Context, eventID string, update programevent.SettingsUpdate) error {
	return slugTaken(s.tx.Model(&model.ProgramEvent{ID: eventID}).Updates(settingsColumns(update)).Error)
}

func (s *store) TouchEvent(ctx context.Context, eventID string, at time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.ProgramEvent{}).Where("id = ?", eventID).Update("updated_at", at).Error
}

func (s *store) EventUpdatedAt(ctx context.Context, eventID string) (time.Time, error) {
	var event struct {
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	if err := s.tx.WithContext(ctx).Table("program_event").Select("updated_at").Where("id = ?", eventID).Take(&event).Error; err != nil {
		return time.Time{}, eventNotFound(err)
	}
	return event.UpdatedAt, nil
}

func (s *store) DeleteEvent(ctx context.Context, eventID string) (bool, error) {
	result := s.tx.Delete(&model.ProgramEvent{}, "id = ?", eventID)
	return result.RowsAffected > 0, result.Error
}

func (s *store) ListEvents(ctx context.Context, page programevent.ListPage) ([]programevent.Event, int64, error) {
	rows := s.tx.WithContext(ctx).Model(&model.ProgramEvent{})
	rows, err := eventFilterConfig.ApplyFilters(rows, page.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := rows.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows, err = eventSortConfig.ApplySort(rows, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var events []model.ProgramEvent
	if err := rows.Order("program_event.id ASC").Limit(int(page.Limit)).Offset(int(page.Offset)).Find(&events).Error; err != nil {
		return nil, 0, err
	}
	result := make([]programevent.Event, 0, len(events))
	for i := range events {
		result = append(result, eventFromRow(events[i]))
	}
	return result, total, nil
}

func (s *store) ContentDocumentID(ctx context.Context, eventID string, lock programevent.RowLock) (*string, error) {
	query := withLock(s.tx.WithContext(ctx).
		Table("program_event").
		Select("content_document_id").
		Where("id = ?", eventID), lock)
	var root struct {
		ContentDocumentID sql.NullString `gorm:"column:content_document_id"`
	}
	if err := query.Take(&root).Error; err != nil {
		return nil, eventNotFound(err)
	}
	if !root.ContentDocumentID.Valid {
		return nil, nil
	}
	return &root.ContentDocumentID.String, nil
}

func (s *store) SourceLocale(ctx context.Context, eventID string, lock programevent.RowLock) (string, error) {
	query := withLock(s.tx.WithContext(ctx).Table("program_event").
		Select("source_locale").Where("id = ?", eventID), lock)
	var state struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := query.Take(&state).Error; err != nil {
		return "", eventNotFound(err)
	}
	return state.SourceLocale, nil
}

func (s *store) DocumentRoot(ctx context.Context, eventID string, lock programevent.RowLock) (programevent.DocumentRoot, error) {
	query := withLock(s.tx.WithContext(ctx).
		Select("id", "title", "content_document_id", "status", "source_locale").
		Where("id = ?", eventID), lock)
	var root model.ProgramEvent
	if err := query.Take(&root).Error; err != nil {
		return programevent.DocumentRoot{}, eventNotFound(err)
	}
	return programevent.DocumentRoot{
		ID: root.ID, Title: root.Title, ContentDocumentID: root.ContentDocumentID,
		Status: programevent.Status(root.Status), SourceLocale: root.SourceLocale,
	}, nil
}

func (s *store) LockTitle(ctx context.Context, eventID string) (string, string, error) {
	var root model.ProgramEvent
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "title", "source_locale").
		Where("id = ?", eventID).
		Take(&root).Error; err != nil {
		return "", "", eventNotFound(err)
	}
	return root.Title, root.SourceLocale, nil
}

func (s *store) Title(ctx context.Context, eventID string) (string, error) {
	var root struct {
		Title string `gorm:"column:title"`
	}
	if err := s.tx.WithContext(ctx).
		Table("program_event").
		Select("title").
		Where("id = ?", eventID).
		Take(&root).Error; err != nil {
		return "", eventNotFound(err)
	}
	return root.Title, nil
}

func (s *store) SetTitle(ctx context.Context, eventID, title string, updatedAt time.Time) error {
	return s.tx.Model(&model.ProgramEvent{}).
		Where("id = ?", eventID).
		Updates(structured.Fields{"title": title, "updated_at": updatedAt}).Error
}

func (s *store) TranslationSource(ctx context.Context, eventID, locale string) (string, *string, bool, error) {
	var row struct {
		Title   string  `gorm:"column:title"`
		Summary *string `gorm:"column:summary"`
	}
	result := s.tx.WithContext(ctx).Raw(
		`SELECT program_event.title, translation.summary
		 FROM program_event
		 JOIN program_event_translation AS translation
		   ON translation.entity_id = program_event.id
		  AND translation.locale = ?
		 WHERE program_event.id = ?
		 LIMIT 1`,
		locale,
		eventID,
	).Scan(&row)
	if result.Error != nil {
		return "", nil, false, result.Error
	}
	if result.RowsAffected != 1 {
		return "", nil, false, nil
	}
	return row.Title, row.Summary, true, nil
}

// routeNames is the entity and namespace of each route kind.
var routeNames = map[programevent.RouteKind]struct{ entity, namespace string }{
	programevent.EventRoute:  {"program event", "events"},
	programevent.SeriesRoute: {"program event series", "event-series"},
}

func (s *store) CheckRoute(ctx context.Context, kind programevent.RouteKind, slug string) error {
	names := routeNames[kind]
	return routeregistry.EnsureResourceRouteAvailable(ctx, s.tx, names.entity, names.namespace, slug)
}

func (s *store) LockRoute(ctx context.Context, kind programevent.RouteKind, slug string) error {
	names := routeNames[kind]
	return routeregistry.EnsureResourceRouteAvailableInTx(ctx, s.tx, names.entity, names.namespace, slug)
}

func (s *store) SeriesOrderTaken(ctx context.Context, seriesID string, order int32, excludeEventID string) (bool, error) {
	query := s.tx.Select("id").Where("series_id = ? AND series_order = ?", seriesID, order)
	if strings.TrimSpace(excludeEventID) != "" {
		query = query.Where("id <> ?", excludeEventID)
	}
	var duplicate model.ProgramEvent
	err := query.Take(&duplicate).Error
	if err == nil {
		return true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return false, err
}

func (s *store) LockSeriesEvents(ctx context.Context, seriesID string) ([]string, error) {
	var affected []model.ProgramEvent
	if err := s.tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("series_id = ?", seriesID).
		Find(&affected).Error; err != nil {
		return nil, seriesNotFound(err)
	}
	ids := make([]string, 0, len(affected))
	for _, event := range affected {
		ids = append(ids, event.ID)
	}
	return ids, nil
}

func (s *store) DetachFromSeries(ctx context.Context, eventID string, updatedAt time.Time) error {
	return seriesNotFound(s.tx.Model(&model.ProgramEvent{}).Where("id = ?", eventID).
		Updates(structured.Fields{"series_id": nil, "series_order": nil, "updated_at": updatedAt}).Error)
}

func (s *store) CountTypeEvents(ctx context.Context, typeID string) (int64, error) {
	var usageCount int64
	err := s.tx.Model(&model.ProgramEvent{}).Where("type_id = ?", typeID).Count(&usageCount).Error
	return usageCount, err
}
