// Package programeventpublicpostgres is the PostgreSQL store of the public
// Program Event, series and type reads.
package programeventpublicpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	translationpostgres "github.com/echovisionlab/geul-api/internal/translation/postgres"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/programevent/public"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"github.com/echovisionlab/geul-api/internal/translation"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// NewUnits returns the read snapshots of the public Program Event reads
// over db, with the content store they read documents through.
func NewUnits(db *gorm.DB, contentBlocks *contentblock.Store) (mutation.UnitOfWork[public.Store], error) {
	if db == nil {
		return nil, errors.New("public program event store database is required")
	}
	if contentBlocks == nil {
		return nil, errors.New("public program event store content blocks are required")
	}
	return mutation.Postgres(db, func(tx *gorm.DB) public.Store {
		return &store{tx: tx, contentBlocks: contentBlocks}
	}), nil
}

type store struct {
	tx            *gorm.DB
	contentBlocks *contentblock.Store
}

var _ public.Store = (*store)(nil)

// localizationSpec is where a public Program Event's localized summary
// lives.
var localizationSpec = publiccontent.Spec{
	EntityType:   programevent.EntityType,
	TableName:    "program_event_translation",
	SelectClause: "locale, summary, NULL::text AS content_html, NULL::text AS content_text",
}

var programEventPublicSortConfig = &queryutil.SortConfig{
	AllowedFields: map[string]string{
		"title":        "program_event.title",
		"starts_at":    "starts_at",
		"ends_at":      "ends_at",
		"published_at": "published_at",
		"updated_at":   "updated_at",
	},
}

// notFoundError keeps the driver's not-found error readable and in the
// chain while it answers a domain sentinel.
type notFoundError struct {
	error
	sentinel error
}

func (err notFoundError) Is(target error) bool { return target == err.sentinel }
func (err notFoundError) Unwrap() error        { return err.error }

func notFound(err, sentinel error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFoundError{error: err, sentinel: sentinel}
	}
	return err
}

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

func seriesFromRow(row model.ProgramEventSeries) programevent.Series {
	return programevent.Series{
		ID: row.ID, Slug: row.Slug, Status: programevent.Status(row.Status), Title: row.Title,
		Summary: row.Summary, Description: row.Description, PosterFileID: row.PosterFileID,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func typeFromRow(row model.ProgramEventType) programevent.Type {
	return programevent.Type{
		ID: row.ID, Slug: row.Slug, Status: programevent.TypeStatus(row.Status), SortOrder: row.SortOrder,
		RequiresPlace: row.RequiresPlace, RequiresStreamURL: row.RequiresStreamURL,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func (s *store) PublicEvent(ctx context.Context, slugOrID string, byID bool) (programevent.Event, error) {
	var event model.ProgramEvent
	query := s.tx.WithContext(ctx).
		Model(&model.ProgramEvent{}).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where("status IN ?", publicProgramEventStatuses)
	if byID {
		query = query.Where("id = ?", slugOrID)
	} else {
		query = query.Where("slug = ?", slugOrID)
	}
	if err := query.First(&event).Error; err != nil {
		return programevent.Event{}, notFound(err, programevent.ErrEventNotFound)
	}
	return eventFromRow(event), nil
}

func (s *store) ListPublicEvents(ctx context.Context, page programevent.ListPage) ([]programevent.Event, int64, error) {
	rows := s.tx.WithContext(ctx).
		Model(&model.ProgramEvent{}).
		Where("status IN ?", publicProgramEventStatuses)
	rows, err := applyPublicProgramEventFilters(rows, page.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := rows.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows, err = applyProgramEventPublicSort(rows, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var events []model.ProgramEvent
	if err := rows.Limit(int(page.Limit)).Offset(int(page.Offset)).Find(&events).Error; err != nil {
		return nil, 0, err
	}
	result := make([]programevent.Event, 0, len(events))
	for _, event := range events {
		result = append(result, eventFromRow(event))
	}
	return result, total, nil
}

func applyProgramEventPublicSort(query *gorm.DB, sorts []*commonv1.SortSpec) (*gorm.DB, error) {
	if len(sorts) == 0 {
		return query.
			Order(programEventTBAFirstOrder()).
			Order("starts_at ASC").
			Order("id ASC"), nil
	}

	for _, sort := range sorts {
		column, ok := programEventPublicSortConfig.AllowedFields[sort.GetField()]
		if !ok {
			return nil, invalidSortField(sort.GetField())
		}

		order := "ASC"
		if sort.GetOrder() == commonv1.SortOrder_SORT_ORDER_DESC {
			order = "DESC"
		}

		if sort.GetField() == "starts_at" {
			query = query.Order(programEventTBAFirstOrder()).Order("starts_at " + order)
			continue
		}
		query = query.Order(column + " " + order)
	}

	return query.Order("id ASC"), nil
}

func programEventTBAFirstOrder() string {
	return "location_mode = '" + string(model.ProgramEventLocationModeTBA) + "' DESC"
}

func (s *store) ResolveLocalizations(ctx context.Context, eventIDs []string, acceptLanguage string) (map[string]publiccontent.Selection, error) {
	return publiccontent.ResolveBatch(ctx, s.tx, localizationSpec, eventIDs, acceptLanguage, translationpostgres.NewRuntimeSettingsReader(s.tx))
}

func (s *store) ResolveLocalization(ctx context.Context, eventID, acceptLanguage string, settings translation.RuntimeSettings) (publiccontent.Selection, error) {
	return publiccontent.ResolveWithPolicy(ctx, s.tx, localizationSpec, eventID, acceptLanguage, settings)
}

func (s *store) AvailableLocales(ctx context.Context, eventID, sourceLocale string, settings translation.RuntimeSettings) ([]string, error) {
	return publiccontent.AvailableLocales(ctx, s.tx, localizationSpec, eventID, sourceLocale, settings)
}

func (s *store) TranslationSettings(ctx context.Context) (translation.RuntimeSettings, error) {
	return translationpostgres.NewRuntimeSettingsReader(s.tx).RuntimeSettings(ctx)
}

func (s *store) PosterFileIDs(ctx context.Context, eventIDs []string) (map[string]string, error) {
	ids := sliceutil.UniqueTrimmed(eventIDs)
	fileByEvent := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return fileByEvent, nil
	}
	var rows []struct {
		EventID string `gorm:"column:event_id"`
		FileID  string `gorm:"column:file_id"`
	}
	err := s.tx.WithContext(ctx).Raw(`
		SELECT pem.event_id, pem.file_id
		FROM program_event_media AS pem
		WHERE pem.event_id IN ? AND pem.role = 'poster'
		ORDER BY pem.event_id ASC,
			pem.is_primary DESC,
			pem.sort_order ASC,
			pem.created_at ASC,
			pem.id ASC
	`, ids).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if _, exists := fileByEvent[row.EventID]; !exists && strings.TrimSpace(row.FileID) != "" {
			fileByEvent[row.EventID] = row.FileID
		}
	}
	return fileByEvent, nil
}

func (s *store) PosterFileID(ctx context.Context, eventID string) (string, error) {
	var file struct {
		FileID string `gorm:"column:file_id"`
	}
	err := s.tx.WithContext(ctx).Raw(
		`SELECT pem.file_id
		 FROM program_event_media AS pem
		 WHERE pem.event_id = ? AND pem.role = 'poster'
		 ORDER BY
		   pem.is_primary DESC,
		   pem.sort_order ASC,
		   pem.created_at ASC
		 LIMIT 1`,
		eventID,
	).Scan(&file).Error
	return file.FileID, err
}

func (s *store) Types(ctx context.Context, typeIDs []string) (map[string]programevent.Type, error) {
	ids := sliceutil.UniqueTrimmed(typeIDs)
	result := make(map[string]programevent.Type, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []model.ProgramEventType
	if err := s.tx.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ID] = typeFromRow(row)
	}
	return result, nil
}

func (s *store) TypeLocales(ctx context.Context, typeIDs []string) (map[string][]programevent.TypeLocale, error) {
	ids := sliceutil.UniqueTrimmed(typeIDs)
	result := make(map[string][]programevent.TypeLocale, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []struct {
		TypeID      string  `gorm:"column:type_id"`
		Locale      string  `gorm:"column:locale"`
		Name        string  `gorm:"column:name"`
		Description *string `gorm:"column:description"`
	}
	if err := s.tx.WithContext(ctx).
		Table("program_event_type_locale").
		Select("type_id, locale, name, description").
		Where("type_id IN ?", ids).
		Order("type_id ASC, locale ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.TypeID] = append(result[row.TypeID], programevent.TypeLocale{
			TypeID: row.TypeID, Locale: row.Locale, Name: row.Name, Description: row.Description,
		})
	}
	return result, nil
}

func (s *store) Type(ctx context.Context, typeID string) (programevent.Type, error) {
	var eventType model.ProgramEventType
	if err := s.tx.WithContext(ctx).First(&eventType, "id = ?", typeID).Error; err != nil {
		return programevent.Type{}, notFound(err, programevent.ErrTypeNotFound)
	}
	return typeFromRow(eventType), nil
}

func (s *store) ListActiveTypes(ctx context.Context, page programevent.ListPage) ([]programevent.Type, int64, error) {
	rows := s.tx.WithContext(ctx).
		Model(&model.ProgramEventType{}).
		Where("status = ?", model.ProgramEventTypeStatusActive)
	rows, err := applySimpleProgramEventTypeFilters(rows, page.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := rows.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var types []model.ProgramEventType
	if err := rows.Order("sort_order ASC, slug ASC").Limit(int(page.Limit)).Offset(int(page.Offset)).Find(&types).Error; err != nil {
		return nil, 0, err
	}
	result := make([]programevent.Type, 0, len(types))
	for _, row := range types {
		result = append(result, typeFromRow(row))
	}
	return result, total, nil
}

func (s *store) MapPlace(ctx context.Context, mapPlaceID string) (public.MapPlace, error) {
	var place model.MapPlace
	if err := s.tx.WithContext(ctx).First(&place, "id = ?", mapPlaceID).Error; err != nil {
		return public.MapPlace{}, notFound(err, public.ErrMapPlaceNotFound)
	}
	return public.MapPlace{
		ID: place.ID, Name: place.Name, Address: place.Address, Lat: place.Lat, Lng: place.Lng,
		GooglePlaceID: place.GooglePlaceID,
	}, nil
}

func (s *store) PublishedSeries(ctx context.Context, seriesID string) (programevent.Series, bool, error) {
	var series model.ProgramEventSeries
	err := s.tx.WithContext(ctx).
		First(
			&series,
			"id = ? AND status = ?",
			seriesID,
			model.ProgramEventStatusPublished,
		).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return programevent.Series{}, false, nil
	}
	if err != nil {
		return programevent.Series{}, false, err
	}
	return seriesFromRow(series), true, nil
}

func (s *store) FindPublishedSeries(ctx context.Context, slugOrID string, byID bool) (programevent.Series, error) {
	var series model.ProgramEventSeries
	rows := s.tx.WithContext(ctx).
		Model(&model.ProgramEventSeries{}).
		Where("status = ?", model.ProgramEventStatusPublished)
	if byID {
		rows = rows.Where("id = ?", slugOrID)
	} else {
		rows = rows.Where("slug = ?", slugOrID)
	}
	if err := rows.First(&series).Error; err != nil {
		return programevent.Series{}, notFound(err, programevent.ErrSeriesNotFound)
	}
	return seriesFromRow(series), nil
}

func (s *store) ListPublishedSeries(ctx context.Context, page programevent.ListPage) ([]programevent.Series, int64, error) {
	rows := s.tx.WithContext(ctx).
		Model(&model.ProgramEventSeries{}).
		Where("status = ?", model.ProgramEventStatusPublished)
	rows, err := applySimpleProgramEventSeriesFilters(rows, page.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := rows.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var series []model.ProgramEventSeries
	if err := rows.Order("updated_at DESC").Limit(int(page.Limit)).Offset(int(page.Offset)).Find(&series).Error; err != nil {
		return nil, 0, err
	}
	result := make([]programevent.Series, 0, len(series))
	for _, row := range series {
		result = append(result, seriesFromRow(row))
	}
	return result, total, nil
}

type participantRow struct {
	ID        string  `gorm:"column:id"`
	Name      string  `gorm:"column:name"`
	Slug      *string `gorm:"column:slug"`
	Role      *string `gorm:"column:role"`
	SortOrder int32   `gorm:"column:sort_order"`
}

func creativeSourceTitleSQL(entityType, tableAlias string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = entityType
	}
	return fmt.Sprintf(
		"COALESCE((SELECT translation.title FROM %s_translation AS translation WHERE translation.entity_id = %s.id AND translation.locale = %s.source_locale LIMIT 1), '')",
		entityType,
		alias,
		alias,
	)
}

type participantQuery struct {
	Junction   string
	Projection string
	Join       string
	Where      string
	Status     string
	Order      string
}

func (s *store) participants(ctx context.Context, eventID string, query participantQuery) ([]public.Participant, error) {
	var rows []participantRow
	if err := s.tx.WithContext(ctx).
		Table(query.Junction).
		Select(query.Projection).
		Joins(query.Join).
		Where(query.Where, eventID, query.Status).
		Order(query.Order).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	participants := make([]public.Participant, 0, len(rows))
	for _, row := range rows {
		participants = append(participants, public.Participant{ID: row.ID, Name: row.Name, Slug: row.Slug, Role: row.Role, SortOrder: row.SortOrder})
	}
	return participants, nil
}

func (s *store) Artists(ctx context.Context, eventID string) ([]public.Participant, error) {
	return s.participants(ctx, eventID, participantQuery{
		Junction:   "program_event_artist AS pea",
		Projection: "a.id, " + creativeSourceTitleSQL("artist", "a") + " AS name, a.slug, pea.role, pea.sort_order",
		Join:       "JOIN artist AS a ON a.id = pea.artist_id",
		Where:      "pea.event_id = ? AND a.status = ?",
		Status:     string(model.ArtistStatusPublished),
		Order:      "pea.sort_order ASC, name ASC",
	})
}

func (s *store) Labels(ctx context.Context, eventID string) ([]public.Participant, error) {
	return s.participants(ctx, eventID, participantQuery{
		Junction:   "program_event_label AS pel",
		Projection: "l.id, " + creativeSourceTitleSQL("label", "l") + " AS name, l.slug, pel.role, pel.sort_order",
		Join:       "JOIN label AS l ON l.id = pel.label_id",
		Where:      "pel.event_id = ? AND l.status = ?",
		Status:     string(model.LabelStatusPublished),
		Order:      "pel.sort_order ASC, name ASC",
	})
}

func (s *store) Clients(ctx context.Context, eventID string) ([]public.Client, error) {
	var rows []struct {
		ID        string  `gorm:"column:id"`
		Name      string  `gorm:"column:name"`
		Website   *string `gorm:"column:website"`
		Role      *string `gorm:"column:role"`
		SortOrder int32   `gorm:"column:sort_order"`
	}
	if err := s.tx.WithContext(ctx).
		Table("program_event_client AS pec").
		Select("c.id, c.name, c.website, pec.role, pec.sort_order").
		Joins("JOIN client AS c ON c.id = pec.client_id").
		Where("pec.event_id = ?", eventID).
		Order("pec.sort_order ASC, c.name ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	clients := make([]public.Client, 0, len(rows))
	for _, row := range rows {
		clients = append(clients, public.Client{ID: row.ID, Name: row.Name, Website: row.Website, Role: row.Role, SortOrder: row.SortOrder})
	}
	return clients, nil
}

func (s *store) Credits(ctx context.Context, eventID string) ([]public.CreditRow, error) {
	var rows []struct {
		ID                string  `gorm:"column:id"`
		Display           *string `gorm:"column:display_name"`
		CreditRole        *string `gorm:"column:credit_role"`
		Description       *string `gorm:"column:description"`
		SortOrder         int32   `gorm:"column:sort_order"`
		ArtistID          *string `gorm:"column:artist_id"`
		ArtistName        *string `gorm:"column:artist_name"`
		ArtistSlug        *string `gorm:"column:artist_slug"`
		ArtistImageFileID *string `gorm:"column:artist_image_file_id"`
		MemberID          *string `gorm:"column:member_id"`
	}
	if err := s.tx.WithContext(ctx).
		Table("program_event_credit AS pec").
		Select(`
			pec.id,
			pec.display_name,
			pec.credit_role,
			pec.description,
			pec.sort_order,
			a.id AS artist_id,
			`+creativeSourceTitleSQL("artist", "a")+` AS artist_name,
			a.slug AS artist_slug,
			(SELECT file_id FROM artist_file WHERE artist_id = a.id
			 ORDER BY sort_order ASC LIMIT 1) AS artist_image_file_id,
			pec.member_id
		`).
		Joins("LEFT JOIN artist AS a ON a.id = pec.artist_id AND a.status = ?", string(model.ArtistStatusPublished)).
		Where("pec.event_id = ?", eventID).
		Order("pec.sort_order ASC, pec.id ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	credits := make([]public.CreditRow, 0, len(rows))
	for _, row := range rows {
		credits = append(credits, public.CreditRow{
			ID: row.ID, DisplayName: row.Display, CreditRole: row.CreditRole, Description: row.Description,
			SortOrder: row.SortOrder, ArtistID: row.ArtistID, ArtistName: row.ArtistName, ArtistSlug: row.ArtistSlug,
			ArtistImageFileID: row.ArtistImageFileID, MemberID: row.MemberID,
		})
	}
	return credits, nil
}

func (s *store) ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.contentBlocks.LoadSnapshotInTransaction(ctx, s.tx, documentID, sourceLocale)
}

func (s *store) MediaReferences(ctx context.Context, documentID uuid.UUID) ([]*fileref.BlockMediaItem, error) {
	return contentblock.LoadMediaReferences(ctx, s.tx, documentID)
}

func (s *store) ContentFileMIMEType(ctx context.Context, blockID uuid.UUID, referencePath string, fileID uuid.UUID) (string, bool, error) {
	var file struct {
		MIMEType string `gorm:"column:mime_type"`
	}
	result := s.tx.WithContext(ctx).Raw(`
		SELECT file.mime_type
		FROM content_block_attachment AS attachment
		JOIN file ON file.id = attachment.file_id
		WHERE attachment.block_id = ? AND attachment.reference_path = ?
		  AND attachment.selector_kind = 'active' AND attachment.file_id = ?
		  AND file.delete_requested_at IS NULL
	`, blockID, referencePath, fileID).Scan(&file)
	if result.Error != nil {
		return "", false, result.Error
	}
	return file.MIMEType, result.RowsAffected == 1, nil
}

func (s *store) SiteVariables(ctx context.Context) (sitevariable.Context, error) {
	return sitevariable.LoadRuntime(ctx, s.tx)
}
