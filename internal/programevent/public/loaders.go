package public

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/sliceutil"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func loadPublicProgramEventType(ctx context.Context, db *gorm.DB, typeID string, acceptLanguage string) (*openv1.ProgramEventType, error) {
	var eventType model.ProgramEventType
	if err := db.WithContext(ctx).First(&eventType, "id = ?", typeID).Error; err != nil {
		return nil, err
	}
	localesByType, err := loadPublicProgramEventTypeLocales(ctx, db, []string{eventType.ID})
	if err != nil {
		return nil, err
	}
	return publicProgramEventTypeProto(eventType, localesByType[eventType.ID], acceptLanguage)
}

func loadPublicProgramEventTypes(ctx context.Context, db *gorm.DB, typeIDs []string) (map[string]model.ProgramEventType, error) {
	ids := sliceutil.UniqueTrimmed(typeIDs)
	result := make(map[string]model.ProgramEventType, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []model.ProgramEventType
	if err := db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		result[rows[i].ID] = rows[i]
	}
	return result, nil
}

func loadPublicProgramEventTypeLocales(ctx context.Context, db *gorm.DB, typeIDs []string) (map[string][]publicProgramEventTypeLocaleRow, error) {
	ids := sliceutil.UniqueTrimmed(typeIDs)
	result := make(map[string][]publicProgramEventTypeLocaleRow, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []publicProgramEventTypeLocaleRow
	if err := db.WithContext(ctx).
		Table("program_event_type_locale").
		Select("type_id, locale, name, description").
		Where("type_id IN ?", ids).
		Order("type_id ASC, locale ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.TypeID] = append(result[row.TypeID], row)
	}
	return result, nil
}

func publicProgramEventTypeProto(
	eventType model.ProgramEventType,
	locales []publicProgramEventTypeLocaleRow,
	acceptLanguage string,
) (*openv1.ProgramEventType, error) {
	localeRow, err := selectPublicProgramEventTypeLocaleRows(locales, eventType.ID, acceptLanguage)
	if err != nil {
		return nil, err
	}
	return &openv1.ProgramEventType{
		Id:                eventType.ID,
		Slug:              eventType.Slug,
		Status:            openProgramEventTypeStatus(eventType.Status),
		SortOrder:         eventType.SortOrder,
		Name:              localeRow.Name,
		Description:       localeRow.Description,
		RequiresPlace:     eventType.RequiresPlace,
		RequiresStreamUrl: eventType.RequiresStreamURL,
	}, nil
}

type publicProgramEventTypeLocaleRow struct {
	TypeID      string  `gorm:"column:type_id"`
	Locale      string  `gorm:"column:locale"`
	Name        string  `gorm:"column:name"`
	Description *string `gorm:"column:description"`
}

func selectPublicProgramEventTypeLocaleRows(rows []publicProgramEventTypeLocaleRow, typeID string, acceptLanguage string) (publicProgramEventTypeLocaleRow, error) {
	if len(rows) == 0 {
		return publicProgramEventTypeLocaleRow{}, domainerr.ResourceNotFound("program event type locale", typeID)
	}
	requested := resolveRequestedLocale(acceptLanguage)
	if matched, ok := findProgramEventTypeLocale(rows, requested); ok {
		return matched, nil
	}
	if matched, ok := findProgramEventTypeLocale(rows, defaultPublicLocale); ok {
		return matched, nil
	}
	return rows[0], nil
}

func collectPublicProgramEventTypeIDs(events []model.ProgramEvent) []string {
	typeIDs := make([]string, 0, len(events))
	for i := range events {
		if events[i].TypeID != "" {
			typeIDs = append(typeIDs, events[i].TypeID)
		}
	}
	return sliceutil.UniqueTrimmed(typeIDs)
}

// loadProgramEventPosterFileIDs applies the public poster selection rule to an entire result page.
func loadProgramEventPosterFileIDs(ctx context.Context, db *gorm.DB, eventIDs []string) (map[string]string, error) {
	ids := sliceutil.UniqueTrimmed(eventIDs)
	fileByEvent := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return fileByEvent, nil
	}
	var rows []struct {
		EventID string `gorm:"column:event_id"`
		FileID  string `gorm:"column:file_id"`
	}
	err := db.WithContext(ctx).Raw(`
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

func findProgramEventTypeLocale(rows []publicProgramEventTypeLocaleRow, locale string) (publicProgramEventTypeLocaleRow, bool) {
	for _, row := range rows {
		if normalizeSourceLocale(row.Locale) == normalizeSourceLocale(locale) {
			return row, true
		}
	}
	return publicProgramEventTypeLocaleRow{}, false
}

func loadProgramEventPosterAsset(ctx context.Context, db *gorm.DB, assets Assets, eventID string) *commonv1.AssetRef {
	var file struct {
		FileID string `gorm:"column:file_id"`
	}
	if err := db.WithContext(ctx).Raw(
		`SELECT pem.file_id
		 FROM program_event_media AS pem
		 WHERE pem.event_id = ? AND pem.role = 'poster'
		 ORDER BY
		   pem.is_primary DESC,
		   pem.sort_order ASC,
		   pem.created_at ASC
		 LIMIT 1`,
		eventID,
	).Scan(&file).Error; err != nil || strings.TrimSpace(file.FileID) == "" {
		if err != nil {
			slog.ErrorContext(ctx, "Program Event poster lookup failed; serving event without poster", "domain", "programevent", "operation", "load_poster", failurecause.Attr(err), "event_id", eventID)
		}
		return nil
	}
	return assets.ResolveReadyAssetForSourceFile(ctx, file.FileID, "poster", "image")
}

func loadProgramEventFileAsset(ctx context.Context, assets Assets, fileID *string) *commonv1.AssetRef {
	if fileID == nil || strings.TrimSpace(*fileID) == "" {
		return nil
	}
	return assets.ResolveReadyAssetForSourceFile(ctx, *fileID, "image", "poster", "map_image")
}

func loadPublicProgramEventMapPlace(ctx context.Context, db *gorm.DB, mapPlaceID string) (*openv1.MapPlaceBasic, error) {
	var place model.MapPlace
	if err := db.WithContext(ctx).First(&place, "id = ?", mapPlaceID).Error; err != nil {
		return nil, err
	}
	protoPlace := &openv1.MapPlaceBasic{
		Id:      place.ID,
		Name:    place.Name,
		Address: place.Address,
		Lat:     place.Lat,
		Lng:     place.Lng,
	}
	if place.GooglePlaceID != nil && *place.GooglePlaceID != "" {
		protoPlace.GooglePlaceId = place.GooglePlaceID
	}
	return protoPlace, nil
}

type publicProgramEventParticipantRow struct {
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

func loadPublicProgramEventArtists(ctx context.Context, db *gorm.DB, eventID string) ([]*openv1.ProgramEventArtist, error) {
	rows, err := loadPublicProgramEventParticipants(ctx, db, eventID, publicProgramEventParticipantQuery{
		Junction:   "program_event_artist AS pea",
		Projection: "a.id, " + creativeSourceTitleSQL("artist", "a") + " AS name, a.slug, pea.role, pea.sort_order",
		Join:       "JOIN artist AS a ON a.id = pea.artist_id",
		Where:      "pea.event_id = ? AND a.status = ?",
		Status:     managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String(),
		Order:      "pea.sort_order ASC, name ASC",
	})
	if err != nil {
		return nil, err
	}
	result := make([]*openv1.ProgramEventArtist, 0, len(rows))
	for _, row := range rows {
		result = append(result, &openv1.ProgramEventArtist{
			Id:        row.ID,
			Name:      row.Name,
			Slug:      row.Slug,
			Role:      row.Role,
			SortOrder: row.SortOrder,
		})
	}
	return result, nil
}

type publicProgramEventParticipantQuery struct {
	Junction   string
	Projection string
	Join       string
	Where      string
	Status     string
	Order      string
}

func loadPublicProgramEventParticipants(
	ctx context.Context,
	db *gorm.DB,
	eventID string,
	query publicProgramEventParticipantQuery,
) ([]publicProgramEventParticipantRow, error) {
	var rows []publicProgramEventParticipantRow
	if err := db.WithContext(ctx).
		Table(query.Junction).
		Select(query.Projection).
		Joins(query.Join).
		Where(query.Where, eventID, query.Status).
		Order(query.Order).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func loadPublicProgramEventLabels(ctx context.Context, db *gorm.DB, eventID string) ([]*openv1.ProgramEventLabel, error) {
	rows, err := loadPublicProgramEventParticipants(ctx, db, eventID, publicProgramEventParticipantQuery{
		Junction:   "program_event_label AS pel",
		Projection: "l.id, " + creativeSourceTitleSQL("label", "l") + " AS name, l.slug, pel.role, pel.sort_order",
		Join:       "JOIN label AS l ON l.id = pel.label_id",
		Where:      "pel.event_id = ? AND l.status = ?",
		Status:     managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String(),
		Order:      "pel.sort_order ASC, name ASC",
	})
	if err != nil {
		return nil, err
	}
	result := make([]*openv1.ProgramEventLabel, 0, len(rows))
	for _, row := range rows {
		result = append(result, &openv1.ProgramEventLabel{
			Id:        row.ID,
			Name:      row.Name,
			Slug:      row.Slug,
			Role:      row.Role,
			SortOrder: row.SortOrder,
		})
	}
	return result, nil
}

type publicProgramEventClientRow struct {
	ID        string  `gorm:"column:id"`
	Name      string  `gorm:"column:name"`
	Website   *string `gorm:"column:website"`
	Role      *string `gorm:"column:role"`
	SortOrder int32   `gorm:"column:sort_order"`
}

func loadPublicProgramEventClients(ctx context.Context, db *gorm.DB, eventID string) ([]*openv1.ProgramEventClient, error) {
	var rows []publicProgramEventClientRow
	if err := db.WithContext(ctx).
		Table("program_event_client AS pec").
		Select("c.id, c.name, c.website, pec.role, pec.sort_order").
		Joins("JOIN client AS c ON c.id = pec.client_id").
		Where("pec.event_id = ?", eventID).
		Order("pec.sort_order ASC, c.name ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]*openv1.ProgramEventClient, 0, len(rows))
	for _, row := range rows {
		result = append(result, &openv1.ProgramEventClient{
			Id:        row.ID,
			Name:      row.Name,
			Website:   row.Website,
			Role:      row.Role,
			SortOrder: row.SortOrder,
		})
	}
	return result, nil
}

type publicProgramEventCreditRow struct {
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

func (s *Service) loadPublicProgramEventCredits(ctx context.Context, eventID string) ([]*openv1.ProgramEventCredit, error) {
	var rows []publicProgramEventCreditRow
	if err := s.db.WithContext(ctx).
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
		Joins("LEFT JOIN artist AS a ON a.id = pec.artist_id AND a.status = ?", managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String()).
		Where("pec.event_id = ?", eventID).
		Order("pec.sort_order ASC, pec.id ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	memberIDs := make([]string, 0, len(rows))
	imageFileIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.ArtistImageFileID != nil {
			imageFileIDs = append(imageFileIDs, *row.ArtistImageFileID)
		}
		if row.MemberID != nil {
			memberIDs = append(memberIDs, *row.MemberID)
		}
	}
	members, err := s.creditMembers.LoadPublicCreditMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	images, err := s.assets.ResolveReadyAssetsForSourceFiles(ctx, imageFileIDs, "image")
	if err != nil {
		return nil, err
	}
	result := make([]*openv1.ProgramEventCredit, 0, len(rows))
	for _, row := range rows {
		item := &openv1.ProgramEventCredit{
			Id:          row.ID,
			DisplayName: row.Display,
			CreditRole:  row.CreditRole,
			Description: row.Description,
			SortOrder:   row.SortOrder,
		}
		if row.ArtistID != nil {
			name := ""
			if row.ArtistName != nil {
				name = *row.ArtistName
			}
			item.Artist = &openv1.ProgramEventArtist{
				Id:   *row.ArtistID,
				Name: name,
				Slug: row.ArtistSlug,
			}
			if row.ArtistImageFileID != nil {
				item.Artist.ImageAsset = images[*row.ArtistImageFileID]
			}
		}
		if row.MemberID != nil {
			item.Member = members[*row.MemberID]
		}
		result = append(result, item)
	}
	return result, nil
}

func collectPublicProgramEventIDs(events []model.ProgramEvent) []string {
	ids := make([]string, 0, len(events))
	for i := range events {
		ids = append(ids, events[i].ID)
	}
	return ids
}
