package public

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/memberref"
	optional "github.com/echovisionlab/geul-api/internal/optional"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/google/uuid"

	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/translation"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// GetProgramEventQuery names a published or archived event by slug or ID.
type GetProgramEventQuery struct {
	SlugOrID       string
	AcceptLanguage string
}

// TypeEntry is the type of a public event, named in the displayed locale.
type TypeEntry struct {
	programevent.Type
	Name        string
	Description *string
}

// SeriesEntry is a published series with its ready poster.
type SeriesEntry struct {
	programevent.Series
	PosterAsset *mediaasset.Reference
}

// CreditArtist is the published artist a public credit names.
type CreditArtist struct {
	ID         string
	Name       string
	Slug       *string
	ImageAsset *mediaasset.Reference
}

// Credit is one credit of a public event with its projections.
type Credit struct {
	ID          string
	DisplayName *string
	CreditRole  *string
	Description *string
	SortOrder   int32
	Artist      *CreditArtist
	Member      *memberref.Summary
}

// EventDetail is a public event in its displayed locale. MapPlaceID is set
// only for a location mode that names a Map Place.
type EventDetail struct {
	programevent.Event
	Localization     publiccontent.Selection
	Document         *contentv1.LocalizedRichTextDocument
	ContentHTML      string
	ContentText      string
	DocumentRevision string
	PosterAsset      *mediaasset.Reference
	LocationPlace    *MapPlace
	Type             *TypeEntry
	Series           *SeriesEntry
	Artists          []Participant
	Labels           []Participant
	Clients          []Client
	Credits          []Credit
}

// PublicEvent is a public event in its displayed locale with the delivery
// of its Block media.
type PublicEvent struct {
	Event      EventDetail
	BlockMedia []*contentv1.ContentBlockMediaItem
}

// GetProgramEvent returns a published or archived event in the locale
// AcceptLanguage selects.
func (s *Service) GetProgramEvent(ctx context.Context, query GetProgramEventQuery) (PublicEvent, error) {
	slugOrID := strings.TrimSpace(query.SlugOrID)
	if slugOrID == "" {
		return PublicEvent{}, domainerr.Required("slug")
	}
	var detail EventDetail
	var media []*contentv1.ContentBlockMediaItem
	var mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization
	err := s.units.ReadCurrent(ctx, func(store Store) error {
		_, parseErr := uuid.Parse(slugOrID)
		event, err := store.PublicEvent(ctx, slugOrID, parseErr == nil)
		if err != nil {
			if errors.Is(err, programevent.ErrEventNotFound) {
				return domainerr.New(domainerr.NotFound, "", "program event not found")
			}
			return err
		}
		blockRead, err := loadProgramEventBlockRead(ctx, store, event)
		if err != nil {
			return err
		}
		localization, err := resolveProgramEventLocalization(
			ctx, store, query.AcceptLanguage, event.ID, blockRead.SourceLocale, blockRead.CompleteLocales,
		)
		if err != nil {
			return err
		}
		detail, err = s.programEventDetail(ctx, store, event, localization, blockRead)
		if err != nil {
			return err
		}
		media, err = store.MediaReferences(ctx, blockRead.DocumentID)
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
	return PublicEvent{Event: detail, BlockMedia: media}, nil
}

// ListProgramEventsQuery is one page of public events.
type ListProgramEventsQuery struct {
	Limit          int32
	Offset         int32
	Filters        []*commonv1.FilterSpec
	Sorts          []*commonv1.SortSpec
	AcceptLanguage string
}

// EventSummary is a public event in a list, in its displayed locale. Its
// MapPlaceID is set only for a location mode that names a Map Place.
type EventSummary struct {
	programevent.Event
	Localization publiccontent.Selection
	PosterAsset  *mediaasset.Reference
	Type         *TypeEntry
}

// EventList is one page of public event summaries.
type EventList struct {
	Events     []EventSummary
	Pagination programevent.Pagination
}

// ListProgramEvents returns one page of published and archived events, TBA
// events first by default.
func (s *Service) ListProgramEvents(ctx context.Context, query ListProgramEventsQuery) (EventList, error) {
	pagination := queryutil.ExtractPaginationRaw(query.Limit, query.Offset, queryutil.DefaultPaginationConfig)
	var events []programevent.Event
	var total int64
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		events, total, err = store.ListPublicEvents(ctx, programevent.ListPage{
			Filters: query.Filters, Sorts: query.Sorts, Limit: pagination.Limit, Offset: pagination.Offset,
		})
		return err
	}); err != nil {
		return EventList{}, err
	}
	summaries, err := s.programEventSummaries(ctx, events, query.AcceptLanguage)
	if err != nil {
		return EventList{}, err
	}
	return EventList{Events: summaries, Pagination: page(pagination, total)}, nil
}

func page(pagination queryutil.Pagination, total int64) programevent.Pagination {
	return programevent.Pagination{
		Total: int32(total), Limit: pagination.Limit, Offset: pagination.Offset, HasMore: pagination.HasMore(total),
	}
}

// programEventSummaries projects a page of events in the locale
// acceptLanguage selects, with their posters and types.
func (s *Service) programEventSummaries(ctx context.Context, events []programevent.Event, acceptLanguage string) ([]EventSummary, error) {
	eventIDs := collectPublicProgramEventIDs(events)
	var localizations map[string]publiccontent.Selection
	if err := s.units.ReadLocked(ctx, func(store Store) error {
		var err error
		localizations, err = store.ResolveLocalizations(ctx, eventIDs, acceptLanguage)
		return err
	}); err != nil {
		return nil, err
	}
	posterAssetsByEvent := s.listPosterAssets(ctx, events)
	eventTypeIDs := collectPublicProgramEventTypeIDs(events)
	var eventTypes map[string]programevent.Type
	var typeLocales map[string][]programevent.TypeLocale
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		if eventTypes, err = store.Types(ctx, eventTypeIDs); err != nil {
			return err
		}
		typeLocales, err = store.TypeLocales(ctx, eventTypeIDs)
		return err
	}); err != nil {
		return nil, err
	}
	summaries := make([]EventSummary, 0, len(events))
	for i := range events {
		event := events[i]
		localization := localizations[event.ID]
		summary := EventSummary{Event: publicLocation(event), Localization: localization, PosterAsset: posterAssetsByEvent[event.ID]}
		if event.TypeID != "" {
			typeRow, ok := eventTypes[event.TypeID]
			if !ok {
				return nil, programevent.ErrTypeNotFound
			}
			eventType, err := publicTypeEntry(typeRow, typeLocales[typeRow.ID], localization.DisplayedLocale)
			if err != nil {
				return nil, err
			}
			summary.Type = &eventType
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// publicLocation clears the Map Place of an event whose location mode does
// not name one.
func publicLocation(event programevent.Event) programevent.Event {
	if !event.LocationMode.UsesMapPlace() {
		event.MapPlaceID = nil
	}
	return event
}

// listPosterAssets resolves the poster of each event of a page in two
// batches, falling back to one lookup per event or per File when a batch
// fails.
func (s *Service) listPosterAssets(ctx context.Context, events []programevent.Event) map[string]*mediaasset.Reference {
	var posterFilesByEvent map[string]string
	posterQueryErr := s.units.Read(ctx, func(store Store) error {
		var err error
		posterFilesByEvent, err = store.PosterFileIDs(ctx, collectPublicProgramEventIDs(events))
		return err
	})
	posterAssetsByEvent := make(map[string]*mediaasset.Reference, len(posterFilesByEvent))
	if posterQueryErr != nil {
		slog.ErrorContext(ctx, "Program Event poster batch lookup failed; resolving posters per event", "domain", "programevent", "operation", "load_poster_file_ids", failurecause.Attr(posterQueryErr))
		for i := range events {
			_ = s.units.Read(ctx, func(store Store) error {
				posterAssetsByEvent[events[i].ID] = loadProgramEventPosterAsset(ctx, store, s.assets, events[i].ID)
				return nil
			})
		}
		return posterAssetsByEvent
	}
	posterFileIDs := make([]string, 0, len(posterFilesByEvent))
	for _, fileID := range posterFilesByEvent {
		posterFileIDs = append(posterFileIDs, fileID)
	}
	assetsByFile := make(map[string]*mediaasset.Reference)
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

func (s *Service) programEventDetail(
	ctx context.Context,
	store Store,
	event programevent.Event,
	localization publiccontent.Selection,
	blockRead programEventPublicBlockRead,
) (EventDetail, error) {
	document, err := contentblock.MaterializeSnapshotRichTextLocale(
		blockRead.Snapshot,
		localization.DisplayedLocale,
	)
	if err != nil {
		return EventDetail{}, fmt.Errorf("materialize Program Event typed document: %w", err)
	}
	var variables sitevariable.Context
	if contentblock.HasSiteVariables(document) {
		variables, err = store.SiteVariables(ctx)
		if err != nil {
			return EventDetail{}, err
		}
	}
	projection, err := contentblock.MaterializeLocalizedRichTextDocument(
		ctx,
		document,
		&programEventPublicFileRenderResolver{store: store, assets: s.assets}, variables,
	)
	if err != nil {
		return EventDetail{}, fmt.Errorf("render Program Event typed document: %w", err)
	}
	detail := EventDetail{
		Event: publicLocation(event), Localization: localization, Document: document,
		ContentHTML: projection.HTML, ContentText: projection.Text,
		DocumentRevision: blockRead.Snapshot.Document.Revision.String(),
		PosterAsset:      loadProgramEventPosterAsset(ctx, store, s.assets, event.ID),
	}
	if detail.MapPlaceID != nil {
		place, err := store.MapPlace(ctx, *detail.MapPlaceID)
		if err != nil {
			return EventDetail{}, err
		}
		detail.LocationPlace = &place
	}
	if event.TypeID != "" {
		eventType, err := loadPublicProgramEventType(ctx, store, event.TypeID, localization.DisplayedLocale)
		if err != nil {
			return EventDetail{}, err
		}
		detail.Type = &eventType
	}
	detail.Series, err = s.loadPublishedProgramEventSeries(ctx, store, event.SeriesID)
	if err != nil {
		return EventDetail{}, err
	}
	if detail.Artists, err = store.Artists(ctx, event.ID); err != nil {
		return EventDetail{}, err
	}
	if detail.Labels, err = store.Labels(ctx, event.ID); err != nil {
		return EventDetail{}, err
	}
	if detail.Clients, err = store.Clients(ctx, event.ID); err != nil {
		return EventDetail{}, err
	}
	if detail.Credits, err = s.loadPublicProgramEventCredits(ctx, store, event.ID); err != nil {
		return EventDetail{}, err
	}
	return detail, nil
}

type programEventPublicBlockRead struct {
	DocumentID      uuid.UUID
	Snapshot        contentblock.Snapshot
	SourceLocale    string
	CompleteLocales []string
}

type programEventPublicFileRenderResolver struct {
	store  Store
	assets Assets
}

func (r *programEventPublicFileRenderResolver) ResolveContentBlockFile(
	ctx context.Context,
	selector contentblock.FileRenderSelector,
) (contentblock.FileRenderTarget, error) {
	if r == nil || r.store == nil || r.assets == nil || selector.BlockID == uuid.Nil || selector.ReferencePath == "" || selector.FileID == uuid.Nil {
		return contentblock.FileRenderTarget{}, fmt.Errorf("invalid public Program Event Content Block File render selector")
	}
	mimeType, found, err := r.store.ContentFileMIMEType(ctx, selector.BlockID, selector.ReferencePath, selector.FileID)
	if err != nil {
		return contentblock.FileRenderTarget{}, fmt.Errorf("load exact public Program Event Content Block File: %w", err)
	}
	if !found {
		return contentblock.FileRenderTarget{}, fmt.Errorf("exact public Program Event Content Block File reference does not exist")
	}
	target := contentblock.FileRenderTarget{MIMEType: mimeType}
	if !strings.HasPrefix(mimeType, "image/") {
		return target, nil
	}
	asset := r.assets.ResolveReadyAssetForSourceFile(ctx, selector.FileID.String(), "image")
	if asset == nil {
		return contentblock.FileRenderTarget{}, fmt.Errorf("public Program Event Content Block image asset is unavailable")
	}
	target.URL = optional.Value(asset).URL
	target.MIMEType = optional.Value(asset).MimeType
	return target, nil
}

func loadProgramEventBlockRead(
	ctx context.Context,
	store Store,
	event programevent.Event,
) (programEventPublicBlockRead, error) {
	if event.ContentDocumentID == nil || strings.TrimSpace(*event.ContentDocumentID) == "" {
		return programEventPublicBlockRead{}, domainerr.Precondition("Program Event content document is not initialized")
	}
	documentID, err := uuid.Parse(*event.ContentDocumentID)
	if err != nil {
		return programEventPublicBlockRead{}, fmt.Errorf("invalid Program Event content_document_id: %w", err)
	}
	if strings.TrimSpace(event.SourceLocale) == "" {
		return programEventPublicBlockRead{}, domainerr.Precondition("Program Event source locale is not initialized")
	}
	snapshot, err := store.ContentSnapshot(ctx, documentID, event.SourceLocale)
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
	store Store,
	seriesID *string,
) (*SeriesEntry, error) {
	if seriesID == nil {
		return nil, nil
	}
	series, found, err := store.PublishedSeries(ctx, *seriesID)
	if err != nil || !found {
		return nil, err
	}
	entry := seriesEntry(ctx, s.assets, series)
	return &entry, nil
}

func resolveProgramEventLocalization(
	ctx context.Context,
	store Store,
	acceptLanguage string,
	eventID string,
	sourceLocale string,
	completeDocumentLocales []string,
) (publiccontent.Selection, error) {
	settings, settingsErr := store.TranslationSettings(ctx)
	if settingsErr != nil {
		slog.ErrorContext(ctx, "Translation runtime settings lookup failed; using default Program Event localization settings", "domain", "programevent", "operation", "load_localization_settings", failurecause.Attr(settingsErr), "event_id", eventID)
		settings = translation.DefaultRuntimeSettings()
	}
	sourceLocale = normalizeSourceLocaleWithDefault(sourceLocale, settings.DefaultLocale)
	selection, err := store.ResolveLocalization(ctx, eventID, acceptLanguage, settings)
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
			selection, err = store.ResolveLocalization(ctx, eventID, fallbackLocale, settings)
			if err != nil {
				return publiccontent.Selection{}, err
			}
			selection.RequestedLocale = requestedLocale
			selection.IsFallback = true
			selection.FallbackReason = openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_SOURCE
		}
	}
	available, availableErr := store.AvailableLocales(ctx, eventID, sourceLocale, settings)
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
