package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/sitevariable"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
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
		return errors.New("program Event translation source transaction is required")
	}
	sourceLocale = strings.TrimSpace(sourceLocale)
	if strings.TrimSpace(eventID) == "" || sourceLocale == "" {
		return errors.New("program Event ID and source locale are required")
	}
	if err := tx.WithContext(ctx).Create(&model.ProgramEventTranslation{
		EntityID:  eventID,
		Locale:    sourceLocale,
		Summary:   summary,
		CreatedAt: now,
		UpdatedAt: now,
	}).Error; err != nil {
		return err
	}
	return nil
}

// loadProgramEvent returns the administrator view of an event the caller
// just changed, read under a share lock of the event row.
func (s *Service) loadProgramEvent(ctx context.Context, id string) (*managev1.ProgramEvent, error) {
	var output *managev1.ProgramEvent
	err := lockedRead(ctx, s.db, func(tx *gorm.DB) error {
		event, err := lockProgramEventForRead(ctx, tx, id)
		if err != nil {
			return err
		}
		output, err = s.toProtoProgramEvent(ctx, tx, event)
		return err
	})
	return output, err
}

func lockProgramEventForRead(ctx context.Context, tx *gorm.DB, id string) (*model.ProgramEvent, error) {
	var event model.ProgramEvent
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).First(&event, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("program event", id)
		}
		return nil, err
	}
	return &event, nil
}

// loadAuthorizedProgramEvent requires the active principal to view the
// share-locked event and returns its view with authorized Block media.
func (s *Service) loadAuthorizedProgramEvent(ctx context.Context, id string) (*managev1.ProgramEvent, error) {
	var output *managev1.ProgramEvent
	err := lockedRead(ctx, s.db, func(tx *gorm.DB) error {
		event, err := lockProgramEventForRead(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := requireActiveProgramEventPrincipal(ctx, tx, "view", true); err != nil {
			return err
		}
		if err := requireProgramEventPermission(
			ctx, s.spiceDB, event.ID, programEventViewAction(event.Status),
		); err != nil {
			return err
		}
		output, err = s.toProtoProgramEvent(ctx, tx, event)
		if err != nil {
			return err
		}
		documentID, err := loadProgramEventContentDocumentID(ctx, tx, event.ID, false)
		if err != nil {
			return err
		}
		output.BlockMedia, err = s.mediaHydrator.HydrateAuthorizedProgramEventBlockMediaWithDB(
			ctx, tx, event.ID, documentID, auth.GetUser(ctx), output.BlockMedia,
		)
		return err
	})
	return output, err
}

func (s *Service) toProtoProgramEvent(ctx context.Context, db *gorm.DB, event *model.ProgramEvent) (*managev1.ProgramEvent, error) {
	documentID, err := loadProgramEventContentDocumentID(ctx, db, event.ID, false)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, db, documentID, event.SourceLocale)
	if err != nil {
		return nil, normalizeProgramEventContentBlockError(err)
	}
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return nil, normalizeProgramEventContentBlockError(err)
	}
	blockMedia, err := contentblock.LoadMediaReferences(ctx, db, documentID)
	if err != nil {
		return nil, err
	}
	locales, err := loadProgramEventLocales(
		ctx,
		db,
		event.ID,
		snapshot,
		newContentBlockFileRenderResolver(db, s.media),
	)
	if err != nil {
		return nil, err
	}
	artists, err := loadProgramEventArtists(ctx, db, event.ID)
	if err != nil {
		return nil, err
	}
	labels, err := loadProgramEventLabels(ctx, db, event.ID)
	if err != nil {
		return nil, err
	}
	clients, err := loadProgramEventClients(ctx, db, event.ID)
	if err != nil {
		return nil, err
	}
	credits, err := s.loadProgramEventCredits(ctx, db, event.ID)
	if err != nil {
		return nil, err
	}
	media, err := loadProgramEventMedia(ctx, db, event.ID)
	if err != nil {
		return nil, err
	}
	primaryPosterFileID, err := loadProgramEventPrimaryPosterFileID(ctx, db, event.ID)
	if err != nil {
		return nil, err
	}
	proto := &managev1.ProgramEvent{
		Id:               event.ID,
		Status:           programEventStatusToProto(event.Status),
		SourceLocale:     event.SourceLocale,
		TypeId:           event.TypeID,
		SeriesId:         event.SeriesID,
		SeriesOrder:      event.SeriesOrder,
		StartsAt:         timestamppb.New(event.StartsAt),
		EndsAt:           timestampProtoPtr(event.EndsAt),
		Timezone:         event.Timezone,
		AllDay:           event.AllDay,
		LocationMode:     locationModeToProto(event.LocationMode),
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

func (s *Service) loadProgramEventSeriesRow(ctx context.Context, idOrSlug string) (*model.ProgramEventSeries, error) {
	series := &model.ProgramEventSeries{}
	db := s.db.WithContext(ctx)
	var err error
	if _, parseErr := uuid.Parse(idOrSlug); parseErr == nil {
		err = db.First(series, "id = ?", idOrSlug).Error
	} else {
		err = db.First(series, "slug = ?", idOrSlug).Error
	}
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("program event series", idOrSlug)
		}
		return nil, err
	}
	return series, nil
}

func (s *Service) loadProgramEventSeries(ctx context.Context, idOrSlug string) (*managev1.ProgramEventSeries, error) {
	series, err := s.loadProgramEventSeriesRow(ctx, idOrSlug)
	if err != nil {
		return nil, err
	}
	return s.toProtoProgramEventSeries(series)
}

func (s *Service) toProtoProgramEventSeries(series *model.ProgramEventSeries) (*managev1.ProgramEventSeries, error) {
	return &managev1.ProgramEventSeries{
		Id:           series.ID,
		Status:       programEventSeriesStatusToProto(series.Status),
		PosterFileId: series.PosterFileID,
		Slug:         series.Slug,
		Title:        series.Title,
		Summary:      series.Summary,
		Description:  series.Description,
		CreatedAt:    timestamppb.New(series.CreatedAt),
		UpdatedAt:    timestamppb.New(series.UpdatedAt),
	}, nil
}

func (s *Service) loadProgramEventType(ctx context.Context, id string) (*managev1.ProgramEventType, error) {
	var eventType model.ProgramEventType
	if err := s.db.WithContext(ctx).First(&eventType, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("program event type", id)
		}
		return nil, err
	}
	return s.toProtoProgramEventType(ctx, &eventType)
}

func (s *Service) toProtoProgramEventType(ctx context.Context, eventType *model.ProgramEventType) (*managev1.ProgramEventType, error) {
	var locales []model.ProgramEventTypeLocale
	if err := s.db.WithContext(ctx).Where("type_id = ?", eventType.ID).Order("locale ASC").Find(&locales).Error; err != nil {
		return nil, err
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
		Status:            programEventTypeStatusToProto(eventType.Status),
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
		return nil, err
	}
	result := make([]*managev1.ProgramEventLocale, 0, len(rows))
	for i := range rows {
		document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, rows[i].Locale)
		if err != nil {
			return nil, normalizeProgramEventContentBlockError(err)
		}
		var variables sitevariable.Context
		if contentblock.HasSiteVariables(document) {
			variables, err = sitevariable.LoadRuntime(ctx, db)
			if err != nil {
				return nil, err
			}
		}
		projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, resolver, variables)
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
