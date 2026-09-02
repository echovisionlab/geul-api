package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func initializeProgramEventBlockTranslationSource(
	ctx context.Context,
	tx *gorm.DB,
	eventID string,
	sourceLocale string,
	summary *string,
	now time.Time,
) error {
	if tx == nil {
		return errs.Internal(errors.New("program Event translation source transaction is required"))
	}
	sourceLocale = strings.TrimSpace(sourceLocale)
	if strings.TrimSpace(eventID) == "" || sourceLocale == "" {
		return errs.Internal(errors.New("program Event ID and source locale are required"))
	}
	if err := tx.WithContext(ctx).Create(&model.ProgramEventTranslation{
		EntityID:  eventID,
		Locale:    sourceLocale,
		Summary:   summary,
		CreatedAt: now,
		UpdatedAt: now,
	}).Error; err != nil {
		return errs.Internal(err)
	}
	return nil
}

func (s *ProgramEventService) loadProgramEvent(ctx context.Context, id string) (*managev1.ProgramEvent, error) {
	var output *managev1.ProgramEvent
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var event model.ProgramEvent
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&event, "id = ?", id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("program event", id)
			}
			return errs.Internal(err)
		}
		scoped := *s
		scoped.db = tx
		var err error
		output, err = scoped.toProtoProgramEvent(ctx, &event)
		return err
	})
	return output, err
}

func (s *ProgramEventService) loadAuthorizedProgramEvent(ctx context.Context, id string) (*managev1.ProgramEvent, error) {
	var output *managev1.ProgramEvent
	hydrator, ok := s.fileDeleter.(ContentBlockMediaHydrator)
	if !ok || hydrator == nil {
		return nil, errs.InternalMsg("Program Event Block media hydrator is not configured")
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var event model.ProgramEvent
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&event, "id = ?", id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("program event", id)
			}
			return errs.Internal(err)
		}
		if err := requireActiveProgramEventPrincipal(ctx, tx, "view", true); err != nil {
			return err
		}
		if err := requireProgramEventPermission(
			ctx, s.spiceDB, event.ID, programEventViewAction(event.Status),
		); err != nil {
			return err
		}
		scoped := *s
		scoped.db = tx
		var err error
		output, err = scoped.toProtoProgramEvent(ctx, &event)
		if err != nil {
			return err
		}
		documentID, err := loadProgramEventContentDocumentID(ctx, tx, event.ID, false)
		if err != nil {
			return err
		}
		output.BlockMedia, err = hydrator.HydrateAuthorizedProgramEventBlockMediaWithDB(
			ctx, tx, event.ID, documentID, auth.GetUser(ctx), output.BlockMedia,
		)
		return err
	})
	return output, err
}

func (s *ProgramEventService) toProtoProgramEvent(ctx context.Context, event *model.ProgramEvent) (*managev1.ProgramEvent, error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Program Event content Block store is not configured")
	}
	documentID, err := loadProgramEventContentDocumentID(ctx, s.db, event.ID, false)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, s.db, documentID, event.SourceLocale)
	if err != nil {
		return nil, normalizeProgramEventContentBlockError(err)
	}
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return nil, normalizeProgramEventContentBlockError(err)
	}
	blockMedia, err := LoadContentBlockMediaReferences(ctx, s.db, documentID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	locales, err := loadProgramEventLocales(
		ctx,
		s.db,
		event.ID,
		snapshot,
		newContentBlockFileRenderResolver(s.db, s.runtime),
	)
	if err != nil {
		return nil, err
	}
	artists, err := loadProgramEventArtists(ctx, s.db, event.ID)
	if err != nil {
		return nil, err
	}
	labels, err := loadProgramEventLabels(ctx, s.db, event.ID)
	if err != nil {
		return nil, err
	}
	clients, err := loadProgramEventClients(ctx, s.db, event.ID)
	if err != nil {
		return nil, err
	}
	credits, err := s.loadProgramEventCredits(ctx, event.ID)
	if err != nil {
		return nil, err
	}
	media, err := loadProgramEventMedia(ctx, s.db, event.ID)
	if err != nil {
		return nil, err
	}
	primaryPosterFileID, err := loadProgramEventPrimaryPosterFileID(ctx, s.db, event.ID)
	if err != nil {
		return nil, err
	}
	proto := &managev1.ProgramEvent{
		Id:               event.ID,
		Status:           manageProgramEventStatus(event.Status),
		SourceLocale:     event.SourceLocale,
		TypeId:           event.TypeID,
		SeriesId:         event.SeriesID,
		SeriesOrder:      event.SeriesOrder,
		StartsAt:         timestamppb.New(event.StartsAt),
		EndsAt:           timestampProtoPtr(event.EndsAt),
		Timezone:         event.Timezone,
		AllDay:           event.AllDay,
		LocationMode:     manageProgramEventLocationMode(event.LocationMode),
		MapPlaceId:       event.MapPlaceID,
		PosterFileId:     primaryPosterFileID,
		TicketUrl:        event.TicketURL,
		StreamUrl:        event.StreamURL,
		ExternalUrl:      event.ExternalURL,
		PublishedAt:      timestampProtoPtr(event.PublishedAt),
		Locales:          locales,
		Artists:          artists,
		Labels:           labels,
		Clients:          clients,
		Credits:          credits,
		CreatedAt:        timestamppb.New(event.CreatedAt),
		UpdatedAt:        timestamppb.New(event.UpdatedAt),
		Media:            media,
		Title:            event.Title,
		Slug:             event.Slug,
		Document:         document,
		DocumentRevision: snapshot.Document.Revision.String(),
		BlockMedia:       blockMedia,
	}
	return proto, nil
}

func (s *ProgramEventSeriesService) loadProgramEventSeriesRow(ctx context.Context, idOrSlug string) (*model.ProgramEventSeries, error) {
	series := &model.ProgramEventSeries{}
	db := s.db.WithContext(ctx)
	var err error
	if _, parseErr := uuid.Parse(idOrSlug); parseErr == nil {
		err = db.First(series, "id = ?", idOrSlug).Error
	} else {
		err = db.First(series, "slug = ?", idOrSlug).Error
	}
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("program event series", idOrSlug)
		}
		return nil, errs.Internal(err)
	}
	return series, nil
}

func (s *ProgramEventSeriesService) loadProgramEventSeries(ctx context.Context, idOrSlug string) (*managev1.ProgramEventSeries, error) {
	series, err := s.loadProgramEventSeriesRow(ctx, idOrSlug)
	if err != nil {
		return nil, err
	}
	return s.toProtoProgramEventSeries(ctx, series)
}

func (s *ProgramEventSeriesService) toProtoProgramEventSeries(ctx context.Context, series *model.ProgramEventSeries) (*managev1.ProgramEventSeries, error) {
	return &managev1.ProgramEventSeries{
		Id:           series.ID,
		Status:       manageProgramEventSeriesStatus(series.Status),
		PosterFileId: series.PosterFileID,
		Slug:         series.Slug,
		Title:        series.Title,
		Summary:      series.Summary,
		Description:  series.Description,
		CreatedAt:    timestamppb.New(series.CreatedAt),
		UpdatedAt:    timestamppb.New(series.UpdatedAt),
	}, nil
}

func (s *ProgramEventTypeService) loadProgramEventType(ctx context.Context, id string) (*managev1.ProgramEventType, error) {
	var eventType model.ProgramEventType
	if err := s.db.WithContext(ctx).First(&eventType, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("program event type", id)
		}
		return nil, errs.Internal(err)
	}
	return s.toProtoProgramEventType(ctx, &eventType)
}

func (s *ProgramEventTypeService) toProtoProgramEventType(ctx context.Context, eventType *model.ProgramEventType) (*managev1.ProgramEventType, error) {
	var locales []model.ProgramEventTypeLocale
	if err := s.db.WithContext(ctx).Where("type_id = ?", eventType.ID).Order("locale ASC").Find(&locales).Error; err != nil {
		return nil, errs.Internal(err)
	}
	protoLocales := make([]*managev1.ProgramEventTypeLocale, 0, len(locales))
	for i := range locales {
		protoLocales = append(protoLocales, &managev1.ProgramEventTypeLocale{
			Locale:      locales[i].Locale,
			Name:        locales[i].Name,
			Description: locales[i].Description,
		})
	}
	return &managev1.ProgramEventType{
		Id:                eventType.ID,
		Slug:              eventType.Slug,
		Status:            manageProgramEventTypeStatus(eventType.Status),
		SortOrder:         eventType.SortOrder,
		RequiresPlace:     eventType.RequiresPlace,
		RequiresStreamUrl: eventType.RequiresStreamURL,
		Locales:           protoLocales,
		CreatedAt:         timestamppb.New(eventType.CreatedAt),
		UpdatedAt:         timestamppb.New(eventType.UpdatedAt),
	}, nil
}

func loadProgramEventLocales(
	ctx context.Context,
	db *gorm.DB,
	eventID string,
	snapshot contentblock.Snapshot,
	resolver contentblock.FileRenderResolver,
) ([]*managev1.ProgramEventLocale, error) {
	var rows []struct {
		Locale  string  `gorm:"column:locale"`
		Summary *string `gorm:"column:summary"`
	}
	if err := db.WithContext(ctx).Table("program_event_translation AS translation").
		Select("translation.locale", "translation.summary").
		Joins("JOIN program_event AS event ON event.id = translation.entity_id").
		Where("translation.entity_id = ?", eventID).
		Order("translation.locale = event.source_locale DESC, translation.locale ASC").
		Find(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	result := make([]*managev1.ProgramEventLocale, 0, len(rows))
	for i := range rows {
		document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, rows[i].Locale)
		if err != nil {
			return nil, normalizeProgramEventContentBlockError(err)
		}
		projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, resolver)
		if err != nil {
			return nil, normalizeProgramEventContentBlockError(err)
		}
		result = append(result, &managev1.ProgramEventLocale{
			Locale:      rows[i].Locale,
			Summary:     rows[i].Summary,
			ContentHtml: &projection.HTML,
			ContentText: &projection.Text,
		})
	}
	return result, nil
}
