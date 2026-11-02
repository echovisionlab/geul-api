package programevent

import (
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"github.com/echovisionlab/geul-api/internal/memberref"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// programEventStatusToProto maps a stored event status to the API enum. A
// value the API does not know is UNSPECIFIED.
func programEventStatusToProto(status Status) managev1.ProgramEventStatus {
	switch status {
	case StatusDraft:
		return managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT
	case StatusPublished:
		return managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED
	case StatusArchived:
		return managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED
	default:
		return managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_UNSPECIFIED
	}
}

// programEventStatusFromProto maps an API event status to the stored value. It
// reports false for UNSPECIFIED and unknown values.
func programEventStatusFromProto(status managev1.ProgramEventStatus) (Status, bool) {
	switch status {
	case managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT:
		return StatusDraft, true
	case managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED:
		return StatusPublished, true
	case managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED:
		return StatusArchived, true
	default:
		return "", false
	}
}

// programEventSeriesStatusToProto maps a stored series status, which holds
// the event draft and published values, to the series API enum.
func programEventSeriesStatusToProto(status Status) managev1.ProgramEventSeriesStatus {
	switch status {
	case StatusDraft:
		return managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_DRAFT
	case StatusPublished:
		return managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED
	default:
		return managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_UNSPECIFIED
	}
}

// programEventSeriesStatusFromProto maps a series API status to the stored
// value. It reports false for UNSPECIFIED and unknown values.
func programEventSeriesStatusFromProto(status managev1.ProgramEventSeriesStatus) (Status, bool) {
	switch status {
	case managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_DRAFT:
		return StatusDraft, true
	case managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED:
		return StatusPublished, true
	default:
		return "", false
	}
}

// programEventTypeStatusToProto maps a stored type status to the API enum.
func programEventTypeStatusToProto(status TypeStatus) managev1.ProgramEventTypeStatus {
	switch status {
	case TypeStatusActive:
		return managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE
	case TypeStatusInactive:
		return managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_INACTIVE
	default:
		return managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_UNSPECIFIED
	}
}

// programEventTypeStatusFromProto maps an API type status to the stored
// value. It reports false for UNSPECIFIED and unknown values.
func programEventTypeStatusFromProto(status managev1.ProgramEventTypeStatus) (TypeStatus, bool) {
	switch status {
	case managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE:
		return TypeStatusActive, true
	case managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_INACTIVE:
		return TypeStatusInactive, true
	default:
		return "", false
	}
}

// locationModeToProto maps a stored location mode to the API enum.
func locationModeToProto(mode LocationMode) managev1.ProgramEventLocationMode {
	switch mode {
	case LocationModeMapPlace:
		return managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_MAP_PLACE
	case LocationModeOnline:
		return managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE
	case LocationModeHybrid:
		return managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_HYBRID
	case LocationModeTBA:
		return managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_TBA
	default:
		return managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_UNSPECIFIED
	}
}

// locationModeFromProto maps an API location mode to the stored value.
// UNSPECIFIED is a Map Place; an unknown value is empty, which location
// validation rejects as unsupported.
func locationModeFromProto(mode managev1.ProgramEventLocationMode) LocationMode {
	switch mode {
	case managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_UNSPECIFIED,
		managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_MAP_PLACE:
		return LocationModeMapPlace
	case managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE:
		return LocationModeOnline
	case managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_HYBRID:
		return LocationModeHybrid
	case managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_TBA:
		return LocationModeTBA
	default:
		return ""
	}
}

func timestampPtr(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	value := ts.AsTime()
	return &value
}

func timestampProtoPtr(ts *time.Time) *timestamppb.Timestamp {
	if ts == nil {
		return nil
	}
	return timestamppb.New(*ts)
}

// PaginationMessage is the API form of a page position.
func PaginationMessage(page Pagination) *commonv1.PaginationResponse {
	return &commonv1.PaginationResponse{Total: page.Total, Limit: page.Limit, Offset: page.Offset, HasMore: page.HasMore}
}

// EventMessage is the API form of an event's administrator view.
func EventMessage(event EventView) *managev1.ProgramEvent {
	locales := make([]*managev1.ProgramEventLocale, 0, len(event.Locales))
	for _, locale := range event.Locales {
		html, text := locale.ContentHTML, locale.ContentText
		locales = append(locales, &managev1.ProgramEventLocale{
			Locale: locale.Locale, Summary: locale.Summary, ContentHtml: &html, ContentText: &text,
		})
	}
	artists := make([]*managev1.ProgramEventArtist, 0, len(event.Artists))
	for _, row := range event.Artists {
		artists = append(artists, &managev1.ProgramEventArtist{ArtistId: row.ID, Role: row.Role, SortOrder: row.SortOrder})
	}
	labels := make([]*managev1.ProgramEventLabel, 0, len(event.Labels))
	for _, row := range event.Labels {
		labels = append(labels, &managev1.ProgramEventLabel{LabelId: row.ID, Role: row.Role, SortOrder: row.SortOrder})
	}
	clients := make([]*managev1.ProgramEventClient, 0, len(event.Clients))
	for _, row := range event.Clients {
		clients = append(clients, &managev1.ProgramEventClient{ClientId: row.ID, Role: row.Role, SortOrder: row.SortOrder})
	}
	credits := make([]*managev1.ProgramEventCredit, 0, len(event.Credits))
	for _, credit := range event.Credits {
		credits = append(credits, CreditMessage(credit))
	}
	media := make([]*managev1.ProgramEventMedia, 0, len(event.Media))
	for _, item := range event.Media {
		media = append(media, MediaMessage(item))
	}
	return &managev1.ProgramEvent{
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
		PosterFileId:     event.PosterFileID,
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
		Document:         event.Document,
		DocumentRevision: event.DocumentRevision,
		BlockMedia:       event.BlockMedia,
	}
}

// EventSummaryMessage is the API form of an event in the administrator
// list.
func EventSummaryMessage(event EventSummary) *managev1.ProgramEventSummary {
	slug := event.Slug
	return &managev1.ProgramEventSummary{
		Id:           event.ID,
		Status:       programEventStatusToProto(event.Status),
		SourceLocale: event.SourceLocale,
		Title:        event.Title,
		Slug:         &slug,
		TypeId:       event.TypeID,
		SeriesId:     event.SeriesID,
		StartsAt:     timestamppb.New(event.StartsAt),
		EndsAt:       timestampProtoPtr(event.EndsAt),
		Timezone:     event.Timezone,
		LocationMode: locationModeToProto(event.LocationMode),
		MapPlaceId:   event.MapPlaceID,
		PosterFileId: event.PosterFileID,
		PublishedAt:  timestampProtoPtr(event.PublishedAt),
		UpdatedAt:    timestamppb.New(event.UpdatedAt),
	}
}

// MediaMessage is the API form of a media relation.
func MediaMessage(media Media) *managev1.ProgramEventMedia {
	return &managev1.ProgramEventMedia{
		Id:        media.ID,
		FileId:    media.FileID,
		Role:      media.Role,
		SortOrder: media.SortOrder,
		IsPrimary: media.IsPrimary,
		Alt:       media.Alt,
		Caption:   media.Caption,
		CreatedAt: timestamppb.New(media.CreatedAt),
		UpdatedAt: timestamppb.New(media.UpdatedAt),
	}
}

// CreditMessage is the API form of a credit with its projections.
func CreditMessage(credit CreditView) *managev1.ProgramEventCredit {
	message := &managev1.ProgramEventCredit{
		Id:          credit.ID,
		ArtistId:    credit.ArtistID,
		MemberId:    credit.MemberID,
		DisplayName: credit.DisplayName,
		CreditRole:  credit.CreditRole,
		Description: credit.Description,
		SortOrder:   credit.SortOrder,
		Member:      memberref.ReferenceMessage(credit.Member),
	}
	if credit.Artist != nil {
		message.Artist = &managev1.ProgramEventCreditArtist{
			Id: credit.Artist.ID, Name: credit.Artist.Name, Slug: credit.Artist.Slug, ImageAsset: mediaasset.ReferenceProto(credit.Artist.ImageAsset),
		}
	}
	return message
}

// SeriesMessage is the API form of a series.
func SeriesMessage(series Series) *managev1.ProgramEventSeries {
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
	}
}

// TypeMessage is the API form of a type with its locales.
func TypeMessage(eventType TypeView) *managev1.ProgramEventType {
	locales := make([]*managev1.ProgramEventTypeLocale, 0, len(eventType.Locales))
	for _, locale := range eventType.Locales {
		locales = append(locales, &managev1.ProgramEventTypeLocale{
			Locale: locale.Locale, Name: locale.Name, Description: locale.Description,
		})
	}
	return &managev1.ProgramEventType{
		Id:                eventType.ID,
		Slug:              eventType.Slug,
		Status:            programEventTypeStatusToProto(eventType.Status),
		SortOrder:         eventType.SortOrder,
		RequiresPlace:     eventType.RequiresPlace,
		RequiresStreamUrl: eventType.RequiresStreamURL,
		Locales:           locales,
		CreatedAt:         timestamppb.New(eventType.CreatedAt),
		UpdatedAt:         timestamppb.New(eventType.UpdatedAt),
	}
}

func blockLocaleMetadataMessage(metadata *BlockLocaleMetadata) *intrav1.ProgramEventLocaleMetadata {
	if metadata == nil {
		return nil
	}
	return &intrav1.ProgramEventLocaleMetadata{Locale: metadata.Locale, Title: metadata.Title, Summary: metadata.Summary}
}

func roomDocumentMessage(room RoomDocument) *intrav1.LoadProgramEventBlockDocumentResponse {
	return &intrav1.LoadProgramEventBlockDocumentResponse{
		Document: room.Document, DocumentRevision: room.DocumentRevision,
		SourceMetadata: blockLocaleMetadataMessage(room.SourceMetadata), BlockMedia: room.BlockMedia, Locale: room.Locale,
		LocaleExists: room.LocaleExists, LocaleMetadata: blockLocaleMetadataMessage(room.LocaleMetadata),
		TargetRevision:      room.TargetRevision,
		PresentLocaleValues: room.PresentLocaleValues,
	}
}
