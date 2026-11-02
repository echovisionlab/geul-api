package public

import (
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// openProgramEventStatus maps a stored event status to the public API enum.
// A value the API does not know is UNSPECIFIED.
func openProgramEventStatus(status programevent.Status) openv1.ProgramEventStatus {
	switch status {
	case programevent.StatusDraft:
		return openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT
	case programevent.StatusPublished:
		return openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED
	case programevent.StatusArchived:
		return openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED
	default:
		return openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_UNSPECIFIED
	}
}

// openProgramEventSeriesStatus maps a stored series status, which holds the
// event draft and published values, to the public series enum.
func openProgramEventSeriesStatus(status programevent.Status) openv1.ProgramEventSeriesStatus {
	switch status {
	case programevent.StatusPublished:
		return openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED
	case programevent.StatusDraft:
		return openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_DRAFT
	default:
		return openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_UNSPECIFIED
	}
}

// openProgramEventLocationMode maps a stored location mode to the public
// enum.
func openProgramEventLocationMode(mode programevent.LocationMode) openv1.ProgramEventLocationMode {
	switch mode {
	case programevent.LocationModeMapPlace:
		return openv1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_MAP_PLACE
	case programevent.LocationModeOnline:
		return openv1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE
	case programevent.LocationModeHybrid:
		return openv1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_HYBRID
	case programevent.LocationModeTBA:
		return openv1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_TBA
	default:
		return openv1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_UNSPECIFIED
	}
}

// openProgramEventTypeStatus maps a stored type status to the public enum.
func openProgramEventTypeStatus(status programevent.TypeStatus) openv1.ProgramEventTypeStatus {
	switch status {
	case programevent.TypeStatusActive:
		return openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE
	case programevent.TypeStatusInactive:
		return openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_INACTIVE
	default:
		return openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_UNSPECIFIED
	}
}

func timestampProto(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}

func paginationMessage(page programevent.Pagination) *commonv1.PaginationResponse {
	return &commonv1.PaginationResponse{Total: page.Total, Limit: page.Limit, Offset: page.Offset, HasMore: page.HasMore}
}

func typeMessage(eventType *TypeEntry) *openv1.ProgramEventType {
	if eventType == nil {
		return nil
	}
	return &openv1.ProgramEventType{
		Id:                eventType.ID,
		Slug:              eventType.Slug,
		Status:            openProgramEventTypeStatus(eventType.Status),
		SortOrder:         eventType.SortOrder,
		Name:              eventType.Name,
		Description:       eventType.Description,
		RequiresPlace:     eventType.RequiresPlace,
		RequiresStreamUrl: eventType.RequiresStreamURL,
	}
}

func seriesMessage(series *SeriesEntry) *openv1.ProgramEventSeries {
	if series == nil {
		return nil
	}
	return &openv1.ProgramEventSeries{
		Id:          series.ID,
		Status:      openProgramEventSeriesStatus(series.Status),
		Title:       series.Title,
		Slug:        series.Slug,
		Summary:     series.Summary,
		Description: series.Description,
		PosterAsset: mediaasset.ReferenceProto(series.PosterAsset),
	}
}

func summaryMessage(summary EventSummary) *openv1.ProgramEventSummary {
	event := summary.Event
	return &openv1.ProgramEventSummary{
		Id:               event.ID,
		Status:           openProgramEventStatus(event.Status),
		Title:            event.Title,
		Slug:             &event.Slug,
		Summary:          summary.Localization.Summary,
		TypeId:           event.TypeID,
		SeriesId:         event.SeriesID,
		SeriesOrder:      event.SeriesOrder,
		StartsAt:         timestamppb.New(event.StartsAt),
		EndsAt:           timestampProto(event.EndsAt),
		Timezone:         event.Timezone,
		AllDay:           event.AllDay,
		LocationMode:     openProgramEventLocationMode(event.LocationMode),
		MapPlaceId:       event.MapPlaceID,
		PublishedAt:      timestampProto(event.PublishedAt),
		UpdatedAt:        timestamppb.New(event.UpdatedAt),
		LocalizationInfo: publiccontent.ToProtoLocalizationInfo(summary.Localization),
		PosterAsset:      mediaasset.ReferenceProto(summary.PosterAsset),
		Type:             typeMessage(summary.Type),
	}
}

func participantMessages[Row any](rows []Participant, message func(Participant) *Row) []*Row {
	result := make([]*Row, 0, len(rows))
	for _, row := range rows {
		result = append(result, message(row))
	}
	return result
}

func eventMessage(detail EventDetail) *openv1.ProgramEvent {
	event := detail.Event
	html, text := detail.ContentHTML, detail.ContentText
	message := &openv1.ProgramEvent{
		Id:               event.ID,
		Status:           openProgramEventStatus(event.Status),
		Title:            event.Title,
		Slug:             &event.Slug,
		Summary:          detail.Localization.Summary,
		ContentHtml:      &html,
		ContentText:      &text,
		TypeId:           event.TypeID,
		SeriesId:         event.SeriesID,
		SeriesOrder:      event.SeriesOrder,
		StartsAt:         timestamppb.New(event.StartsAt),
		EndsAt:           timestampProto(event.EndsAt),
		Timezone:         event.Timezone,
		AllDay:           event.AllDay,
		LocationMode:     openProgramEventLocationMode(event.LocationMode),
		MapPlaceId:       event.MapPlaceID,
		TicketUrl:        event.TicketURL,
		StreamUrl:        event.StreamURL,
		ExternalUrl:      event.ExternalURL,
		PublishedAt:      timestampProto(event.PublishedAt),
		UpdatedAt:        timestamppb.New(event.UpdatedAt),
		LocalizationInfo: publiccontent.ToProtoLocalizationInfo(detail.Localization),
		Document:         detail.Document,
		DocumentRevision: detail.DocumentRevision,
		PosterAsset:      mediaasset.ReferenceProto(detail.PosterAsset),
		Type:             typeMessage(detail.Type),
		Series:           seriesMessage(detail.Series),
	}
	if place := detail.LocationPlace; place != nil {
		message.LocationPlace = &openv1.MapPlaceBasic{Id: place.ID, Name: place.Name, Address: place.Address, Lat: place.Lat, Lng: place.Lng}
		if place.GooglePlaceID != nil && *place.GooglePlaceID != "" {
			message.LocationPlace.GooglePlaceId = place.GooglePlaceID
		}
	}
	message.Artists = participantMessages(detail.Artists, func(row Participant) *openv1.ProgramEventArtist {
		return &openv1.ProgramEventArtist{Id: row.ID, Name: row.Name, Slug: row.Slug, Role: row.Role, SortOrder: row.SortOrder}
	})
	message.Labels = participantMessages(detail.Labels, func(row Participant) *openv1.ProgramEventLabel {
		return &openv1.ProgramEventLabel{Id: row.ID, Name: row.Name, Slug: row.Slug, Role: row.Role, SortOrder: row.SortOrder}
	})
	message.Clients = make([]*openv1.ProgramEventClient, 0, len(detail.Clients))
	for _, row := range detail.Clients {
		message.Clients = append(message.Clients, &openv1.ProgramEventClient{Id: row.ID, Name: row.Name, Website: row.Website, Role: row.Role, SortOrder: row.SortOrder})
	}
	message.Credits = make([]*openv1.ProgramEventCredit, 0, len(detail.Credits))
	for _, credit := range detail.Credits {
		item := &openv1.ProgramEventCredit{
			Id: credit.ID, DisplayName: credit.DisplayName, CreditRole: credit.CreditRole,
			Description: credit.Description, SortOrder: credit.SortOrder, Member: memberref.ReferenceMessage(credit.Member),
		}
		if credit.Artist != nil {
			item.Artist = &openv1.ProgramEventArtist{Id: credit.Artist.ID, Name: credit.Artist.Name, Slug: credit.Artist.Slug, ImageAsset: mediaasset.ReferenceProto(credit.Artist.ImageAsset)}
		}
		message.Credits = append(message.Credits, item)
	}
	return message
}
