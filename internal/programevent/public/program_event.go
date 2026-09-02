package public

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	programevent "github.com/echovisionlab/geul-api/internal/programevent"
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

var programEventPublicFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:       queryutil.TypeText,
			AllowedOps: queryutil.SearchOps,
			SearchColumns: []string{
				"program_event.title",
				"COALESCE((SELECT pet.summary FROM program_event_translation AS pet WHERE pet.entity_id = program_event.id AND pet.locale = program_event.source_locale LIMIT 1), '')",
			},
		},
		"type_id": {
			Column:     "type_id",
			Type:       queryutil.TypeID,
			AllowedOps: []commonv1.FilterOp{commonv1.FilterOp_FILTER_OP_EQ, commonv1.FilterOp_FILTER_OP_IN},
			IsFK:       true,
		},
		"location_mode": {
			Column:     "location_mode",
			Type:       queryutil.TypeEnum,
			AllowedOps: []commonv1.FilterOp{commonv1.FilterOp_FILTER_OP_EQ, commonv1.FilterOp_FILTER_OP_IN},
			EnumValues: []string{
				managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_MAP_PLACE.String(),
				managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE.String(),
				managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_HYBRID.String(),
				managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_TBA.String(),
			},
		},
		"series_id": {
			Column:     "series_id",
			Type:       queryutil.TypeID,
			AllowedOps: []commonv1.FilterOp{commonv1.FilterOp_FILTER_OP_EQ, commonv1.FilterOp_FILTER_OP_IN},
			IsFK:       true,
		},
		"map_place_id": {
			Column: "map_place_id",
			Type:   queryutil.TypeID,
			AllowedOps: []commonv1.FilterOp{
				commonv1.FilterOp_FILTER_OP_EQ,
				commonv1.FilterOp_FILTER_OP_IN,
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

type ProgramEventSeriesService struct {
	openv1connect.UnimplementedProgramEventSeriesServiceHandler
	db     *gorm.DB
	assets Assets
}

func NewProgramEventSeriesService(db *gorm.DB, assets Assets) *ProgramEventSeriesService {
	if assets == nil {
		panic("Program Event public assets are required")
	}
	return &ProgramEventSeriesService{db: db, assets: assets}
}

type ProgramEventTypeService struct {
	openv1connect.UnimplementedProgramEventTypeServiceHandler
	db *gorm.DB
}

func NewProgramEventTypeService(db *gorm.DB) *ProgramEventTypeService {
	return &ProgramEventTypeService{db: db}
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
			Clauses(clause.Locking{Strength: "SHARE"}).
			Where("status IN ?", publicProgramEventStatuses())
		if _, parseErr := uuid.Parse(slugOrID); parseErr == nil {
			query = query.Where("id = ?", slugOrID)
		} else {
			query = query.Where("slug = ?", slugOrID)
		}
		if err := query.First(&event).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
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
		media, err = programevent.LoadContentBlockMediaReferences(ctx, tx, blockRead.DocumentID)
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

	summaries := make([]*openv1.ProgramEventSummary, 0, len(events))
	for i := range events {
		item, err := s.toProtoProgramEventSummary(ctx, &events[i], localizations[events[i].ID])
		if err != nil {
			return nil, err
		}
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

func (s *ProgramEventSeriesService) Get(
	ctx context.Context,
	req *connect.Request[openv1.GetProgramEventSeriesRequest],
) (*connect.Response[openv1.GetProgramEventSeriesResponse], error) {
	slugOrID := strings.TrimSpace(req.Msg.Slug)
	if slugOrID == "" {
		return nil, errs.Required("slug")
	}

	var series model.ProgramEventSeries
	query := s.db.WithContext(ctx).
		Model(&model.ProgramEventSeries{}).
		Where("status = ?", managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String())
	if _, parseErr := uuid.Parse(slugOrID); parseErr == nil {
		query = query.Where("id = ?", slugOrID)
	} else {
		query = query.Where("slug = ?", slugOrID)
	}
	if err := query.First(&series).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("program event series not found")
		}
		return nil, errs.Internal(err)
	}
	protoSeries, err := s.toProtoProgramEventSeries(ctx, &series, req.Header().Get("Accept-Language"))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&openv1.GetProgramEventSeriesResponse{Series: protoSeries}), nil
}

func (s *ProgramEventSeriesService) List(
	ctx context.Context,
	req *connect.Request[openv1.ListProgramEventSeriesRequest],
) (*connect.Response[openv1.ListProgramEventSeriesResponse], error) {
	query := s.db.WithContext(ctx).
		Model(&model.ProgramEventSeries{}).
		Where("status = ?", managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String())
	var err error
	query, err = applySimpleProgramEventSeriesFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}
	query = query.Order("updated_at DESC")
	pagination := queryutil.ExtractPagination(req.Msg.Pagination, queryutil.DefaultPaginationConfig)
	query = pagination.Apply(query)

	var rows []model.ProgramEventSeries
	if err := query.Find(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	result, err := s.toProtoProgramEventSeriesList(ctx, rows)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&openv1.ListProgramEventSeriesResponse{
		Series:     result,
		Pagination: pagination.BuildResponse(total),
	}), nil
}

func (s *ProgramEventTypeService) List(
	ctx context.Context,
	req *connect.Request[openv1.ListProgramEventTypesRequest],
) (*connect.Response[openv1.ListProgramEventTypesResponse], error) {
	query := s.db.WithContext(ctx).
		Model(&model.ProgramEventType{}).
		Where("status = ?", openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE.String())
	var err error
	query, err = applySimpleProgramEventTypeFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}
	query = query.Order("sort_order ASC, slug ASC")
	pagination := queryutil.ExtractPagination(req.Msg.Pagination, queryutil.DefaultPaginationConfig)
	query = pagination.Apply(query)

	var rows []model.ProgramEventType
	if err := query.Find(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	result := make([]*openv1.ProgramEventType, 0, len(rows))
	for i := range rows {
		item, err := loadPublicProgramEventType(ctx, s.db, rows[i].ID, req.Header().Get("Accept-Language"))
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return connect.NewResponse(&openv1.ListProgramEventTypesResponse{
		Types:      result,
		Pagination: pagination.BuildResponse(total),
	}), nil
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
	projection, err := contentblock.MaterializeLocalizedRichTextDocument(
		ctx,
		document,
		&programEventPublicFileRenderResolver{db: s.db, assets: s.assets},
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
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, errs.Internal(err)
	}
	service := &ProgramEventSeriesService{db: s.db, assets: s.assets}
	return service.toProtoProgramEventSeries(ctx, &series, locale)
}

func (s *ProgramEventService) toProtoProgramEventSummary(
	ctx context.Context,
	event *model.ProgramEvent,
	localization publiccontent.Selection,
) (*openv1.ProgramEventSummary, error) {
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
	if posterAsset := loadProgramEventPosterAsset(ctx, s.db, s.assets, event.ID); posterAsset != nil {
		summary.PosterAsset = posterAsset
	}
	if event.TypeID != "" {
		eventType, err := loadPublicProgramEventType(ctx, s.db, event.TypeID, localization.DisplayedLocale)
		if err != nil {
			return nil, err
		}
		summary.Type = eventType
	}
	return summary, nil
}

func (s *ProgramEventSeriesService) toProtoProgramEventSeries(
	ctx context.Context,
	series *model.ProgramEventSeries,
	_ string,
) (*openv1.ProgramEventSeries, error) {
	result := openProgramEventSeriesProto(series)
	if posterAsset := loadProgramEventFileAsset(ctx, s.assets, series.PosterFileID); posterAsset != nil {
		result.PosterAsset = posterAsset
	}
	return result, nil
}

func (s *ProgramEventSeriesService) toProtoProgramEventSeriesList(
	ctx context.Context,
	series []model.ProgramEventSeries,
) ([]*openv1.ProgramEventSeries, error) {
	fileIDs := make([]string, 0, len(series))
	for i := range series {
		if series[i].PosterFileID != nil {
			fileIDs = append(fileIDs, *series[i].PosterFileID)
		}
	}
	assetsByFile, err := s.assets.ResolveReadyAssetsForSourceFiles(ctx, fileIDs, "image", "poster", "map_image")
	if err != nil {
		return nil, errs.Internal(err)
	}

	result := make([]*openv1.ProgramEventSeries, 0, len(series))
	for i := range series {
		item := openProgramEventSeriesProto(&series[i])
		if series[i].PosterFileID != nil {
			item.PosterAsset = assetsByFile[*series[i].PosterFileID]
		}
		result = append(result, item)
	}
	return result, nil
}

func openProgramEventSeriesProto(series *model.ProgramEventSeries) *openv1.ProgramEventSeries {
	return &openv1.ProgramEventSeries{
		Id:          series.ID,
		Status:      openProgramEventSeriesStatus(series.Status),
		Title:       series.Title,
		Slug:        series.Slug,
		Summary:     series.Summary,
		Description: series.Description,
	}
}

func applyPublicProgramEventFilters(query *gorm.DB, filters []*commonv1.FilterSpec) (*gorm.DB, error) {
	remaining := make([]*commonv1.FilterSpec, 0, len(filters))
	for _, filter := range filters {
		if filter == nil {
			continue
		}
		switch filter.GetField() {
		case "status":
			q, err := applyPublicProgramEventStatusFilter(query, filter)
			if err != nil {
				return nil, err
			}
			query = q
		case "time_window":
			q, err := applyProgramEventTimeWindowFilter(query, filter)
			if err != nil {
				return nil, err
			}
			query = q
		case "type_slug":
			q, err := applySlugSubqueryFilter(query, filter, "type_id", "program_event_type", "slug", "id")
			if err != nil {
				return nil, err
			}
			query = q
		case "series_slug":
			q, err := applySlugSubqueryFilter(query, filter, "series_id", "program_event_series", "slug", "id")
			if err != nil {
				return nil, err
			}
			query = q
		case "artist_id":
			q, err := applyRelationIDFilter(query, filter, "program_event_artist", "artist_id")
			if err != nil {
				return nil, err
			}
			query = q
		case "label_id":
			q, err := applyRelationIDFilter(query, filter, "program_event_label", "label_id")
			if err != nil {
				return nil, err
			}
			query = q
		case "client_id":
			q, err := applyRelationIDFilter(query, filter, "program_event_client", "client_id")
			if err != nil {
				return nil, err
			}
			query = q
		default:
			remaining = append(remaining, filter)
		}
	}
	return programEventPublicFilterConfig.ApplyFilters(query, remaining)
}

func applyPublicProgramEventStatusFilter(query *gorm.DB, filter *commonv1.FilterSpec) (*gorm.DB, error) {
	switch filter.GetOp() {
	case commonv1.FilterOp_FILTER_OP_EQ:
		if isPublicProgramEventStatus(filter.GetValue()) {
			return query.Where("status = ?", filter.GetValue()), nil
		}
		return query.Where("1 = 0"), nil
	case commonv1.FilterOp_FILTER_OP_IN:
		statuses := make([]string, 0, len(filter.GetValues()))
		for _, value := range filter.GetValues() {
			if isPublicProgramEventStatus(value) {
				statuses = append(statuses, value)
			}
		}
		if len(statuses) == 0 {
			return query.Where("1 = 0"), nil
		}
		return query.Where("status IN ?", statuses), nil
	default:
		return nil, errs.InvalidFilterOp(filter.GetField(), filter.GetOp().String())
	}
}

func applyProgramEventTimeWindowFilter(query *gorm.DB, filter *commonv1.FilterSpec) (*gorm.DB, error) {
	if filter.GetOp() != commonv1.FilterOp_FILTER_OP_EQ {
		return nil, errs.InvalidFilterOp(filter.GetField(), filter.GetOp().String())
	}
	now := time.Now().UTC()
	switch strings.TrimSpace(strings.ToLower(filter.GetValue())) {
	case "", "all":
		return query, nil
	case "upcoming":
		return query.Where("starts_at >= ?", now), nil
	case "past":
		return query.Where("COALESCE(ends_at, starts_at) < ?", now), nil
	case "current":
		return query.Where("starts_at <= ? AND COALESCE(ends_at, starts_at) >= ?", now, now), nil
	default:
		return nil, errs.InvalidArgument("time_window", "must be one of upcoming, past, current, all")
	}
}

func applySlugSubqueryFilter(query *gorm.DB, filter *commonv1.FilterSpec, eventColumn string, tableName string, slugColumn string, idColumn string) (*gorm.DB, error) {
	switch filter.GetOp() {
	case commonv1.FilterOp_FILTER_OP_EQ:
		return query.Where(eventColumn+" IN (SELECT "+idColumn+" FROM "+tableName+" WHERE "+slugColumn+" = ?)", filter.GetValue()), nil
	case commonv1.FilterOp_FILTER_OP_IN:
		return query.Where(eventColumn+" IN (SELECT "+idColumn+" FROM "+tableName+" WHERE "+slugColumn+" IN ?)", filter.GetValues()), nil
	default:
		return nil, errs.InvalidFilterOp(filter.GetField(), filter.GetOp().String())
	}
}

func applyRelationIDFilter(query *gorm.DB, filter *commonv1.FilterSpec, tableName string, relationColumn string) (*gorm.DB, error) {
	switch filter.GetOp() {
	case commonv1.FilterOp_FILTER_OP_EQ:
		return query.Where("id IN (SELECT event_id FROM "+tableName+" WHERE "+relationColumn+" = ?)", filter.GetValue()), nil
	case commonv1.FilterOp_FILTER_OP_IN:
		return query.Where("id IN (SELECT event_id FROM "+tableName+" WHERE "+relationColumn+" IN ?)", filter.GetValues()), nil
	default:
		return nil, errs.InvalidFilterOp(filter.GetField(), filter.GetOp().String())
	}
}

func applySimpleProgramEventSeriesFilters(query *gorm.DB, filters []*commonv1.FilterSpec) (*gorm.DB, error) {
	for _, filter := range filters {
		if filter == nil {
			continue
		}
		switch filter.GetField() {
		case "search":
			if filter.GetOp() != commonv1.FilterOp_FILTER_OP_LIKE && filter.GetOp() != commonv1.FilterOp_FILTER_OP_ILIKE {
				return nil, errs.InvalidFilterOp(filter.GetField(), filter.GetOp().String())
			}
			pattern := "%" + filter.GetValue() + "%"
			query = query.Where("title ILIKE ? OR summary ILIKE ?", pattern, pattern)
		default:
			return nil, errs.InvalidArgument("filter", "unsupported program event series filter")
		}
	}
	return query, nil
}

func applySimpleProgramEventTypeFilters(query *gorm.DB, filters []*commonv1.FilterSpec) (*gorm.DB, error) {
	for _, filter := range filters {
		if filter == nil {
			continue
		}
		switch filter.GetField() {
		case "search":
			if filter.GetOp() != commonv1.FilterOp_FILTER_OP_LIKE && filter.GetOp() != commonv1.FilterOp_FILTER_OP_ILIKE {
				return nil, errs.InvalidFilterOp(filter.GetField(), filter.GetOp().String())
			}
			query = query.Where("id IN (SELECT type_id FROM program_event_type_locale WHERE name ILIKE ? OR description ILIKE ?)", "%"+filter.GetValue()+"%", "%"+filter.GetValue()+"%")
		default:
			return nil, errs.InvalidArgument("filter", "unsupported program event type filter")
		}
	}
	return query, nil
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
			selection, err = publiccontent.ResolveWithPolicy(
				ctx, db, programEventLocalizationSpec, eventID, sourceLocale, settings,
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
	}
	return selection, nil
}

func loadPublicProgramEventType(ctx context.Context, db *gorm.DB, typeID string, acceptLanguage string) (*openv1.ProgramEventType, error) {
	var eventType model.ProgramEventType
	if err := db.WithContext(ctx).First(&eventType, "id = ?", typeID).Error; err != nil {
		return nil, errs.Internal(err)
	}
	localeRow, err := selectPublicProgramEventTypeLocale(ctx, db, eventType.ID, acceptLanguage)
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
	Locale      string  `gorm:"column:locale"`
	Name        string  `gorm:"column:name"`
	Description *string `gorm:"column:description"`
}

func selectPublicProgramEventTypeLocale(ctx context.Context, db *gorm.DB, typeID string, acceptLanguage string) (publicProgramEventTypeLocaleRow, error) {
	var rows []publicProgramEventTypeLocaleRow
	if err := db.WithContext(ctx).
		Table("program_event_type_locale").
		Select("locale, name, description").
		Where("type_id = ?", typeID).
		Order("locale ASC").
		Find(&rows).Error; err != nil {
		return publicProgramEventTypeLocaleRow{}, errs.Internal(err)
	}
	if len(rows) == 0 {
		return publicProgramEventTypeLocaleRow{}, errs.NotFound("program event type locale", typeID)
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
		return nil, errs.Internal(err)
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
		return nil, errs.Internal(err)
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
		return nil, errs.Internal(err)
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
	ID          string  `gorm:"column:id"`
	Display     *string `gorm:"column:display_name"`
	CreditRole  *string `gorm:"column:credit_role"`
	Description *string `gorm:"column:description"`
	SortOrder   int32   `gorm:"column:sort_order"`
	ArtistID    *string `gorm:"column:artist_id"`
	ArtistName  *string `gorm:"column:artist_name"`
	ArtistSlug  *string `gorm:"column:artist_slug"`
	MemberID    *string `gorm:"column:member_id"`
}

func (s *ProgramEventService) loadPublicProgramEventCredits(ctx context.Context, eventID string) ([]*openv1.ProgramEventCredit, error) {
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
			pec.member_id
		`).
		Joins("LEFT JOIN artist AS a ON a.id = pec.artist_id AND a.status = ?", managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String()).
		Where("pec.event_id = ?", eventID).
		Order("pec.sort_order ASC, pec.id ASC").
		Scan(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	memberIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.MemberID != nil {
			memberIDs = append(memberIDs, *row.MemberID)
		}
	}
	members, err := s.creditMembers.LoadPublicCreditMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, errs.Internal(err)
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

func timestampProto(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}

func openProgramEventStatus(status string) openv1.ProgramEventStatus {
	if value, ok := openv1.ProgramEventStatus_value[status]; ok {
		return openv1.ProgramEventStatus(value)
	}
	return openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_UNSPECIFIED
}

func publicProgramEventStatuses() []string {
	return []string{
		openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String(),
		openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String(),
	}
}

func isPublicProgramEventStatus(status string) bool {
	return status == openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String() ||
		status == openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String()
}

func openProgramEventSeriesStatus(status string) openv1.ProgramEventSeriesStatus {
	switch status {
	case managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String():
		return openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED
	case managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT.String():
		return openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_DRAFT
	default:
		return openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_UNSPECIFIED
	}
}

func openProgramEventLocationMode(mode string) openv1.ProgramEventLocationMode {
	if value, ok := openv1.ProgramEventLocationMode_value[mode]; ok {
		return openv1.ProgramEventLocationMode(value)
	}
	return openv1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_UNSPECIFIED
}

func publicProgramEventLocationModeUsesMapPlace(mode string) bool {
	return mode == managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_MAP_PLACE.String() ||
		mode == managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_HYBRID.String()
}

func openProgramEventTypeStatus(status string) openv1.ProgramEventTypeStatus {
	if value, ok := openv1.ProgramEventTypeStatus_value[status]; ok {
		return openv1.ProgramEventTypeStatus(value)
	}
	return openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_UNSPECIFIED
}
