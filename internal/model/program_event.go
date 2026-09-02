package model

import (
	"time"
)

type ProgramEventType struct {
	ID                string    `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	Slug              string    `gorm:"column:slug;type:text;not null;uniqueIndex"`
	Status            string    `gorm:"column:status;type:varchar(40);not null;default:PROGRAM_EVENT_TYPE_STATUS_ACTIVE"`
	SortOrder         int32     `gorm:"column:sort_order;type:int;not null;default:0"`
	RequiresPlace     bool      `gorm:"column:requires_place;type:boolean;not null;default:false"`
	RequiresStreamURL bool      `gorm:"column:requires_stream_url;type:boolean;not null;default:false"`
	CreatedAt         time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt         time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (ProgramEventType) TableName() string {
	return "program_event_type"
}

type ProgramEventTypeLocale struct {
	TypeID      string    `gorm:"column:type_id;type:uuid;primaryKey"`
	Locale      string    `gorm:"column:locale;primaryKey"`
	Name        string    `gorm:"column:name;type:text;not null"`
	Description *string   `gorm:"column:description;type:text"`
	CreatedAt   time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt   time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (ProgramEventTypeLocale) TableName() string {
	return "program_event_type_locale"
}

type ProgramEventSeries struct {
	ID           string    `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	Slug         string    `gorm:"column:slug;type:text;not null;uniqueIndex"`
	Status       string    `gorm:"column:status;type:varchar(40);not null;default:PROGRAM_EVENT_STATUS_DRAFT"`
	Title        string    `gorm:"column:title;type:text;not null"`
	Summary      *string   `gorm:"column:summary;type:text"`
	Description  *string   `gorm:"column:description;type:text"`
	PosterFileID *string   `gorm:"column:poster_file_id;type:uuid"`
	CreatedAt    time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt    time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (ProgramEventSeries) TableName() string {
	return "program_event_series"
}

type ProgramEvent struct {
	ID                string     `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	ContentDocumentID *string    `gorm:"column:content_document_id;type:uuid;uniqueIndex"`
	Title             string     `gorm:"column:title;type:text;not null;default:''"`
	Slug              string     `gorm:"column:slug;type:text;not null;uniqueIndex"`
	Status            string     `gorm:"column:status;type:varchar(40);not null;default:PROGRAM_EVENT_STATUS_DRAFT"`
	SourceLocale      string     `gorm:"column:source_locale;type:text;not null;default:en"`
	TypeID            string     `gorm:"column:type_id;type:uuid;not null"`
	SeriesID          *string    `gorm:"column:series_id;type:uuid"`
	SeriesOrder       *int32     `gorm:"column:series_order;type:int"`
	StartsAt          time.Time  `gorm:"column:starts_at;not null"`
	EndsAt            *time.Time `gorm:"column:ends_at"`
	Timezone          string     `gorm:"column:timezone;type:text;not null;default:UTC"`
	AllDay            bool       `gorm:"column:all_day;type:boolean;not null;default:false"`
	LocationMode      string     `gorm:"column:location_mode;type:varchar(40);not null;default:PROGRAM_EVENT_LOCATION_MODE_MAP_PLACE"`
	MapPlaceID        *string    `gorm:"column:map_place_id;type:uuid"`
	TicketURL         *string    `gorm:"column:ticket_url;type:text"`
	StreamURL         *string    `gorm:"column:stream_url;type:text"`
	ExternalURL       *string    `gorm:"column:external_url;type:text"`
	PublishedAt       *time.Time `gorm:"column:published_at"`
	CreatedAt         time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt         time.Time  `gorm:"column:updated_at;not null;default:now()"`
	MapPlace          *MapPlace  `gorm:"foreignKey:MapPlaceID"`
}

func (ProgramEvent) TableName() string {
	return "program_event"
}

type ProgramEventTranslation struct {
	EntityID  string    `gorm:"column:entity_id;type:uuid;primaryKey"`
	Locale    string    `gorm:"column:locale;primaryKey"`
	Summary   *string   `gorm:"column:summary;type:text"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (ProgramEventTranslation) TableName() string {
	return "program_event_translation"
}

type ProgramEventMedia struct {
	ID        string    `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	EventID   string    `gorm:"column:event_id;type:uuid;not null"`
	FileID    string    `gorm:"column:file_id;type:uuid;not null"`
	Role      string    `gorm:"column:role;type:varchar(40);not null;default:poster"`
	SortOrder int32     `gorm:"column:sort_order;type:int;not null;default:0"`
	IsPrimary bool      `gorm:"column:is_primary;not null;default:false"`
	Alt       *string   `gorm:"column:alt;type:text"`
	Caption   *string   `gorm:"column:caption;type:text"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (ProgramEventMedia) TableName() string {
	return "program_event_media"
}

type ProgramEventArtist struct {
	EventID   string    `gorm:"column:event_id;type:uuid;primaryKey"`
	ArtistID  string    `gorm:"column:artist_id;type:uuid;primaryKey"`
	Role      *string   `gorm:"column:role;type:text"`
	SortOrder int32     `gorm:"column:sort_order;type:int;not null;default:0"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
}

func (ProgramEventArtist) TableName() string {
	return "program_event_artist"
}

type ProgramEventLabel struct {
	EventID   string    `gorm:"column:event_id;type:uuid;primaryKey"`
	LabelID   string    `gorm:"column:label_id;type:uuid;primaryKey"`
	Role      *string   `gorm:"column:role;type:text"`
	SortOrder int32     `gorm:"column:sort_order;type:int;not null;default:0"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
}

func (ProgramEventLabel) TableName() string {
	return "program_event_label"
}

type ProgramEventClient struct {
	EventID   string    `gorm:"column:event_id;type:uuid;primaryKey"`
	ClientID  string    `gorm:"column:client_id;type:uuid;primaryKey"`
	Role      *string   `gorm:"column:role;type:text"`
	SortOrder int32     `gorm:"column:sort_order;type:int;not null;default:0"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
}

func (ProgramEventClient) TableName() string {
	return "program_event_client"
}

type ProgramEventCredit struct {
	ID          string    `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	EventID     string    `gorm:"column:event_id;type:uuid;not null"`
	ArtistID    *string   `gorm:"column:artist_id;type:uuid"`
	MemberID    *string   `gorm:"column:member_id;type:uuid"`
	DisplayName *string   `gorm:"column:display_name;type:text"`
	CreditRole  *string   `gorm:"column:credit_role;type:text"`
	Description *string   `gorm:"column:description;type:text"`
	SortOrder   int32     `gorm:"column:sort_order;type:int;not null;default:0"`
	CreatedAt   time.Time `gorm:"column:created_at;not null;default:now()"`
}

func (ProgramEventCredit) TableName() string {
	return "program_event_credit"
}
