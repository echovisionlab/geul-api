package public

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/sitevariable"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

var programEventPublicSortConfig = &queryutil.SortConfig{
	AllowedFields: map[string]string{
		"title":        "program_event.title",
		"starts_at":    "starts_at",
		"ends_at":      "ends_at",
		"published_at": "published_at",
		"updated_at":   "updated_at",
	},
}

// GetProgramEventQuery names a published or archived event by slug or ID.
type GetProgramEventQuery struct {
	SlugOrID       string
	AcceptLanguage string
}

// PublicEvent is a public event in its displayed locale with the delivery
// of its Block media.
type PublicEvent struct {
	Event      *openv1.ProgramEvent
	BlockMedia []*contentv1.ContentBlockMediaItem
}

// GetProgramEvent returns a published or archived event in the locale
// AcceptLanguage selects.
func (s *Service) GetProgramEvent(ctx context.Context, query GetProgramEventQuery) (PublicEvent, error) {
	slugOrID := strings.TrimSpace(query.SlugOrID)
	if slugOrID == "" {
		return PublicEvent{}, domainerr.Required("slug")
	}
	var protoEvent *openv1.ProgramEvent
	var media []*contentv1.ContentBlockMediaItem
	var mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization
	err := lockedRead(ctx, s.db, func(tx *gorm.DB) error {
		event, err := loadPublicProgramEvent(ctx, tx, slugOrID)
		if err != nil {
			return err
		}
		scoped := *s
		scoped.db = tx
		blockRead, err := scoped.loadProgramEventBlockRead(ctx, event)
		if err != nil {
			return err
		}
		localization, err := resolveProgramEventLocalization(
			ctx, tx, query.AcceptLanguage, event.ID, blockRead.SourceLocale, blockRead.CompleteLocales,
		)
		if err != nil {
			return err
		}
		protoEvent, err = scoped.toProtoProgramEvent(ctx, event, localization, blockRead)
		if err != nil {
			return err
		}
		media, err = contentblock.LoadMediaReferences(ctx, tx, blockRead.DocumentID)
		mediaAuthorization = mediaasset.ContentDownloadOwnerAuthorization{
			ResourceType: "program_event", ResourceID: event.ID, Status: string(event.Status),
			DocumentID: blockRead.DocumentID.String(), Mode: mediaasset.ContentDownloadOwnerAccessPublic,
		}
		return err
	})
	if err != nil {
		return PublicEvent{}, err
	}
	media, err = s.files.HydrateAuthorizedContentBlockMedia(
		mediaasset.WithContentDownloadOwnerAuthorization(ctx, mediaAuthorization), media,
	)
	if err != nil {
		return PublicEvent{}, err
	}
	return PublicEvent{Event: protoEvent, BlockMedia: media}, nil
}

// loadPublicProgramEvent share-locks the published or archived event that
// slugOrID names.
func loadPublicProgramEvent(ctx context.Context, tx *gorm.DB, slugOrID string) (*model.ProgramEvent, error) {
	var event model.ProgramEvent
	query := tx.WithContext(ctx).
		Model(&model.ProgramEvent{}).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where("status IN ?", publicProgramEventStatuses)
	if _, parseErr := uuid.Parse(slugOrID); parseErr == nil {
		query = query.Where("id = ?", slugOrID)
	} else {
		query = query.Where("slug = ?", slugOrID)
	}
	if err := query.First(&event).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.New(domainerr.NotFound, "", "program event not found")
		}
		return nil, err
	}
	return &event, nil
}

// ListProgramEventsQuery is one page of public events.
type ListProgramEventsQuery struct {
	Limit          int32
	Offset         int32
	Filters        []*commonv1.FilterSpec
	Sorts          []*commonv1.SortSpec
	AcceptLanguage string
}

// EventList is one page of public event summaries.
type EventList struct {
	Events     []*openv1.ProgramEventSummary
	Pagination *commonv1.PaginationResponse
}

// ListProgramEvents returns one page of published and archived events, TBA
// events first by default.
func (s *Service) ListProgramEvents(ctx context.Context, query ListProgramEventsQuery) (EventList, error) {
	rows := s.db.WithContext(ctx).
		Model(&model.ProgramEvent{}).
		Where("status IN ?", publicProgramEventStatuses)
	rows, err := applyPublicProgramEventFilters(rows, query.Filters)
	if err != nil {
		return EventList{}, err
	}
	var total int64
	if err := rows.Count(&total).Error; err != nil {
		return EventList{}, err
	}
	rows, err = applyProgramEventPublicSort(rows, query.Sorts)
	if err != nil {
		return EventList{}, err
	}
	pagination := queryutil.ExtractPaginationRaw(query.Limit, query.Offset, queryutil.DefaultPaginationConfig)
	var events []model.ProgramEvent
	if err := pagination.Apply(rows).Find(&events).Error; err != nil {
		return EventList{}, err
	}
	summaries, err := s.programEventSummaries(ctx, events, query.AcceptLanguage)
	if err != nil {
		return EventList{}, err
	}
	return EventList{Events: summaries, Pagination: pagination.BuildResponse(total)}, nil
}

// programEventSummaries projects a page of events in the locale
// acceptLanguage selects, with their posters and types.
func (s *Service) programEventSummaries(ctx context.Context, events []model.ProgramEvent, acceptLanguage string) ([]*openv1.ProgramEventSummary, error) {
	eventIDs := collectPublicProgramEventIDs(events)
	localizations, err := publiccontent.ResolveBatch(ctx, s.db, programEventLocalizationSpec, eventIDs, acceptLanguage)
	if err != nil {
		return nil, err
	}
	posterAssetsByEvent := s.listPosterAssets(ctx, events)
	eventTypeIDs := collectPublicProgramEventTypeIDs(events)
	eventTypes, err := loadPublicProgramEventTypes(ctx, s.db, eventTypeIDs)
	if err != nil {
		return nil, err
	}
	typeLocales, err := loadPublicProgramEventTypeLocales(ctx, s.db, eventTypeIDs)
	if err != nil {
		return nil, err
	}
	summaries := make([]*openv1.ProgramEventSummary, 0, len(events))
	for i := range events {
		event := &events[i]
		localization := localizations[event.ID]
		var eventType *openv1.ProgramEventType
		if event.TypeID != "" {
			typeRow, ok := eventTypes[event.TypeID]
			if !ok {
				return nil, gorm.ErrRecordNotFound
			}
			eventType, err = publicProgramEventTypeProto(typeRow, typeLocales[typeRow.ID], localization.DisplayedLocale)
			if err != nil {
				return nil, err
			}
		}
		summaries = append(summaries, toProtoProgramEventSummary(event, localization, posterAssetsByEvent[event.ID], eventType))
	}
	return summaries, nil
}

// listPosterAssets resolves the poster of each event of a page in two
// batches, falling back to one lookup per event or per File when a batch
// fails.
func (s *Service) listPosterAssets(ctx context.Context, events []model.ProgramEvent) map[string]*commonv1.AssetRef {
	posterFilesByEvent, posterQueryErr := loadProgramEventPosterFileIDs(ctx, s.db, collectPublicProgramEventIDs(events))
	posterAssetsByEvent := make(map[string]*commonv1.AssetRef, len(posterFilesByEvent))
	if posterQueryErr != nil {
		slog.ErrorContext(ctx, "Program Event poster batch lookup failed; resolving posters per event", "domain", "programevent", "operation", "load_poster_file_ids", failurecause.Attr(posterQueryErr))
		for i := range events {
			posterAssetsByEvent[events[i].ID] = loadProgramEventPosterAsset(ctx, s.db, s.assets, events[i].ID)
		}
		return posterAssetsByEvent
	}
	posterFileIDs := make([]string, 0, len(posterFilesByEvent))
	for _, fileID := range posterFilesByEvent {
		posterFileIDs = append(posterFileIDs, fileID)
	}
	assetsByFile := make(map[string]*commonv1.AssetRef)
	if len(posterFileIDs) > 0 {
		resolved, resolveErr := s.assets.ResolveReadyAssetsForSourceFiles(ctx, posterFileIDs, "poster", "image")
		if resolveErr == nil {
			assetsByFile = resolved
		} else {
			slog.ErrorContext(ctx, "Program Event poster batch asset resolution failed; resolving posters per file", "domain", "programevent", "operation", "resolve_poster_assets", failurecause.Attr(resolveErr))
			for _, fileID := range posterFileIDs {
				assetsByFile[fileID] = s.assets.ResolveReadyAssetForSourceFile(ctx, fileID, "poster", "image")
			}
		}
	}
	for eventID, fileID := range posterFilesByEvent {
		posterAssetsByEvent[eventID] = assetsByFile[fileID]
	}
	return posterAssetsByEvent
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

func (s *Service) toProtoProgramEvent(
	ctx context.Context,
	event *model.ProgramEvent,
	localization publiccontent.Selection,
	blockRead programEventPublicBlockRead,
) (*openv1.ProgramEvent, error) {
	document, err := contentblock.MaterializeSnapshotRichTextLocale(
		blockRead.Snapshot,
		localization.DisplayedLocale,
	)
	if err != nil {
		return nil, fmt.Errorf("materialize Program Event typed document: %w", err)
	}
	var variables sitevariable.Context
	if contentblock.HasSiteVariables(document) {
		variables, err = sitevariable.LoadRuntime(ctx, s.db)
		if err != nil {
			return nil, err
		}
	}
	projection, err := contentblock.MaterializeLocalizedRichTextDocument(
		ctx,
		document,
		&programEventPublicFileRenderResolver{db: s.db, assets: s.assets}, variables,
	)
	if err != nil {
		return nil, fmt.Errorf("render Program Event typed document: %w", err)
	}
	mapPlaceID := event.MapPlaceID
	if !event.LocationMode.UsesMapPlace() {
		mapPlaceID = nil
	}
	protoEvent := &openv1.ProgramEvent{
		Id:               event.ID,
		Status:           openProgramEventStatus(event.Status),
		Title:            event.Title,
		Slug:             &event.Slug,
		Summary:          localization.Summary,
		ContentHtml:      &projection.HTML,
		ContentText:      &projection.Text,
		TypeId:           event.TypeID,
		SeriesId:         event.SeriesID,
		SeriesOrder:      event.SeriesOrder,
		StartsAt:         timestamppb.New(event.StartsAt),
		EndsAt:           timestampProto(event.EndsAt),
		Timezone:         event.Timezone,
		AllDay:           event.AllDay,
		LocationMode:     openProgramEventLocationMode(event.LocationMode),
		MapPlaceId:       mapPlaceID,
		TicketUrl:        event.TicketURL,
		StreamUrl:        event.StreamURL,
		ExternalUrl:      event.ExternalURL,
		PublishedAt:      timestampProto(event.PublishedAt),
		UpdatedAt:        timestamppb.New(event.UpdatedAt),
		LocalizationInfo: publiccontent.ToProtoLocalizationInfo(localization),
		Document:         document,
		DocumentRevision: blockRead.Snapshot.Document.Revision.String(),
	}
	if posterAsset := loadProgramEventPosterAsset(ctx, s.db, s.assets, event.ID); posterAsset != nil {
		protoEvent.PosterAsset = posterAsset
	}
	if mapPlaceID != nil {
		if place, err := loadPublicProgramEventMapPlace(ctx, s.db, *mapPlaceID); err != nil {
			return nil, err
		} else {
			protoEvent.LocationPlace = place
		}
	}
	if event.TypeID != "" {
		eventType, err := loadPublicProgramEventType(ctx, s.db, event.TypeID, localization.DisplayedLocale)
		if err != nil {
			return nil, err
		}
		protoEvent.Type = eventType
	}
	protoEvent.Series, err = s.loadPublishedProgramEventSeries(ctx, event.SeriesID)
	if err != nil {
		return nil, err
	}
	if protoEvent.Artists, err = loadPublicProgramEventArtists(ctx, s.db, event.ID); err != nil {
		return nil, err
	}
	if protoEvent.Labels, err = loadPublicProgramEventLabels(ctx, s.db, event.ID); err != nil {
		return nil, err
	}
	if protoEvent.Clients, err = loadPublicProgramEventClients(ctx, s.db, event.ID); err != nil {
		return nil, err
	}
	if protoEvent.Credits, err = s.loadPublicProgramEventCredits(ctx, event.ID); err != nil {
		return nil, err
	}
	return protoEvent, nil
}

type programEventPublicBlockRead struct {
	DocumentID      uuid.UUID
	Snapshot        contentblock.Snapshot
	SourceLocale    string
	CompleteLocales []string
}

type programEventPublicFileRenderResolver struct {
	db     *gorm.DB
	assets Assets
}

func (r *programEventPublicFileRenderResolver) ResolveContentBlockFile(
	ctx context.Context,
	selector contentblock.FileRenderSelector,
) (contentblock.FileRenderTarget, error) {
	if r == nil || r.db == nil || r.assets == nil || selector.BlockID == uuid.Nil || selector.ReferencePath == "" || selector.FileID == uuid.Nil {
		return contentblock.FileRenderTarget{}, fmt.Errorf("invalid public Program Event Content Block File render selector")
	}
	var file struct {
		MIMEType string `gorm:"column:mime_type"`
	}
	result := r.db.WithContext(ctx).Raw(`
		SELECT file.mime_type
		FROM content_block_attachment AS attachment
		JOIN file ON file.id = attachment.file_id
		WHERE attachment.block_id = ? AND attachment.reference_path = ?
		  AND attachment.selector_kind = 'active' AND attachment.file_id = ?
		  AND file.delete_requested_at IS NULL
	`, selector.BlockID, selector.ReferencePath, selector.FileID).Scan(&file)
	if result.Error != nil {
		return contentblock.FileRenderTarget{}, fmt.Errorf("load exact public Program Event Content Block File: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return contentblock.FileRenderTarget{}, fmt.Errorf("exact public Program Event Content Block File reference does not exist")
	}
	target := contentblock.FileRenderTarget{MIMEType: file.MIMEType}
	if !strings.HasPrefix(file.MIMEType, "image/") {
		return target, nil
	}
	asset := r.assets.ResolveReadyAssetForSourceFile(ctx, selector.FileID.String(), "image")
	if asset == nil {
		return contentblock.FileRenderTarget{}, fmt.Errorf("public Program Event Content Block image asset is unavailable")
	}
	target.URL = asset.GetUrl()
	target.MIMEType = asset.GetMimeType()
	return target, nil
}

func (s *Service) loadProgramEventBlockRead(
	ctx context.Context,
	event *model.ProgramEvent,
) (programEventPublicBlockRead, error) {
	if event == nil || event.ContentDocumentID == nil || strings.TrimSpace(*event.ContentDocumentID) == "" {
		return programEventPublicBlockRead{}, domainerr.Precondition("Program Event content document is not initialized")
	}
	documentID, err := uuid.Parse(*event.ContentDocumentID)
	if err != nil {
		return programEventPublicBlockRead{}, fmt.Errorf("invalid Program Event content_document_id: %w", err)
	}
	if strings.TrimSpace(event.SourceLocale) == "" {
		return programEventPublicBlockRead{}, domainerr.Precondition("Program Event source locale is not initialized")
	}
	snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, s.db, documentID, event.SourceLocale)
	if err != nil {
		return programEventPublicBlockRead{}, fmt.Errorf("load Program Event content document: %w", err)
	}
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return programEventPublicBlockRead{}, fmt.Errorf("restore Program Event typed document: %w", err)
	}
	completeLocales, err := contentblock.CompleteRichTextDocumentLocales(document)
	if err != nil {
		return programEventPublicBlockRead{}, fmt.Errorf("validate Program Event typed document locales: %w", err)
	}
	return programEventPublicBlockRead{
		DocumentID:      documentID,
		Snapshot:        snapshot,
		SourceLocale:    event.SourceLocale,
		CompleteLocales: completeLocales,
	}, nil
}

func (s *Service) loadPublishedProgramEventSeries(
	ctx context.Context,
	seriesID *string,
) (*openv1.ProgramEventSeries, error) {
	if seriesID == nil {
		return nil, nil
	}
	var series model.ProgramEventSeries
	err := s.db.WithContext(ctx).
		First(
			&series,
			"id = ? AND status = ?",
			*seriesID,
			model.ProgramEventStatusPublished,
		).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toProtoProgramEventSeries(ctx, s.assets, &series), nil
}

func toProtoProgramEventSummary(
	event *model.ProgramEvent,
	localization publiccontent.Selection,
	posterAsset *commonv1.AssetRef,
	eventType *openv1.ProgramEventType,
) *openv1.ProgramEventSummary {
	mapPlaceID := event.MapPlaceID
	if !event.LocationMode.UsesMapPlace() {
		mapPlaceID = nil
	}
	summary := &openv1.ProgramEventSummary{
		Id:               event.ID,
		Status:           openProgramEventStatus(event.Status),
		Title:            event.Title,
		Slug:             &event.Slug,
		Summary:          localization.Summary,
		TypeId:           event.TypeID,
		SeriesId:         event.SeriesID,
		SeriesOrder:      event.SeriesOrder,
		StartsAt:         timestamppb.New(event.StartsAt),
		EndsAt:           timestampProto(event.EndsAt),
		Timezone:         event.Timezone,
		AllDay:           event.AllDay,
		LocationMode:     openProgramEventLocationMode(event.LocationMode),
		MapPlaceId:       mapPlaceID,
		PublishedAt:      timestampProto(event.PublishedAt),
		UpdatedAt:        timestamppb.New(event.UpdatedAt),
		LocalizationInfo: publiccontent.ToProtoLocalizationInfo(localization),
	}
	if posterAsset != nil {
		summary.PosterAsset = posterAsset
	}
	if eventType != nil {
		summary.Type = eventType
	}
	return summary
}

func resolveProgramEventLocalization(
	ctx context.Context,
	db *gorm.DB,
	acceptLanguage string,
	eventID string,
	sourceLocale string,
	completeDocumentLocales []string,
) (publiccontent.Selection, error) {
	settings, settingsErr := translation.LoadRuntimeSettings(ctx, db)
	if settingsErr != nil {
		slog.ErrorContext(ctx, "Translation runtime settings lookup failed; using default Program Event localization settings", "domain", "programevent", "operation", "load_localization_settings", failurecause.Attr(settingsErr), "event_id", eventID)
		settings = translation.DefaultRuntimeSettings()
	}
	sourceLocale = normalizeSourceLocaleWithDefault(sourceLocale, settings.DefaultLocale)
	selection, err := publiccontent.ResolveWithPolicy(
		ctx, db, programEventLocalizationSpec, eventID, acceptLanguage, settings,
	)
	if err != nil {
		return publiccontent.Selection{}, err
	}
	complete := make(map[string]struct{}, len(completeDocumentLocales))
	for _, locale := range completeDocumentLocales {
		complete[normalizeSourceLocaleWithDefault(locale, settings.DefaultLocale)] = struct{}{}
	}
	if _, ok := complete[sourceLocale]; !ok {
		return publiccontent.Selection{}, errors.New("program Event source locale Block overlay is incomplete")
	}
	if selection.DisplayedLocale != sourceLocale {
		if _, ok := complete[selection.DisplayedLocale]; !ok {
			requestedLocale := selection.RequestedLocale
			// Re-resolving with the default locale yields its row only when it is
			// stored; otherwise ResolveWithPolicy already falls back to source.
			fallbackLocale := publiccontent.DocumentFallbackLocale(selection, complete, settings)
			if fallbackLocale == selection.SourceLocale {
				fallbackLocale = sourceLocale
			}
			selection, err = publiccontent.ResolveWithPolicy(
				ctx, db, programEventLocalizationSpec, eventID, fallbackLocale, settings,
			)
			if err != nil {
				return publiccontent.Selection{}, err
			}
			selection.RequestedLocale = requestedLocale
			selection.IsFallback = true
			selection.FallbackReason = openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_SOURCE
		}
	}
	available, availableErr := publiccontent.AvailableLocales(
		ctx, db, programEventLocalizationSpec, eventID, sourceLocale, settings,
	)
	if availableErr == nil {
		filtered := make([]string, 0, len(available))
		for _, locale := range available {
			if locale == sourceLocale {
				filtered = append(filtered, locale)
				continue
			}
			if _, ok := complete[locale]; ok {
				filtered = append(filtered, locale)
			}
		}
		selection.AvailableLocales = filtered
	} else {
		slog.ErrorContext(ctx, "Program Event available locales lookup failed; serving without available locales", "domain", "programevent", "operation", "load_available_locales", failurecause.Attr(availableErr), "event_id", eventID)
	}
	return selection, nil
}

func timestampProto(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}
