package public

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/sitevariable"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
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

type ProgramEventService struct {
	openv1connect.UnimplementedProgramEventServiceHandler
	db            *gorm.DB
	assets        Assets
	contentBlocks *contentblock.Store
	files         MediaHydrator
	creditMembers CreditMemberSummaries
}

// MediaHydrator resolves request-scoped delivery for authorized Program Event Block media.
type MediaHydrator interface {
	HydrateAuthorizedContentBlockMedia(context.Context, []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error)
}

// CreditMemberSummaries projects public Member-owned identity details for Program Event credits.
type CreditMemberSummaries interface {
	LoadPublicCreditMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
}

type ProgramEventServiceOption func(*ProgramEventService)

func WithProgramEventContentBlockStore(store *contentblock.Store) ProgramEventServiceOption {
	return func(service *ProgramEventService) {
		service.contentBlocks = store
	}
}

func WithProgramEventFileService(files MediaHydrator) ProgramEventServiceOption {
	return func(service *ProgramEventService) {
		service.files = files
	}
}

func NewProgramEventService(
	db *gorm.DB,
	assets Assets,
	creditMembers CreditMemberSummaries,
	options ...ProgramEventServiceOption,
) *ProgramEventService {
	if creditMembers == nil {
		panic("Program Event public credit Member summaries are required")
	}
	if assets == nil {
		panic("Program Event public assets are required")
	}
	service := &ProgramEventService{db: db, assets: assets, creditMembers: creditMembers}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func (s *ProgramEventService) Get(
	ctx context.Context,
	req *connect.Request[openv1.GetProgramEventRequest],
) (*connect.Response[openv1.GetProgramEventResponse], error) {
	slugOrID := strings.TrimSpace(req.Msg.Slug)
	if slugOrID == "" {
		return nil, errs.Required("slug")
	}
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Program Event content Block store is not configured")
	}
	if s.files == nil {
		return nil, errs.InternalMsg("Program Event media resolver is not configured")
	}

	var protoEvent *openv1.ProgramEvent
	var media []*contentv1.ContentBlockMediaItem
	var mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var event model.ProgramEvent
		query := tx.
			Model(&model.ProgramEvent{}).
			Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
			Where("status IN ?", publicProgramEventStatuses())
		if _, parseErr := uuid.Parse(slugOrID); parseErr == nil {
			query = query.Where("id = ?", slugOrID)
		} else {
			query = query.Where("slug = ?", slugOrID)
		}
		if err := query.First(&event).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFoundMsg("program event not found")
			}
			return errs.Internal(err)
		}

		scoped := *s
		scoped.db = tx
		blockRead, err := scoped.loadProgramEventBlockRead(ctx, &event)
		if err != nil {
			return err
		}
		localization, err := resolveProgramEventLocalization(
			ctx,
			tx,
			req.Header().Get("Accept-Language"),
			event.ID,
			blockRead.SourceLocale,
			blockRead.CompleteLocales,
		)
		if err != nil {
			return err
		}
		protoEvent, err = scoped.toProtoProgramEvent(ctx, &event, localization, blockRead)
		if err != nil {
			return err
		}
		media, err = contentblock.LoadMediaReferences(ctx, tx, blockRead.DocumentID)
		mediaAuthorization = mediaasset.ContentDownloadOwnerAuthorization{
			ResourceType: "program_event", ResourceID: event.ID, Status: event.Status,
			DocumentID: blockRead.DocumentID.String(), Mode: mediaasset.ContentDownloadOwnerAccessPublic,
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	media, err = s.files.HydrateAuthorizedContentBlockMedia(
		mediaasset.WithContentDownloadOwnerAuthorization(ctx, mediaAuthorization), media,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&openv1.GetProgramEventResponse{
		Event:      protoEvent,
		BlockMedia: media,
	}), nil
}

func (s *ProgramEventService) List(
	ctx context.Context,
	req *connect.Request[openv1.ListProgramEventsRequest],
) (*connect.Response[openv1.ListProgramEventsResponse], error) {
	query := s.db.WithContext(ctx).
		Model(&model.ProgramEvent{}).
		Where("status IN ?", publicProgramEventStatuses())

	var err error
	query, err = applyPublicProgramEventFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	query, err = applyProgramEventPublicSort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}
	pagination := queryutil.ExtractPagination(req.Msg.Pagination, queryutil.DefaultPaginationConfig)
	query = pagination.Apply(query)

	var events []model.ProgramEvent
	if err := query.Find(&events).Error; err != nil {
		return nil, errs.Internal(err)
	}

	localizations, err := publiccontent.ResolveBatch(
		ctx,
		s.db,
		programEventLocalizationSpec,
		collectPublicProgramEventIDs(events),
		req.Header().Get("Accept-Language"),
	)
	if err != nil {
		return nil, errs.Internal(err)
	}
	posterFilesByEvent, posterQueryErr := loadProgramEventPosterFileIDs(ctx, s.db, collectPublicProgramEventIDs(events))
	posterAssetsByEvent := make(map[string]*commonv1.AssetRef, len(posterFilesByEvent))
	if posterQueryErr != nil {
		slog.ErrorContext(ctx, "Program Event poster batch lookup failed; resolving posters per event", "domain", "programevent", "operation", "load_poster_file_ids", failurecause.Attr(posterQueryErr))
		for i := range events {
			posterAssetsByEvent[events[i].ID] = loadProgramEventPosterAsset(ctx, s.db, s.assets, events[i].ID)
		}
	} else {
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
	}
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
		posterAsset := posterAssetsByEvent[event.ID]
		var eventType *openv1.ProgramEventType
		if event.TypeID != "" {
			typeRow, ok := eventTypes[event.TypeID]
			if !ok {
				return nil, errs.Internal(gorm.ErrRecordNotFound)
			}
			eventType, err = publicProgramEventTypeProto(typeRow, typeLocales[typeRow.ID], localization.DisplayedLocale)
			if err != nil {
				return nil, err
			}
		}
		item := s.toProtoProgramEventSummary(event, localization, posterAsset, eventType)
		summaries = append(summaries, item)
	}

	return connect.NewResponse(&openv1.ListProgramEventsResponse{
		Events:     summaries,
		Pagination: pagination.BuildResponse(total),
	}), nil
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
			return nil, errs.InvalidSortField(sort.GetField())
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
	return "location_mode = '" + managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_TBA.String() + "' DESC"
}

func (s *ProgramEventService) toProtoProgramEvent(
	ctx context.Context,
	event *model.ProgramEvent,
	localization publiccontent.Selection,
	blockRead programEventPublicBlockRead,
) (*openv1.ProgramEvent, error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Program Event content Block store is not configured")
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(
		blockRead.Snapshot,
		localization.DisplayedLocale,
	)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("materialize Program Event typed document: %w", err))
	}
	var variables sitevariable.Context
	if contentblock.HasSiteVariables(document) {
		variables, err = sitevariable.LoadRuntime(ctx, s.db)
		if err != nil {
			return nil, errs.Internal(err)
		}
	}
	projection, err := contentblock.MaterializeLocalizedRichTextDocument(
		ctx,
		document,
		&programEventPublicFileRenderResolver{db: s.db, assets: s.assets}, variables,
	)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("render Program Event typed document: %w", err))
	}
	mapPlaceID := event.MapPlaceID
	if !publicProgramEventLocationModeUsesMapPlace(event.LocationMode) {
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
	protoEvent.Series, err = s.loadPublishedProgramEventSeries(
		ctx, event.SeriesID, localization.DisplayedLocale,
	)
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

func (s *ProgramEventService) loadProgramEventBlockRead(
	ctx context.Context,
	event *model.ProgramEvent,
) (programEventPublicBlockRead, error) {
	if event == nil || event.ContentDocumentID == nil || strings.TrimSpace(*event.ContentDocumentID) == "" {
		return programEventPublicBlockRead{}, errs.FailedPrecondition("Program Event content document is not initialized")
	}
	documentID, err := uuid.Parse(*event.ContentDocumentID)
	if err != nil {
		return programEventPublicBlockRead{}, errs.Internal(fmt.Errorf("invalid Program Event content_document_id: %w", err))
	}
	if strings.TrimSpace(event.SourceLocale) == "" {
		return programEventPublicBlockRead{}, errs.FailedPrecondition("Program Event source locale is not initialized")
	}
	snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, s.db, documentID, event.SourceLocale)
	if err != nil {
		return programEventPublicBlockRead{}, errs.Internal(fmt.Errorf("load Program Event content document: %w", err))
	}
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return programEventPublicBlockRead{}, errs.Internal(fmt.Errorf("restore Program Event typed document: %w", err))
	}
	completeLocales, err := contentblock.CompleteRichTextDocumentLocales(document)
	if err != nil {
		return programEventPublicBlockRead{}, errs.Internal(fmt.Errorf("validate Program Event typed document locales: %w", err))
	}
	return programEventPublicBlockRead{
		DocumentID:      documentID,
		Snapshot:        snapshot,
		SourceLocale:    event.SourceLocale,
		CompleteLocales: completeLocales,
	}, nil
}

func (s *ProgramEventService) loadPublishedProgramEventSeries(
	ctx context.Context,
	seriesID *string,
	locale string,
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
			managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String(),
		).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, errs.Internal(err)
	}
	service := &ProgramEventSeriesService{db: s.db, assets: s.assets}
	return service.toProtoProgramEventSeries(ctx, &series, locale)
}

func (s *ProgramEventService) toProtoProgramEventSummary(
	event *model.ProgramEvent,
	localization publiccontent.Selection,
	posterAsset *commonv1.AssetRef,
	eventType *openv1.ProgramEventType,
) *openv1.ProgramEventSummary {
	mapPlaceID := event.MapPlaceID
	if !publicProgramEventLocationModeUsesMapPlace(event.LocationMode) {
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
		return publiccontent.Selection{}, errs.Internal(err)
	}
	complete := make(map[string]struct{}, len(completeDocumentLocales))
	for _, locale := range completeDocumentLocales {
		complete[normalizeSourceLocaleWithDefault(locale, settings.DefaultLocale)] = struct{}{}
	}
	if _, ok := complete[sourceLocale]; !ok {
		return publiccontent.Selection{}, errs.InternalMsg("Program Event source locale Block overlay is incomplete")
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
				return publiccontent.Selection{}, errs.Internal(err)
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
